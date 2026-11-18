/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

func TestDeduplicatePathKeepsStorageMetadataSeparate(t *testing.T) {
	root := t.TempDir()
	cp := Cpak{Ctx: context.Background()}
	cp.Options.DaBaDeeStoreOptions = store.Options{Root: filepath.Join(root, "dabadee"), PreserveMetadata: true}
	locks := []string{
		"fvs/blocks/.objects/lock",
		"fvs/blocks/.objects/transaction.lock",
		"fvs/layers/first/.fvs2/lock",
		"fvs/layers/second/.fvs2/lock",
	}
	before := make([]os.FileInfo, len(locks))
	for i, name := range locks {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		before[i], _ = os.Stat(path)
	}
	for _, name := range []string{"payload-a", "payload-b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("shared content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cp.DeduplicatePath(root); err != nil {
		t.Fatal(err)
	}
	for i, name := range locks {
		after, err := os.Stat(filepath.Join(root, name))
		if err != nil || !os.SameFile(before[i], after) {
			t.Fatalf("dedup replaced lock %s: %v", name, err)
		}
	}
	a, _ := os.Stat(filepath.Join(root, "payload-a"))
	b, _ := os.Stat(filepath.Join(root, "payload-b"))
	if !os.SameFile(a, b) {
		t.Fatal("equal payload files were not deduplicated")
	}
	if _, err := cp.DeduplicatePath(filepath.Join(root, "fvs", "blocks", ".objects")); err == nil {
		t.Fatal("accepted storage metadata as a dedup root")
	}
}
