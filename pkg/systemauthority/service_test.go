/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
