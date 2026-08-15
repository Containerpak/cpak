/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogResolvesIndependentPolicies(t *testing.T) {
	directory := t.TempDir()
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	if err := WritePolicy(directory, first, Policy{AllowNotify: true}); err != nil {
		t.Fatal(err)
	}
	if err := WritePolicy(directory, second, Policy{AllowOpenURI: true}); err != nil {
		t.Fatal(err)
	}
	firstOptions, err := resolveCatalogPolicy("/tmp/broker.sock", directory, Request{Token: first})
	if err != nil {
		t.Fatal(err)
	}
	secondOptions, err := resolveCatalogPolicy("/tmp/broker.sock", directory, Request{Token: second})
	if err != nil {
		t.Fatal(err)
	}
	if !firstOptions.AllowNotify || firstOptions.AllowOpenURI {
		t.Fatalf("first policy leaked permissions: %+v", firstOptions)
	}
	if !secondOptions.AllowOpenURI || secondOptions.AllowNotify {
		t.Fatalf("second policy leaked permissions: %+v", secondOptions)
	}
	if _, err = resolveCatalogPolicy("/tmp/broker.sock", directory, Request{Token: strings.Repeat("c", 64)}); err == nil {
		t.Fatal("unknown token resolved a policy")
	}
}

func TestCatalogReadsPublishedPolicyUpdates(t *testing.T) {
	directory := t.TempDir()
	token := strings.Repeat("d", 64)
	if err := WritePolicy(directory, token, Policy{AllowNotify: true}); err != nil {
		t.Fatal(err)
	}
	if err := WritePolicy(directory, token, Policy{AllowOpenURI: true}); err != nil {
		t.Fatal(err)
	}
	options, err := resolveCatalogPolicy("/tmp/broker.sock", directory, Request{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if !options.AllowOpenURI || options.AllowNotify {
		t.Fatalf("catalog returned a stale policy: %+v", options)
	}
}

func TestCatalogPreservesFilePickerApplication(t *testing.T) {
	directory := t.TempDir()
	token := strings.Repeat("g", 64)
	policy := Policy{FilePickerApplication: "Google Chrome"}
	if err := WritePolicy(directory, token, policy); err != nil {
		t.Fatal(err)
	}
	options, err := resolveCatalogPolicy("/tmp/broker.sock", directory, Request{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if options.FilePickerApplication != policy.FilePickerApplication {
		t.Fatalf("application: %q", options.FilePickerApplication)
	}
}

func TestCatalogRejectsSymlinkedPolicy(t *testing.T) {
	directory := t.TempDir()
	token := strings.Repeat("e", 64)
	path, err := PolicyPath(directory, token)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "policy.json")
	if err = os.WriteFile(target, []byte(`{"allow_notify":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveCatalogPolicy("/tmp/broker.sock", directory, Request{Token: token}); err == nil {
		t.Fatal("symlinked policy was accepted")
	}
}

func TestWritePolicyRejectsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "policies")
	if err := os.Symlink(t.TempDir(), directory); err != nil {
		t.Fatal(err)
	}
	if err := WritePolicy(directory, strings.Repeat("f", 64), Policy{AllowNotify: true}); err == nil {
		t.Fatal("symlinked policy directory was accepted")
	}
}
