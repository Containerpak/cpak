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

	"github.com/godbus/dbus/v5"
)

type testAuthorizer struct {
	err     error
	action  string
	details map[string]string
}

func (a *testAuthorizer) Authorize(_ dbus.Sender, action string, details map[string]string) error {
	a.action = action
	a.details = details
	return a.err
}

func TestServiceAuthorizesEverySessionMutation(t *testing.T) {
	registry := testRegistry(t)
	authorizer := &testAuthorizer{}
	service := Service{Registry: registry, Authorizer: authorizer}
	session := testSession()
	if err := service.RegisterSession(":1.20", session.ID, session.Origin, session.Name, session.Description, session.Kind); err != nil {
		t.Fatal(err)
	}
	if authorizer.action != ActionRegisterSession || authorizer.details["package-origin"] != session.Origin {
		t.Fatalf("unexpected registration policy request: %s %#v", authorizer.action, authorizer.details)
	}
	if err := service.RemoveSession(":1.20", session.ID, session.Origin); err != nil {
		t.Fatal(err)
	}
	if authorizer.action != ActionRemoveSession {
		t.Fatalf("unexpected removal policy request: %s", authorizer.action)
	}
}

func TestServiceDenialDoesNotMutateTheRegistry(t *testing.T) {
	registry := testRegistry(t)
	service := Service{
		Registry:   registry,
		Authorizer: &testAuthorizer{err: errors.New("denied")},
	}
	session := testSession()
	if err := service.RegisterSession(":1.20", session.ID, session.Origin, session.Name, session.Description, session.Kind); err == nil {
		t.Fatal("authorization denial was ignored")
	}
	if _, err := os.Stat(filepath.Join(registry.RegistryDirectory, session.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("denied registration changed the registry")
	}
}

func TestServiceRejectsInvalidInputBeforeAuthorization(t *testing.T) {
	authorizer := &testAuthorizer{}
	service := Service{Registry: testRegistry(t), Authorizer: authorizer}
	if err := service.RegisterSession(":1.20", "bad/id", "github.com/example/desktop", "Desktop", "", "desktop"); err == nil {
		t.Fatal("invalid request was accepted")
	}
	if authorizer.action != "" {
		t.Fatal("invalid request reached the authorization service")
	}
}

// The anchor is the only record the rules against going backwards are derived
// from, so who may remove one decides whether those rules can be removed. A
// caller naming somebody else's account has to be asked the hard question, and
// which question is asked must come from the bus rather than from the caller.
func TestForgettingAnotherAccountsAnchorIsNotTheEasyQuestion(t *testing.T) {
	const caller = uint32(1000)
	service := Service{
		Anchors:    testAnchorLedger(t),
		CallerUID:  func(dbus.Sender) (uint32, error) { return caller, nil },
		Authorizer: &testAuthorizer{},
	}

	for name, target := range map[string]uint32{
		"another account":   caller + 1,
		"root":              0,
		"one's own account": caller,
	} {
		authorizer := &testAuthorizer{err: errors.New("denied")}
		service.Authorizer = authorizer
		_ = service.ForgetAnchor(":1.20", target, "github.com/containerpak/demo")
		wanted := ActionForgetAnchorOther
		if target == caller {
			wanted = ActionForgetAnchor
		}
		if authorizer.action != wanted {
			t.Fatalf("forgetting the anchor of %s asked for %s instead of %s", name, authorizer.action, wanted)
		}
	}
}

// An authority that cannot find out who is calling must not fall back to the
// question anybody may answer.
func TestAnUnnamedCallerIsAskedTheHardQuestion(t *testing.T) {
	authorizer := &testAuthorizer{err: errors.New("denied")}
	service := Service{
		Anchors:    testAnchorLedger(t),
		CallerUID:  func(dbus.Sender) (uint32, error) { return 0, errors.New("the bus will not say") },
		Authorizer: authorizer,
	}
	_ = service.ForgetAnchor(":1.20", 1000, "github.com/containerpak/demo")
	if authorizer.action != ActionForgetAnchorOther {
		t.Fatalf("a caller the bus could not name was asked for %s", authorizer.action)
	}
}
