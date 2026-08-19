/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	if err := store.Store([]byte(`{"network":false,"socketWayland":true,"filesystem":[{"path":"xdg-download","access":"read-only"}]}`)); err != nil {
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
	if err := store.Store([]byte(`{"network":false}`)); err != nil {
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
	if err := store.Store([]byte(`{"network":false}`)); err != nil {
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

// The case the whole Named field exists for. A ceiling meets a policy by
// intersection, so a permission the administrator did not write must not be
// answered for: it would be answered with a zero value, and an administrator
// closing the session bus would be emptying the filesystem, the environment and
// every host action for everything on the host.
func TestTheCeilingRemembersWhichPermissionsItNames(t *testing.T) {
	store := testCeilingStore(t)
	if err := store.Store([]byte(`{"socketSessionBus":false}`)); err != nil {
		t.Fatal(err)
	}
	ceiling, err := store.Load()
	if err != nil || !ceiling.Present {
		t.Fatalf("the ceiling was not read back: present=%v err=%v", ceiling.Present, err)
	}
	if !ceiling.Named["socketSessionBus"] {
		t.Fatal("the ceiling forgot the one permission it named")
	}
	for _, unwritten := range []string{"socketSshAgent", "filesystem", "env", "hostActions", "deviceUsb"} {
		if ceiling.Named[unwritten] {
			t.Fatalf("the ceiling claims to decide %s, which its file never mentions", unwritten)
		}
	}
}

// Storing has to keep the file the administrator wrote rather than a
// marshalled struct, because writing every key back would turn a ceiling over
// one permission into a ceiling over all of them.
func TestACeilingIsStoredAsItWasWritten(t *testing.T) {
	store := testCeilingStore(t)
	if err := store.Store([]byte(`{"socketSessionBus":false}`)); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(store.Directory, ceilingFileName))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(written, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("a ceiling naming one permission was stored naming %d: %s", len(keys), written)
	}
}

// An empty ceiling is a real thing an administrator can write, and it is not
// the same as no ceiling: it is present, and it holds nobody to anything.
func TestACeilingThatNamesNothingIsStillACeiling(t *testing.T) {
	store := testCeilingStore(t)
	if err := store.Store([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	ceiling, err := store.Load()
	if err != nil || !ceiling.Present {
		t.Fatalf("an empty ceiling was not read back: present=%v err=%v", ceiling.Present, err)
	}
	if ceiling.Named == nil {
		t.Fatal("an empty ceiling came back as one that decides everything")
	}
	if len(ceiling.Named) != 0 {
		t.Fatalf("an empty ceiling named %v", ceiling.Named)
	}
}

// The file is refused before anybody authenticates, so the check the command
// runs has to be the one the store runs.
func TestAFileThatIsNotAPolicyIsRefusedBeforeItIsStored(t *testing.T) {
	if err := ValidateCeiling([]byte(`{"whatIsThis":true}`)); err == nil {
		t.Fatal("a file naming something no policy has passed validation")
	}
	if err := ValidateCeiling([]byte(`{"socketSessionBus":false}`)); err != nil {
		t.Fatalf("a real ceiling was refused: %v", err)
	}
}
