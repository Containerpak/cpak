/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestBuildContainerPath(t *testing.T) {
	cases := []struct {
		name     string
		imageEnv []string
		contains []string
	}{
		{
			name:     "no image path",
			imageEnv: []string{"LANG=C"},
			contains: []string{"/usr/local/bin", "/usr/bin", "/bin"},
		},
		{
			name:     "image path is kept",
			imageEnv: []string{"LANG=C", "PATH=/opt/app/bin:/usr/bin"},
			contains: []string{"/usr/local/bin", "/opt/app/bin", "/usr/bin"},
		},
		{
			name:     "the last image path wins",
			imageEnv: []string{"PATH=/first", "PATH=/second"},
			contains: []string{"/second"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := buildContainerPath(tc.imageEnv)
			entries := strings.Split(path, ":")

			if entries[0] != "/usr/local/bin" {
				t.Fatalf("the path does not start with /usr/local/bin: %q", path)
			}
			for _, want := range tc.contains {
				if !slicesContain(entries, want) {
					t.Fatalf("the path is missing %s: %q", want, path)
				}
			}

			seen := map[string]bool{}
			for _, entry := range entries {
				if entry == "" {
					t.Fatalf("the path has an empty entry: %q", path)
				}
				if seen[entry] {
					t.Fatalf("the path repeats %s: %q", entry, path)
				}
				seen[entry] = true
			}
		})
	}
}

// Nothing expands variables in an argv, a literal $PATH is only a directory
// that does not exist taking the place of the real one.
func TestBuildContainerPathDropsUnexpandedVariables(t *testing.T) {
	path := buildContainerPath([]string{"PATH=/opt/bin:$PATH"})

	if strings.Contains(path, "$") {
		t.Fatalf("the path carries an unexpanded variable: %q", path)
	}
	if !slicesContain(strings.Split(path, ":"), "/opt/bin") {
		t.Fatalf("the path lost the directory of the image: %q", path)
	}
	if !slicesContain(strings.Split(path, ":"), "/usr/bin") {
		t.Fatalf("the path lost the standard directories: %q", path)
	}
}

func TestGetNested(t *testing.T) {
	original := nestedMarkerPath
	t.Cleanup(func() { nestedMarkerPath = original })

	marker := filepath.Join(t.TempDir(), ".cpak")
	nestedMarkerPath = marker

	if _, nested := getNested(); nested {
		t.Fatal("a container was detected without a marker")
	}

	cases := []struct {
		content string
		want    string
	}{
		{"application-id", "application-id"},
		{"application-id\n", "application-id"},
		{"  application-id  \n", "application-id"},
		{"", ""},
	}

	for _, tc := range cases {
		if err := os.WriteFile(marker, []byte(tc.content), 0644); err != nil {
			t.Fatalf("write the marker: %v", err)
		}
		parent, nested := getNested()
		if !nested {
			t.Fatalf("the marker %q was not detected", tc.content)
		}
		if parent != tc.want {
			t.Fatalf("parent: got %q, want %q", parent, tc.want)
		}
	}
}

func TestContainerEnvironmentKeepsImageAndOverrideValues(t *testing.T) {
	app := types.Application{
		Config: `{"config":{"Env":["IMAGE_VALUE=1"]}}`,
		ParsedOverride: types.Override{
			Env: []string{"OVERRIDE_VALUE=1"},
		},
	}
	container := types.Container{
		CpakId:             "container-id",
		HostExecSocketPath: "/tmp/hostexec.sock",
	}

	env, err := containerEnvironment(app, container)
	if err != nil {
		t.Fatalf("container environment: %v", err)
	}

	for _, value := range []string{
		"IMAGE_VALUE=1",
		"OVERRIDE_VALUE=1",
		"CPAK_CONTAINER_ID=container-id",
		"CPAK_HOSTEXEC_SOCKET=/tmp/hostexec.sock",
	} {
		if !slicesContain(env, value) {
			t.Fatalf("missing %q in %v", value, env)
		}
	}
}

func TestGetCpakBinaryUsesTheRunningExecutable(t *testing.T) {
	got, err := getCpakBinary()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, _ = filepath.EvalSymlinks(want)
	if got != want {
		t.Fatalf("cpak binary: got %q, want %q", got, want)
	}
}

func TestContainerPolicyHashChangesWithPermissions(t *testing.T) {
	first := types.NewOverride()
	second := first
	second.Network = false
	firstHash, err := containerPolicyHash(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := containerPolicyHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("different permission sets produced the same policy hash")
	}
}

func TestEffectiveHostCommandsAreExplicitAndDeduplicated(t *testing.T) {
	commands := effectiveHostCommands(types.Override{
		Notification:        true,
		AllowedHostCommands: []string{"xdg-open", "xdg-open"},
	})
	if got := strings.Join(commands, ","); got != "xdg-open,notify-send" {
		t.Fatalf("unexpected host commands: %s", got)
	}
	if commands := effectiveHostCommands(types.Override{}); len(commands) != 0 {
		t.Fatalf("empty policy exposed host commands: %v", commands)
	}
}

func slicesContain(entries []string, want string) bool {
	for _, entry := range entries {
		if entry == want {
			return true
		}
	}
	return false
}
