/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package selfupdate

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckerInstallsVerifiedBinary(t *testing.T) {
	binary := []byte("new cpak")
	digest := sha256.Sum256(binary)
	companion := []byte("new fvs service")
	companionDigest := sha256.Sum256(companion)
	checksums := fmt.Sprintf("%x  cpak-linux-%s\n%x  cpak-storaged-linux-%s\n", digest[:], runtime.GOARCH, companionDigest[:], runtime.GOARCH)
	bundle := []byte("signed checksums")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/cpak":
			_, _ = writer.Write(binary)
		case "/cpak-storaged":
			_, _ = writer.Write(companion)
		case "/SHA256SUMS":
			_, _ = writer.Write([]byte(checksums))
		case "/SHA256SUMS.sigstore.json":
			_, _ = writer.Write(bundle)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "cpak")
	if err := os.WriteFile(target, []byte("old cpak"), 0755); err != nil {
		t.Fatal(err)
	}
	var migrated string
	var steps []string
	checker := Checker{
		Executable: target,
		PrepareRuntime: func(_ context.Context, executable string) error {
			installed, err := os.ReadFile(executable)
			if err != nil || string(installed) != string(binary) {
				return fmt.Errorf("runtime preparation saw %q: %w", installed, err)
			}
			steps = append(steps, "runtime")
			return nil
		},
		Migrate: func(_ context.Context, executable string) error {
			migrated = executable
			steps = append(steps, "migration")
			return nil
		},
		VerifyChecksums: func(gotChecksums, gotBundle []byte) error {
			if string(gotChecksums) != checksums || string(gotBundle) != string(bundle) {
				return errors.New("selfupdate: verifier received the wrong release artifacts")
			}
			return nil
		},
	}
	release := Release{
		BinaryURL:    server.URL + "/cpak",
		StorageURL:   server.URL + "/cpak-storaged",
		ChecksumsURL: server.URL + "/SHA256SUMS",
		SignatureURL: server.URL + "/SHA256SUMS.sigstore.json",
	}
	if err := checker.Install(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != string(binary) {
		t.Fatalf("unexpected installed binary: %q %v", installed, err)
	}
	installedCompanion, err := os.ReadFile(filepath.Join(filepath.Dir(target), "cpak-storaged"))
	if err != nil || string(installedCompanion) != string(companion) {
		t.Fatalf("unexpected installed FVS service: %q %v", installedCompanion, err)
	}
	if migrated != target {
		t.Fatalf("storage migration used %q", migrated)
	}
	if strings.Join(steps, ",") != "runtime,migration" {
		t.Fatalf("update steps ran in the wrong order: %v", steps)
	}
}

func TestCheckerRejectsInvalidChecksum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/SHA256SUMS":
			_, _ = fmt.Fprintf(writer, "%064d  cpak-linux-%s\n", 0, runtime.GOARCH)
		case "/SHA256SUMS.sigstore.json":
			_, _ = writer.Write([]byte("signature"))
		default:
			_, _ = writer.Write([]byte("new cpak"))
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "cpak")
	if err := os.WriteFile(target, []byte("old cpak"), 0755); err != nil {
		t.Fatal(err)
	}
	checker := Checker{Executable: target, VerifyChecksums: func([]byte, []byte) error { return nil }}
	err := checker.Install(context.Background(), Release{
		BinaryURL:    server.URL,
		StorageURL:   server.URL + "/cpak-storaged",
		ChecksumsURL: server.URL + "/SHA256SUMS",
		SignatureURL: server.URL + "/SHA256SUMS.sigstore.json",
	})
	if err == nil {
		t.Fatal("installed a binary with an invalid checksum")
	}
	installed, _ := os.ReadFile(target)
	if string(installed) != "old cpak" {
		t.Fatalf("modified target after checksum failure: %q", installed)
	}
}

func TestCheckerRejectsUntrustedChecksumsBeforeDownloadingBinaries(t *testing.T) {
	verificationError := errors.New("untrusted checksums")
	binaryRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/SHA256SUMS":
			_, _ = writer.Write([]byte("checksums"))
		case "/SHA256SUMS.sigstore.json":
			_, _ = writer.Write([]byte("signature"))
		default:
			binaryRequested = true
			_, _ = writer.Write([]byte("new cpak"))
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "cpak")
	if err := os.WriteFile(target, []byte("old cpak"), 0755); err != nil {
		t.Fatal(err)
	}
	checker := Checker{
		Executable: target,
		VerifyChecksums: func([]byte, []byte) error {
			return verificationError
		},
	}
	err := checker.Install(context.Background(), Release{
		BinaryURL:    server.URL + "/cpak",
		StorageURL:   server.URL + "/cpak-storaged",
		ChecksumsURL: server.URL + "/SHA256SUMS",
		SignatureURL: server.URL + "/SHA256SUMS.sigstore.json",
	})
	if !errors.Is(err, verificationError) {
		t.Fatalf("got %v, want the verification failure", err)
	}
	if binaryRequested {
		t.Fatal("downloaded binaries after checksum verification failed")
	}
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != "old cpak" {
		t.Fatalf("modified target after signature failure: %q %v", installed, err)
	}
}

