/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestSelectedReferencePrecedence(t *testing.T) {
	kind, value := selectedReference(storeManifest{Branch: "main", Release: "v1", Commit: "abc"})
	if kind != "branch" || value != "main" {
		t.Fatalf("unexpected reference: %s %s", kind, value)
	}
}

func TestTruncateUsesCharacters(t *testing.T) {
	if value := truncate("caffè", 4); value != "caff" {
		t.Fatalf("unexpected value: %s", value)
	}
}

func TestResolveCommit(t *testing.T) {
	requestPath := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestPath <- request.URL.Path
		_, _ = writer.Write([]byte(`{"sha":"0123456789abcdef0123456789abcdef01234567"}`))
	}))
	defer server.Close()

	commit, err := resolveCommit(context.Background(), server.Client(), server.URL, "github.com/containerpak/cpak", "main")
	if err != nil {
		t.Fatal(err)
	}
	if commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("unexpected commit: %s", commit)
	}
	if path := <-requestPath; path != "/repos/containerpak/cpak/commits/main" {
		t.Fatalf("unexpected path: %s", path)
	}
}

func TestInstallerDigests(t *testing.T) {
	dir := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(dir, "cpak-installer-linux-"+arch), []byte(arch), 0644); err != nil {
			t.Fatal(err)
		}
	}
	digests, err := installerDigests(dir)
	if err != nil {
		t.Fatal(err)
	}
	wanted := sha256.Sum256([]byte("amd64"))
	if digests["amd64"] != hex.EncodeToString(wanted[:]) {
		t.Fatalf("unexpected digest: %s", digests["amd64"])
	}
}

func TestInstallerArchitectures(t *testing.T) {
	architectures, err := installerArchitectures(storeEntry{})
	if err != nil || !reflect.DeepEqual(architectures, []string{"amd64", "arm64"}) {
		t.Fatalf("unexpected default architectures: %#v, %v", architectures, err)
	}
	architectures, err = installerArchitectures(storeEntry{Architectures: []string{"amd64"}})
	if err != nil || !reflect.DeepEqual(architectures, []string{"amd64"}) {
		t.Fatalf("unexpected explicit architectures: %#v, %v", architectures, err)
	}
	if _, err = installerArchitectures(storeEntry{Architectures: []string{"riscv64"}}); err == nil {
		t.Fatal("unsupported store architecture was accepted")
	}
	if _, err = installerArchitectures(storeEntry{Architectures: []string{"amd64", "amd64"}}); err == nil {
		t.Fatal("duplicate store architecture was accepted")
	}
}

func TestLoadPackageManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/containerpak/demo/contents/cpak.json" || request.URL.Query().Get("ref") != "abc123" {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		if request.Header.Get("Accept") != "application/vnd.github.raw+json" {
			t.Fatalf("unexpected Accept header: %s", request.Header.Get("Accept"))
		}
		_, _ = writer.Write([]byte(`{"manifest_version":"2.0","name":"Demo","description":"Demo application","image":"ghcr.io/containerpak/demo:main","binaries":["/usr/bin/demo"],"override":{"network":true}}`))
	}))
	defer server.Close()

	manifest, err := loadPackageManifest(context.Background(), server.Client(), server.URL, "github.com/containerpak/demo", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Override.Network {
		t.Fatal("network permission was not decoded")
	}
}

func TestGitHubContentsURL(t *testing.T) {
	rawURL := "https://raw.githubusercontent.com/Containerpak/store/main/categories/Graphics/icon.svg"
	want := "https://api.github.com/repos/Containerpak/store/contents/categories/Graphics/icon.svg?ref=main"
	if got := githubContentsURL(rawURL); got != want {
		t.Fatalf("unexpected contents URL: %s", got)
	}
	customURL := "https://store.example/icon.svg"
	if got := githubContentsURL(customURL); got != customURL {
		t.Fatalf("custom URL changed: %s", got)
	}
}

