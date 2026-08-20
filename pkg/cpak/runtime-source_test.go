/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func runtimeChecksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func TestValidateRuntimeSource(t *testing.T) {
	valid := types.RuntimeSource{URL: "https://example.com/demo.deb", SHA256: strings.Repeat("a", 64), Size: 42, Installer: "dpkg"}
	tests := []struct {
		name   string
		source types.RuntimeSource
		valid  bool
	}{
		{name: "dpkg", source: valid, valid: true},
		{name: "deb extract", source: types.RuntimeSource{URL: "https://example.com/demo.deb", SHA256: valid.SHA256, Size: 42, Installer: "deb-extract"}, valid: true},
		{name: "tar", source: types.RuntimeSource{URL: "https://example.com/demo.tar.gz", SHA256: valid.SHA256, Size: 42, Installer: "tar"}, valid: true},
		{name: "rpm", source: types.RuntimeSource{URL: "https://example.com/demo.rpm", SHA256: valid.SHA256, Size: 42, Installer: "rpm"}, valid: true},
		{name: "file", source: types.RuntimeSource{URL: "https://example.com/demo.jar", SHA256: valid.SHA256, Size: 42, Installer: "file", Destination: "/opt/demo/demo.jar"}, valid: true},
		{name: "file without destination", source: types.RuntimeSource{URL: "https://example.com/demo.jar", SHA256: valid.SHA256, Size: 42, Installer: "file"}},
		{name: "file outside opt", source: types.RuntimeSource{URL: "https://example.com/demo.jar", SHA256: valid.SHA256, Size: 42, Installer: "file", Destination: "/usr/bin/demo"}},
		{name: "destination on package", source: types.RuntimeSource{URL: valid.URL, SHA256: valid.SHA256, Size: 42, Installer: "dpkg", Destination: "/opt/demo/demo.deb"}},
		{name: "http", source: types.RuntimeSource{URL: "http://example.com/demo.deb", SHA256: valid.SHA256, Size: 42, Installer: "dpkg"}},
		{name: "bad checksum", source: types.RuntimeSource{URL: valid.URL, SHA256: "bad", Size: 42, Installer: "dpkg"}},
		{name: "zero size", source: types.RuntimeSource{URL: valid.URL, SHA256: valid.SHA256, Installer: "dpkg"}},
		{name: "path name", source: types.RuntimeSource{Name: "../demo.deb", URL: valid.URL, SHA256: valid.SHA256, Size: 42, Installer: "dpkg"}},
		{name: "installer", source: types.RuntimeSource{URL: valid.URL, SHA256: valid.SHA256, Size: 42, Installer: "extract"}},
		{name: "architecture", source: types.RuntimeSource{URL: valid.URL, SHA256: valid.SHA256, Size: 42, Installer: "dpkg", Architecture: "riscv64"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRuntimeSource(test.source)
			if test.valid && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("expected validation to fail")
			}
		})
	}
}

func TestRuntimeSourcesForArchitecture(t *testing.T) {
	sources := []types.RuntimeSource{
		{Name: "common"},
		{Name: "amd64", Architecture: "amd64"},
		{Name: "arm64", Architecture: "arm64"},
	}
	selected := RuntimeSourcesForArchitecture(sources, "amd64")
	if len(selected) != 2 || selected[0].Name != "common" || selected[1].Name != "amd64" {
		t.Fatalf("selected runtime sources: %#v", selected)
	}
}

func TestRuntimeFetcherVerifiesAndCaches(t *testing.T) {
	payload := []byte("runtime payload")
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	progress := make([]string, 0, 3)
	fetcher := &RuntimeFetcher{
		CacheDir: t.TempDir(),
		Client:   server.Client(),
		Progress: func(message string) {
			progress = append(progress, message)
		},
	}
	source := types.RuntimeSource{URL: server.URL + "/demo.deb", SHA256: runtimeChecksum(payload), Size: int64(len(payload)), Installer: "dpkg"}
	first, err := fetcher.Fetch(source)
	if err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}
	second, err := fetcher.Fetch(source)
	if err != nil {
		t.Fatalf("cached fetch failed: %v", err)
	}
	if first != second || requests != 1 {
		t.Fatalf("cache was not reused: first=%q second=%q requests=%d", first, second, requests)
	}
	if _, err = os.Stat(filepath.Join(fetcher.CacheDir, source.SHA256)); err != nil {
		t.Fatalf("verified artifact missing: %v", err)
	}
	wantProgress := []string{
		"Downloading runtime source demo.deb (15 bytes)",
		"Verified runtime source demo.deb",
		"Using cached runtime source demo.deb",
	}
	if strings.Join(progress, "\n") != strings.Join(wantProgress, "\n") {
		t.Fatalf("progress = %q, want %q", progress, wantProgress)
	}
}

func TestRuntimeFetcherRejectsSizeAndChecksumMismatch(t *testing.T) {
	payload := []byte("runtime payload")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	tests := []struct {
		name   string
		source types.RuntimeSource
		want   error
	}{
		{
			name:   "size",
			source: types.RuntimeSource{URL: server.URL + "/demo.deb", SHA256: runtimeChecksum(payload), Size: int64(len(payload) - 1), Installer: "dpkg"},
			want:   ErrRuntimeSourceSize,
		},
		{
			name:   "checksum",
			source: types.RuntimeSource{URL: server.URL + "/demo.deb", SHA256: strings.Repeat("a", 64), Size: int64(len(payload)), Installer: "dpkg"},
			want:   ErrRuntimeSourceChecksum,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &RuntimeFetcher{CacheDir: t.TempDir(), Client: server.Client()}
			_, err := fetcher.Fetch(test.source)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			entries, readErr := os.ReadDir(fetcher.CacheDir)
			if readErr != nil {
				t.Fatalf("read cache: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("unverified download remained in cache: %v", entries)
			}
		})
	}
}

func TestRuntimeFetcherRejectsHTTPRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("payload"))
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer server.Close()

	fetcher := RuntimeFetcher{CacheDir: t.TempDir(), Client: server.Client()}
	source := types.RuntimeSource{
		URL:       server.URL + "/demo.deb",
		SHA256:    runtimeChecksum([]byte("payload")),
		Size:      int64(len("payload")),
		Installer: "dpkg",
	}
	if _, err := fetcher.Fetch(source); !errors.Is(err, ErrRuntimeSourceInsecure) {
		t.Fatalf("expected insecure redirect error, got %v", err)
	}
}
