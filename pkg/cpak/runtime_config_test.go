/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRuntimePathKeepsBrokerCommandsAndApplicationDirectories(t *testing.T) {
	for _, path := range []string{"/opt/example/bin:/usr/bin", "", "/opt/example/bin:$PATH:/opt/example/bin"} {
		t.Run(path, func(t *testing.T) {
			cp := Cpak{}
			if err := cp.ConfigureRuntime([]string{"PATH=" + path, "MODE=runtime"}, nil, nil); err != nil {
				t.Fatal(err)
			}
			environment := applyRuntimeEnvironment([]string{"PATH=/image/bin", "MODE=image"}, cp.runtimeEnvironment)
			entries := strings.Split(environmentValue(environment, "PATH"), ":")
			if entries[0] != "/usr/local/bin" || !slicesContain(entries, "/usr/bin") {
				t.Fatalf("runtime PATH lost the broker or standard commands: %v", entries)
			}
			if strings.Contains(path, "/opt/example/bin") && !slicesContain(entries, "/opt/example/bin") {
				t.Fatalf("runtime PATH lost the application directory: %v", entries)
			}
			if strings.Contains(strings.Join(entries, ":"), "$") || slicesContain(entries, "") {
				t.Fatalf("runtime PATH contains an unexpanded or empty entry: %v", entries)
			}
			if environmentValue(environment, "MODE") != "runtime" || slicesContain(entries, "/image/bin") {
				t.Fatalf("runtime values did not replace the image values: %v", environment)
			}
		})
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