func TestBuildCatalogOmitsLoginSessions(t *testing.T) {
	const origin = "github.com/example/desktop"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		base := "http://" + request.Host
		switch request.URL.Path {
		case "/index.json":
			_, _ = writer.Write([]byte(`{"Desktop":{"` + origin + `":{"name":"Desktop","description":"Desktop session","manifest":"` + base + `/desktop.json"}}}`))
		case "/desktop.json":
			_, _ = writer.Write([]byte(`{"branch":"main","description":"Desktop session"}`))
		case "/repos/example/desktop/commits/main":
			_, _ = writer.Write([]byte(`{"sha":"0123456789abcdef0123456789abcdef01234567"}`))
		case "/repos/example/desktop/contents/cpak.json":
			_, _ = writer.Write([]byte(`{"manifest_version":"3.0","name":"Desktop","description":"Desktop session","version":"1.0.0","image":"ghcr.io/example/desktop@sha256:` + strings.Repeat("a", 64) + `","binaries":["/usr/bin/desktop"],"sessions":[{"id":"dev.example.desktop","name":"Desktop","description":"Desktop session","kind":"desktop","entrypoint":"/usr/bin/desktop","override":{}}],"override":{}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := buildCatalog(context.Background(), server.Client(), server.URL+"/index.json", server.URL, "v2.9.1", map[string]string{"amd64": "a", "arm64": "b"}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := result.Packages[origin]; exists {
		t.Fatal("the signed installer catalog included a login session")
	}
}

func TestSignedManifestDigestRequiresPinnedCode(t *testing.T) {
	manifest := &types.CpakManifest{
		ManifestVersion: "2.0",
		Name:            "Demo",
		Description:     "Demo application",
		Image:           "ghcr.io/containerpak/demo:main",
		Binaries:        []string{"/usr/bin/demo"},
	}
	if _, err := signedManifestDigest(manifest); err == nil {
		t.Fatal("a signed installer accepted a mutable image tag")
	}
	manifest.Image = "ghcr.io/containerpak/demo@sha256:" + strings.Repeat("a", 64)
	if _, err := signedManifestDigest(manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Dependencies = []types.Dependency{{Origin: "github.com/containerpak/runtime"}}
	if _, err := signedManifestDigest(manifest); !errors.Is(err, errUnsupportedSignedInstaller) {
		t.Fatal("a signed installer accepted an unbound dependency graph")
	}
}

func TestSignedCatalogOmitsUnsupportedPackageShapes(t *testing.T) {
	manifest := &types.CpakManifest{
		ManifestVersion: "3.0",
		Name:            "Desktop",
		Description:     "Desktop session",
		Image:           "ghcr.io/containerpak/desktop@sha256:" + strings.Repeat("a", 64),
		Binaries:        []string{"/usr/bin/desktop"},
		Sessions:        []types.Session{{ID: "dev.example.desktop"}},
	}
	digest, err := signedCatalogManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "" {
		t.Fatal("the signed installer catalog included a login session")
	}
	manifest.Sessions = nil
	manifest.Image = "ghcr.io/containerpak/desktop:main"
	if _, err = signedCatalogManifestDigest(manifest); err == nil {
		t.Fatal("the signed installer catalog ignored a mutable image")
	}
	manifest.ImageRef = "source"
	digest, err = signedCatalogManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "" {
		t.Fatal("the signed installer catalog included a tracked image tag")
	}
}

func TestFetchRetriesTemporaryFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte("ready"))
	}))
	defer server.Close()

	result, err := fetch(context.Background(), server.Client(), server.URL, 32)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "ready" || attempts != 3 {
		t.Fatalf("unexpected result %q after %d attempts", result, attempts)
	}
}

func TestSummarizePermissions(t *testing.T) {
	override := types.Override{
		SocketX11:        true,
		SocketWayland:    true,
		SocketPulseAudio: true,
		DeviceAll:        true,
		Notification:     true,
		Filesystem: []types.FilesystemPermission{
			{Path: "home", Access: "read-write"},
		},
		Network: true,
	}
	want := []bootstrap.Permission{
		{Name: "Display", Detail: "X11 (no input or screen isolation), Wayland display and compositor-mediated clipboard"},
		{Name: "Audio", Detail: "PulseAudio"},
		{Name: "Devices", Detail: "all devices"},
		{Name: "Notifications", Detail: "desktop notifications"},
		{Name: "Files", Detail: "home, read write; can run code on the host through startup files"},
		{Name: "Network", Detail: "internet and local network"},
	}
	if got := summarizePermissions(override); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected permissions: %#v", got)
	}
}
