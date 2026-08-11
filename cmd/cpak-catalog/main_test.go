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
	"testing"
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
