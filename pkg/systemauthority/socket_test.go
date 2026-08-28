/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"golang.org/x/sys/unix"
)

func startAuthoritySocket(t *testing.T, service socketService) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authority.sock")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- service.serve(ctx, path) }()
	t.Cleanup(func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("the authority socket stopped with %v", err)
		}
	})
	for attempt := 0; attempt < 200; attempt++ {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the authority socket never started")
	return ""
}

func TestAuthoritySocketRegistersWithoutABus(t *testing.T) {
	registry := testRegistry(t)
	path := startAuthoritySocket(t, socketService{
		Registry:  registry,
		Authorize: func(*unix.Ucred) error { return nil },
	})
	session := testSession()
	err := requestOverSocket(path, socketRequest{
		Action:      "register",
		ID:          session.ID,
		Origin:      session.Origin,
		Name:        session.Name,
		Description: session.Description,
		Kind:        session.Kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(session.ID)
	if err != nil {
		t.Fatalf("the session was not registered: %v", err)
	}
	if loaded != session {
		t.Fatalf("got %+v, want %+v", loaded, session)
	}
	if err := requestOverSocket(path, socketRequest{Action: "remove", ID: session.ID, Origin: session.Origin}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Load(session.ID); err == nil {
		t.Fatal("the session survived its removal")
	}
}

func TestAuthoritySocketRefusesAnUnauthorizedPeer(t *testing.T) {
	path := startAuthoritySocket(t, socketService{
		Registry:  testRegistry(t),
		Authorize: func(*unix.Ucred) error { return errors.New("not allowed here") },
	})
	session := testSession()
	err := requestOverSocket(path, socketRequest{
		Action: "register", ID: session.ID, Origin: session.Origin,
		Name: session.Name, Kind: session.Kind,
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed here") {
		t.Fatalf("the refusal did not reach the caller: %v", err)
	}
}

func TestAuthoritySocketRejectsAnUnknownAction(t *testing.T) {
	path := startAuthoritySocket(t, socketService{
		Registry:  testRegistry(t),
		Authorize: func(*unix.Ucred) error { return nil },
	})
	if err := requestOverSocket(path, socketRequest{Action: "purge"}); err == nil {
		t.Fatal("an unknown action was accepted")
	}
}

func TestAuthoritySocketValidatesBeforeTouchingTheRegistry(t *testing.T) {
	registry := testRegistry(t)
	path := startAuthoritySocket(t, socketService{
		Registry:  registry,
		Authorize: func(*unix.Ucred) error { return nil },
	})
	err := requestOverSocket(path, socketRequest{
		Action: "register", ID: "../escape", Origin: testSession().Origin,
		Name: "Escape", Kind: "desktop",
	})
	if err == nil {
		t.Fatal("an invalid session identifier was accepted")
	}
	if entries, readErr := os.ReadDir(registry.RegistryDirectory); readErr == nil && len(entries) != 0 {
		t.Fatalf("the registry was written despite the refusal: %v", entries)
	}
}

func TestMissingAuthoritySocketIsAnUnavailableTransport(t *testing.T) {
	err := requestOverSocket(filepath.Join(t.TempDir(), "absent.sock"), socketRequest{Action: "remove"})
	if !errors.Is(err, errTransportUnavailable) {
		t.Fatalf("got %v, want an unavailable transport so the caller can fall through", err)
	}
}

func TestSocketAuthorizesOnlyOrdinaryChangesForTheCaller(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	caller := &unix.Ucred{Uid: anchor.UID}
	if err := authorizeSocketRequest(caller, socketRequest{Action: anchorEnrolAction, Anchor: &anchor}, ledger); err != nil {
		t.Fatalf("the caller's first enrolment was refused: %v", err)
	}
	if err := ledger.Record(Enrolment{Anchor: anchor}); err != nil {
		t.Fatal(err)
	}
	wider := anchor
	wider.Generation++
	wider.PolicyRoot = strings.Repeat("d4", 32)
	wider.LaunchRoot = integrity.LaunchRoot(wider.PackageRoot, wider.PolicyRoot)
	if err := authorizeSocketRequest(caller, socketRequest{Action: anchorEnrolAction, Anchor: &wider}, ledger); !errors.Is(err, errRootRequired) {
		t.Fatalf("a widening over the socket returned %v, want root", err)
	}
	if err := authorizeSocketRequest(caller, socketRequest{Action: anchorForgetAction, UID: anchor.UID, Origin: anchor.Origin}, ledger); err != nil {
		t.Fatalf("the caller could not forget its own anchor: %v", err)
	}
	if err := authorizeSocketRequest(caller, socketRequest{Action: anchorClearAction, UID: anchor.UID, Origin: anchor.Origin}, ledger); !errors.Is(err, errRootRequired) {
		t.Fatalf("clearing a removal returned %v, want root", err)
	}
	other := anchor
	other.UID++
	if err := authorizeSocketRequest(caller, socketRequest{Action: anchorEnrolAction, Anchor: &other}, ledger); !errors.Is(err, errRootRequired) {
		t.Fatalf("an enrolment for another account returned %v, want root", err)
	}
	if err := authorizeSocketRequest(&unix.Ucred{Uid: 0}, socketRequest{Action: anchorClearAction}, ledger); err != nil {
		t.Fatalf("root was refused: %v", err)
	}
	if err := authorizeSocketRequest(nil, socketRequest{}, ledger); err == nil {
		t.Fatal("a caller without credentials was authorized")
	}
}

func TestSocketReturnsRootRequirementAsAStableResult(t *testing.T) {
	path := startAuthoritySocket(t, socketService{
		Registry:  testRegistry(t),
		Authorize: func(*unix.Ucred) error { return errRootRequired },
	})
	err := requestOverSocket(path, socketRequest{Action: "register"})
	if !errors.Is(err, errRootRequired) {
		t.Fatalf("got %v, want a root requirement the caller can act on", err)
	}
}
