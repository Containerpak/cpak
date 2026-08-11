/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestLoadPackageManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/containerpak/demo/contents/cpak.json" || request.URL.Query().Get("ref") != "abc123" {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		if request.Header.Get("Accept") != "application/vnd.github.raw+json" {
			t.Fatalf("unexpected Accept header: %s", request.Header.Get("Accept"))
		}
		_, _ = writer.Write([]byte(`{"manifest_version":"2.0","override":{"network":true}}`))
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
		{Name: "Display", Detail: "X11, Wayland"},
		{Name: "Audio", Detail: "PulseAudio"},
		{Name: "Devices", Detail: "all devices"},
		{Name: "Notifications", Detail: "desktop notifications"},
		{Name: "Files", Detail: "home, read write"},
		{Name: "Network", Detail: "internet and local network"},
	}
	if got := summarizePermissions(override); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected permissions: %#v", got)
	}
}
