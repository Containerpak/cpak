/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestRepoProvider(t *testing.T, origin string) *RepoProvider {
	t.Helper()

	provider, err := NewRepoProvider(origin, t.TempDir())
	if err != nil {
		t.Fatalf("failed to create the repo provider: %v", err)
	}
	return provider
}

func TestGetLatestReleaseFromGithubApi(t *testing.T) {
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v2.0.0","draft":false,"prerelease":false}`))
	}))
	defer server.Close()

	provider := newTestRepoProvider(t, "github.com/user/demo")
	provider.APIBaseURL = server.URL

	release, err := provider.GetLatestRelease()
	if err != nil {
		t.Fatalf("GetLatestRelease returned an error: %v", err)
	}
	if release != "v2.0.0" {
		t.Fatalf("expected v2.0.0, got %q", release)
	}
	if requested != "/repos/user/demo/releases/latest" {
		t.Fatalf("unexpected request path: %s", requested)
	}
}

func TestGetDefaultBranchFromGithubApi(t *testing.T) {
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"default_branch":"master"}`))
	}))
	defer server.Close()

	provider := newTestRepoProvider(t, "github.com/user/demo")
	provider.APIBaseURL = server.URL

	branch, err := provider.GetDefaultBranch()
	if err != nil {
		t.Fatalf("GetDefaultBranch returned an error: %v", err)
	}
	if branch != "master" {
		t.Fatalf("expected master, got %q", branch)
	}
	if requested != "/repos/user/demo" {
		t.Fatalf("unexpected request path: %s", requested)
	}
}

func TestGetDefaultBranchUnsupportedHost(t *testing.T) {
	provider := newTestRepoProvider(t, "example.com/user/demo")

	_, err := provider.GetDefaultBranch()
	if !errors.Is(err, ErrDefaultBranchUnsupported) {
		t.Fatalf("expected ErrDefaultBranchUnsupported, got %v", err)
	}
}

func TestGetLatestReleaseUnsupportedHost(t *testing.T) {
	provider := newTestRepoProvider(t, "example.com/user/demo")

	_, err := provider.GetLatestRelease()
	if !errors.Is(err, ErrLatestReleaseUnsupported) {
		t.Fatalf("expected ErrLatestReleaseUnsupported, got %v", err)
	}
}

func TestGetLatestReleaseRejectsUnstableReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v3.0.0-rc1","draft":false,"prerelease":true}`))
	}))
	defer server.Close()

	provider := newTestRepoProvider(t, "github.com/user/demo")
	provider.APIBaseURL = server.URL

	_, err := provider.GetLatestRelease()
	if err == nil {
		t.Fatalf("expected a prerelease to be rejected")
	}
	if errors.Is(err, ErrLatestReleaseUnsupported) {
		t.Fatalf("a prerelease is not an unsupported host")
	}
}

func TestGetLatestReleaseWithoutReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	provider := newTestRepoProvider(t, "github.com/user/demo")
	provider.APIBaseURL = server.URL

	_, err := provider.GetLatestRelease()
	if err == nil {
		t.Fatalf("expected an error when no release is published")
	}
}

func TestGetFileInBranch(t *testing.T) {
	var cacheBypass string
	var cacheControl string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/demo/raw/main/cpak.json" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		cacheBypass = r.URL.Query().Get("cpak")
		cacheControl = r.Header.Get("Cache-Control")
		w.Write([]byte(`{"name":"demo"}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	provider := newTestRepoProvider(t, host+"/user/demo")
	provider.Scheme = "http"

	content, err := provider.GetFileInBranch("cpak.json", "main")
	if err != nil {
		t.Fatalf("GetFileInBranch returned an error: %v", err)
	}
	if string(content) != `{"name":"demo"}` {
		t.Fatalf("unexpected content: %s", content)
	}
	if cacheBypass == "" || cacheControl != "no-cache" {
		t.Fatalf("branch request did not bypass caches")
	}
}

func TestGetFileInCommitKeepsImmutableReferenceCacheable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("cpak") || r.Header.Get("Cache-Control") != "" {
			http.Error(w, "immutable request bypassed the cache", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"name":"demo"}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	provider := newTestRepoProvider(t, host+"/user/demo")
	provider.Scheme = "http"

	if _, err := provider.GetFileInCommit("cpak.json", "abc123"); err != nil {
		t.Fatalf("GetFileInCommit returned an error: %v", err)
	}
}