func TestCheckerUsesCachedRelease(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		_, _ = writer.Write([]byte(`{"tag_name":"v2.1.0","body":"notes","published_at":"2026-08-12T00:00:00Z","assets":[]}`))
	}))
	defer server.Close()
	directory := t.TempDir()
	target := filepath.Join(directory, "cpak")
	for _, path := range []string{target, filepath.Join(directory, "cpak-storaged")} {
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	checker := Checker{CurrentVersion: "v2.0.0", APIURL: server.URL, CachePath: filepath.Join(directory, "cache.json"), Executable: target}
	for range 2 {
		release, available, err := checker.Check(context.Background(), 24*time.Hour)
		if err != nil || !available || release.Version != "v2.1.0" {
			t.Fatalf("unexpected update check: %+v %v %v", release, available, err)
		}
	}
	if requests != 1 {
		t.Fatalf("expected one release request, got %d", requests)
	}
}

func TestCheckerFindsTheChecksumSignatureAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		baseURL := "http://" + request.Host
		_, _ = fmt.Fprintf(writer, `{
			"tag_name":"v2.1.0",
			"published_at":"2026-08-12T00:00:00Z",
			"assets":[
				{"name":"cpak-linux-%s","browser_download_url":"%s/cpak"},
				{"name":"cpak-storaged-linux-%s","browser_download_url":"%s/cpak-storaged"},
				{"name":"SHA256SUMS","browser_download_url":"%s/SHA256SUMS"},
				{"name":"SHA256SUMS.sigstore.json","browser_download_url":"%s/SHA256SUMS.sigstore.json"}
			]
		}`, runtime.GOARCH, baseURL, runtime.GOARCH, baseURL, baseURL, baseURL)
	}))
	defer server.Close()

	release, err := (Checker{APIURL: server.URL}).fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.SignatureURL != server.URL+"/SHA256SUMS.sigstore.json" {
		t.Fatalf("unexpected checksum signature URL: %q", release.SignatureURL)
	}
}

func TestCheckerRepairsMissingStorageServiceAtTheCurrentVersion(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "cpak")
	if err := os.WriteFile(target, []byte("cpak"), 0o755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(directory, "cache.json")
	release := Release{Version: "v2.2.0", StorageURL: "https://example.com/cpak-storaged"}
	if err := writeCache(cachePath, cache{CheckedAt: time.Now().UTC(), Release: release}); err != nil {
		t.Fatal(err)
	}
	checker := Checker{CurrentVersion: "v2.2.0", Executable: target, CachePath: cachePath}
	_, available, err := checker.Check(context.Background(), 24*time.Hour)
	if err != nil || !available {
		t.Fatalf("missing storage service was not repairable: available=%v err=%v", available, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "cpak-storaged"), []byte("storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, available, err = checker.Check(context.Background(), 24*time.Hour)
	if err != nil || available {
		t.Fatalf("complete installation requested a repair: available=%v err=%v", available, err)
	}
}

func TestManagedCheckerDoesNotReplaceBinary(t *testing.T) {
	checker := Checker{Mode: "managed"}
	if err := checker.Install(context.Background(), Release{}); err != ErrManagedInstall {
		t.Fatalf("unexpected managed install error: %v", err)
	}
}

func TestCheckerRejectsInsecureDownloadURL(t *testing.T) {
	checker := Checker{}
	if _, err := checker.download(context.Background(), "http://example.com/cpak", 1024); err == nil {
		t.Fatal("insecure download URL was accepted")
	}
}

func TestStableReleaseReplacesMatchingPrerelease(t *testing.T) {
	if !newer("v2.0.1", "v2.0.1-rc.1") {
		t.Fatal("stable release was not newer than its release candidate")
	}
	if newer("v2.0.1-rc.1", "v2.0.1") {
		t.Fatal("release candidate was newer than its stable release")
	}
}
