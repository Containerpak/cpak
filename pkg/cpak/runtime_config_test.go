/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveRuntimeEnvironmentSupportsURLsAndPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.env")
	if err := os.WriteFile(path, []byte("DATABASE_URL=postgres://db:5432/app\nMODE=file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := resolveRuntimeEnvironment([]string{"MODE=flag"}, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DATABASE_URL=postgres://db:5432/app", "MODE=flag"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("environment: %v", values)
	}
}

func TestRuntimeEnvironmentRejectsInternalVariables(t *testing.T) {
	if _, err := resolveRuntimeEnvironment([]string{"CPAK_CONTAINER_ID=forged"}, nil); err == nil {
		t.Fatal("internal variable was accepted")
	}
}

func TestEmptyRuntimeConfigurationKeepsTheExistingContainerIdentity(t *testing.T) {
	identity, err := (&Cpak{}).runtimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity != "" {
		t.Fatalf("empty runtime identity: %q", identity)
	}
}

func TestRuntimeSecretMustBePrivateAndChangesIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	cp := Cpak{}
	if err := cp.ConfigureRuntime(nil, nil, []string{"TOKEN=" + path}); err != nil {
		t.Fatal(err)
	}
	first, err := cp.runtimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := cp.runtimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("secret content did not change runtime identity")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err = cp.ConfigureRuntime(nil, nil, []string{"TOKEN=" + path}); err == nil {
		t.Fatal("public secret file was accepted")
	}
}
