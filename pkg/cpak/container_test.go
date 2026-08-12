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
	"time"

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

func TestContainerScopeLockSerializesTheSameApplication(t *testing.T) {
	cp := Cpak{Options: types.CpakOptions{StorePath: t.TempDir()}}
	firstUnlock, err := cp.lockContainerScope("application")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { firstUnlock() })

	acquired := make(chan func(), 1)
	go func() {
		unlock, lockErr := cp.lockContainerScope("application")
		if lockErr != nil {
			acquired <- nil
			return
		}
		acquired <- unlock
	}()

	select {
	case unlock := <-acquired:
		if unlock != nil {
			unlock()
		}
		t.Fatal("the second lock acquired the same application scope")
	case <-time.After(100 * time.Millisecond):
	}

	firstUnlock()
	firstUnlock = func() {}
	select {
	case unlock := <-acquired:
		if unlock == nil {
			t.Fatal("the second lock failed")
		}
		unlock()
	case <-time.After(time.Second):
		t.Fatal("the second lock did not acquire the released scope")
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
	firstHash, err := containerPolicyHash(first, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := containerPolicyHash(second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("different permission sets produced the same policy hash")
	}
}

func TestEffectiveHostCommandsAreExplicitAndDeduplicated(t *testing.T) {
	commands := effectiveHostCommands(types.Override{
		AllowedHostCommands: []string{"xdg-open", "xdg-open"},
	})
	if got := strings.Join(commands, ","); got != "xdg-open" {
		t.Fatalf("unexpected host commands: %s", got)
	}
	if commands := effectiveHostCommands(types.Override{}); len(commands) != 0 {
		t.Fatalf("empty policy exposed host commands: %v", commands)
	}
}

func TestContainerEnvironmentIncludesSystemBrokerOnlyWhenAvailable(t *testing.T) {
	app := types.Application{Config: `{"config":{}}`}
	container := types.Container{
		CpakId:                 "container-id",
		SystemBrokerSocketPath: "/tmp/system-broker.sock",
	}
	env, err := containerEnvironment(app, container)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"CPAK_SYSTEM_BROKER_SOCKET=" + systemBrokerSocketTarget,
		"CPAK_SYSTEM_BROKER_TOKEN_FILE=" + systemBrokerTokenTarget,
	} {
		if !slicesContain(env, value) {
			t.Fatalf("missing %q in %v", value, env)
		}
	}
}

func TestSystemBrokerRuntimeUsesPrivateDirectory(t *testing.T) {
	runtimeDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	socketPath, tokenPath, err := createSystemBrokerRuntime()
	if err != nil {
		t.Fatal(err)
	}
	container := types.Container{SystemBrokerSocketPath: socketPath, SystemBrokerTokenPath: tokenPath}
	t.Cleanup(func() { cleanupSystemBrokerRuntime(container) })
	if filepath.Dir(socketPath) != filepath.Dir(tokenPath) || filepath.Dir(filepath.Dir(socketPath)) != runtimeDirectory {
		t.Fatalf("system broker paths escaped the runtime directory: %s %s", socketPath, tokenPath)
	}
	info, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("system broker runtime permissions: %o", info.Mode().Perm())
	}
}

func TestSystemBrokerTokenCannotBeReplaced(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	if err := writeSystemBrokerToken(path); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemBrokerToken(path); err == nil {
		t.Fatal("existing system broker token was replaced")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("system broker token permissions: %o", info.Mode().Perm())
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
