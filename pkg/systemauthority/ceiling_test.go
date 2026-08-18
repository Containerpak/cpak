/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func testCeilingStore(t *testing.T) CeilingStore {
	t.Helper()
	return CeilingStore{Directory: t.TempDir(), OwnerUID: uint32(os.Getuid())}
}

// Every installation in the world has no ceiling, so this is the case that must
// never refuse anything and never fail.
func TestAHostWithNoCeilingDecidesNothing(t *testing.T) {
	ceiling, err := testCeilingStore(t).Load()
	if err != nil {
		t.Fatalf("a host with no ceiling answered with an error: %v", err)
	}
	if ceiling.Present {
		t.Fatal("a host with no ceiling reported one")
	}
}

func TestTheCeilingSurvivesBeingWrittenAndRead(t *testing.T) {
	store := testCeilingStore(t)
	policy := types.NewOverride()
	policy.Network = false
	policy.SocketWayland = true
	policy.Filesystem = []types.FilesystemPermission{{Path: "xdg-download", Access: "read-only"}}
	if err := store.Store(policy); err != nil {
		t.Fatal(err)
	}
	ceiling, err := store.Load()
	if err != nil || !ceiling.Present {
		t.Fatalf("the ceiling was written and not read back: present=%v err=%v", ceiling.Present, err)
	}
	if ceiling.Policy.Network || !ceiling.Policy.SocketWayland {
		t.Fatalf("the ceiling came back saying something else: %+v", ceiling.Policy)
	}
	if len(ceiling.Policy.Filesystem) != 1 || ceiling.Policy.Filesystem[0].Access != "read-only" {
		t.Fatalf("the filesystem the ceiling allows changed: %+v", ceiling.Policy.Filesystem)
	}
}

// A ceiling decides what every application on the host may do, so a file
// anybody can rewrite must not be allowed to decide it. It reads as no ceiling
// rather than as an error, because a host that cannot vouch for the file is a
// host nobody configured, and refusing every launch over it would be worse than
// the control it was meant to add.
func TestACeilingNobodyCanVouchForDecidesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case rests on")
	}
	store := testCeilingStore(t)
	if err := store.Store(types.NewOverride()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(store.Directory, ceilingFileName), 0666); err != nil {
		t.Fatal(err)
	}
	ceiling, err := store.Load()
	if err != nil {
		t.Fatalf("a world writable ceiling answered with an error: %v", err)
	}
	if ceiling.Present {
		t.Fatal("a ceiling anyone could rewrite was allowed to decide what applications may do")
	}
}

func TestClearingTheCeilingReturnsTheHostToUnmanaged(t *testing.T) {
	store := testCeilingStore(t)
	if err := store.Store(types.NewOverride()); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	ceiling, err := store.Load()
	if err != nil || ceiling.Present {
		t.Fatalf("the ceiling outlived its removal: present=%v err=%v", ceiling.Present, err)
	}
	// Removing one that is not there is what an administrator does twice by
	// accident, and it must not be an error.
	if err := store.Clear(); err != nil {
		t.Fatalf("removing a ceiling that was already gone failed: %v", err)
	}
}

func TestACeilingThatIsNotAPolicyIsRefused(t *testing.T) {
	store := testCeilingStore(t)
	if err := os.WriteFile(filepath.Join(store.Directory, ceilingFileName), []byte(`{"whatIsThis":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("a file naming something no policy has was read as a ceiling")
	}
}
