/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func testEnforcementStore(t *testing.T) EnforcementStore {
	t.Helper()
	return EnforcementStore{
		Directory: filepath.Join(t.TempDir(), "integrity", "v1"),
		OwnerUID:  uint32(os.Getuid()),
	}
}

func TestEnforcementIsOffUntilItIsSet(t *testing.T) {
	store := testEnforcementStore(t)
	level, err := store.Level()
	if err != nil {
		t.Fatalf("a host that never set a level answered %v", err)
	}
	if level != EnforcementOff {
		t.Fatalf("a host that never set a level enforces %s", level)
	}
	if err := store.Set(EnforcementRefuse); err != nil {
		t.Fatal(err)
	}
	// The directory is only created when a level is set, so an untouched host
	// never gains a root owned directory it did not ask for.
	if _, err := os.Stat(filepath.Join(store.Directory, enforcementFileName)); err != nil {
		t.Fatalf("the level was not written: %v", err)
	}
}

func TestEnforcementRoundTripsEveryLevel(t *testing.T) {
	store := testEnforcementStore(t)
	for _, want := range []EnforcementLevel{EnforcementWarn, EnforcementRefuse, EnforcementOff} {
		if err := store.Set(want); err != nil {
			t.Fatal(err)
		}
		level, err := store.Level()
		if err != nil {
			t.Fatal(err)
		}
		if level != want {
			t.Fatalf("the level reads back as %s, want %s", level, want)
		}
	}
}

func TestEnforcementRejectsALevelItDoesNotKnow(t *testing.T) {
	store := testEnforcementStore(t)
	if err := store.Set(EnforcementLevel("strict")); err == nil {
		t.Fatal("a level nobody defines was written")
	}
	if err := SetEnforcement(EnforcementLevel("")); err == nil {
		t.Fatal("an empty level was accepted")
	}
	if err := store.Set(EnforcementRefuse); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Directory, enforcementFileName), []byte("refuse everything"), 0644); err != nil {
		t.Fatal(err)
	}
	level, err := store.Level()
	if err == nil {
		t.Fatal("a level nobody defines was read")
	}
	if level != EnforcementOff {
		t.Fatalf("a level that could not be read answered %s", level)
	}
}

func TestEnforcementRejectsALevelAnybodyCanWrite(t *testing.T) {
	store := testEnforcementStore(t)
	if err := store.Set(EnforcementRefuse); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, enforcementFileName)
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Level(); err == nil {
		t.Fatal("a world writable level was trusted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Directory, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Level(); err == nil {
		t.Fatal("a level in a world writable directory was trusted")
	}
	if err := os.Chmod(store.Directory, 0755); err != nil {
		t.Fatal(err)
	}
	foreign := store
	foreign.OwnerUID = store.OwnerUID + 1
	if _, err := foreign.Level(); err == nil {
		t.Fatal("a level written by another user was trusted")
	}
}

// The level decides whether a refusal happens, so the account a refusal binds
// must have no way to state it. This pins that nothing in the environment is
// read, which is the way a user would otherwise talk their own launch out of a
// refusal.
func TestEnforcementIsNeverTakenFromTheEnvironment(t *testing.T) {
	recorded, err := DefaultEnforcementStore().Level()
	if err != nil {
		t.Skipf("the enforcement level of this host cannot be read here: %v", err)
	}
	for _, name := range []string{"CPAK_ENFORCEMENT", "ENFORCEMENT", "CPAK_INTEGRITY_ENFORCEMENT"} {
		t.Setenv(name, string(EnforcementRefuse))
	}
	if level := Enforcement(); level != recorded {
		t.Fatalf("the environment moved the enforcement level to %s", level)
	}
}

func TestServiceAuthorizesEveryEnforcementChange(t *testing.T) {
	store := testEnforcementStore(t)
	authorizer := &testAuthorizer{}
	service := Service{Enforcement: store, Authorizer: authorizer}
	if dbusErr := service.SetEnforcement(":1.20", string(EnforcementRefuse)); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionSetEnforcement {
		t.Fatalf("changing the enforcement level asked for %s", authorizer.action)
	}
	level, err := store.Level()
	if err != nil || level != EnforcementRefuse {
		t.Fatalf("the authorized level reads back as %s, %v", level, err)
	}
}

func TestServiceDenialDoesNotChangeTheEnforcementLevel(t *testing.T) {
	store := testEnforcementStore(t)
	service := Service{Enforcement: store, Authorizer: &testAuthorizer{err: errors.New("denied")}}
	if dbusErr := service.SetEnforcement(":1.20", string(EnforcementRefuse)); dbusErr == nil {
		t.Fatal("authorization denial was ignored")
	}
	level, err := store.Level()
	if err != nil || level != EnforcementOff {
		t.Fatalf("a denied change left the level at %s, %v", level, err)
	}
}

func TestServiceRejectsAnUnknownLevelBeforeAuthorization(t *testing.T) {
	authorizer := &testAuthorizer{}
	service := Service{Enforcement: testEnforcementStore(t), Authorizer: authorizer}
	if dbusErr := service.SetEnforcement(":1.20", "strict"); dbusErr == nil {
		t.Fatal("a level nobody defines was accepted")
	}
	if authorizer.action != "" {
		t.Fatal("invalid request reached the authorization service")
	}
}

func TestAuthoritySocketSetsTheEnforcementLevel(t *testing.T) {
	store := testEnforcementStore(t)
	path := startAuthoritySocket(t, socketService{
		Anchors:     testAnchorLedger(t),
		Enforcement: store,
		Authorize:   func(*unix.Ucred) error { return nil },
	})
	if err := requestOverSocket(path, socketRequest{Action: enforcementSetAction, Level: string(EnforcementWarn)}); err != nil {
		t.Fatal(err)
	}
	level, err := store.Level()
	if err != nil || level != EnforcementWarn {
		t.Fatalf("the level reads back as %s, %v", level, err)
	}
	if err := requestOverSocket(path, socketRequest{Action: enforcementSetAction, Level: "strict"}); err == nil {
		t.Fatal("a level nobody defines crossed the socket")
	}
}

func TestEnforcementActionAsksTheOwnerOfTheMachine(t *testing.T) {
	defaults := policyDefaults(t)
	want := [3]string{"no", "no", "auth_admin"}
	if defaults[ActionSetEnforcement] != want {
		t.Fatalf("%s is declared as %v, want %v", ActionSetEnforcement, defaults[ActionSetEnforcement], want)
	}
}
