/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The references that used to walk out of the manifest cache. Each one is a
// value a manifest could carry, and each one used to decide where cpak wrote
// and, through the query separator, what the file was called.
func TestAReferenceThatNavigatesIsRefused(t *testing.T) {
	for _, reference := range []string{
		"../../../../.config/autostart",
		"main/../../../../.config/cpak",
		"..",
		"main?/..",
		"main\nrelease",
		"main\x00",
		"-oProxyCommand=sh",
		"/etc/passwd",
		"main/",
		"main//release",
		"refs.lock",
		"",
	} {
		if err := validateGitReference(reference); err == nil {
			t.Fatalf("%q was accepted as a git reference", reference)
		}
	}
}

// The references a package legitimately names have to keep working, or the
// check is a regression rather than a fix.
func TestTheReferencesPackagesActuallyUseAreKept(t *testing.T) {
	for _, reference := range []string{
		"main",
		"v2.6.5",
		"feature/desktop-entries",
		"release-1.0.x",
		"9f3c1a2b4d5e6f708192a3b4c5d6e7f809a1b2c3",
	} {
		if err := validateGitReference(reference); err != nil {
			t.Fatalf("%q is a reference a package would use, and it was refused: %v", reference, err)
		}
	}
}

func TestAFileNameThatIsAPathIsRefused(t *testing.T) {
	for _, name := range []string{
		"../cpak.json", "a/b", "/cpak.json", "", ".", "..", "evil.desktop\x00",
	} {
		if _, err := singlePathComponent(name); err == nil {
			t.Fatalf("%q was accepted as a file name", name)
		}
	}
	if name, err := singlePathComponent("cpak.json"); err != nil || name != "cpak.json" {
		t.Fatalf("the name cpak actually asks for was refused: %v", err)
	}
}

func TestAJoinThatLeavesItsRootIsRefused(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"../outside", "a/../../outside", "../../etc"} {
		if _, err := containedPath(root, relative); err == nil {
			t.Fatalf("%q was allowed to escape %s", relative, root)
		}
	}
	inside, err := containedPath(root, filepath.Join("branches", "main"))
	if err != nil || !strings.HasPrefix(inside, root) {
		t.Fatalf("a path inside the root was refused: %s %v", inside, err)
	}
}

// The whole path, against a server that answers whatever it likes. This is the
// case the fix exists for: a manifest that names a navigating reference must
// not be able to put a file anywhere on disk.
func TestAHostileReferenceCannotWriteOutsideTheCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[Desktop Entry]\nExec=/bin/sh -c id\n"))
	}))
	defer server.Close()

	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "cache")

	provider := &RepoProvider{
		Origin: strings.TrimPrefix(server.URL, "http://"),
		GitDir: cache,
	}
	// The reference walks up out of the cache and then back down into a
	// directory the user owns, which is the shape that reached ~/.config.
	hostile := "../outside/evil.desktop?/.."
	if _, err := provider.GetFileInBranch("cpak.json", hostile); err == nil {
		t.Fatal("a navigating reference was fetched")
	}

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the fetch wrote outside the cache: %v", names)
	}
}
