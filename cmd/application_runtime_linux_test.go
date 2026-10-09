/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

func TestApplicationRuntimeCannotReadParentRoot(t *testing.T) {
	switch os.Getenv("CPAK_RUNTIME_ISOLATION_TEST") {
	case "parent":
		for _, root := range []bool{false, true} {
			command := (&SpawnCmd{AllowRoot: root}).applicationCommand(nil, append(os.Environ(),
				"CPAK_RUNTIME_ISOLATION_TEST=child",
				fmt.Sprintf("CPAK_RUNTIME_PARENT_PID=%d", os.Getpid()),
				fmt.Sprintf("CPAK_RUNTIME_ROOT=%t", root)))
			command.Path = os.Args[0]
			command.Args = []string{os.Args[0], "-test.run=^TestApplicationRuntimeCannotReadParentRoot$"}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("root %t: application runtime: %v\n%s", root, err, output)
			}
		}
		return
	case "child":
		root, err := strconv.ParseBool(os.Getenv("CPAK_RUNTIME_ROOT"))
		if err != nil {
			t.Fatal(err)
		}
		uid := 1000
		if root {
			uid = 0
		}
		if os.Geteuid() != uid {
			t.Fatalf("application UID: got %d, want %d", os.Geteuid(), uid)
		}
		path := "/proc/" + os.Getenv("CPAK_RUNTIME_PARENT_PID") + "/root"
		if _, err = os.ReadDir(path); !os.IsPermission(err) {
			t.Fatalf("parent root access: %v, want permission denied", err)
		}
		return
	}
	command := exec.Command("unshare", "--user", "--map-root-user", os.Args[0],
		"-test.run=^TestApplicationRuntimeCannotReadParentRoot$")
	command.Env = append(os.Environ(), "CPAK_RUNTIME_ISOLATION_TEST=parent")
	if output, err := command.CombinedOutput(); err != nil {
		if bytes.Contains(output, []byte("unshare failed: Operation not permitted")) {
			t.Skip("user namespaces are unavailable")
		}
		t.Fatalf("application namespace fixture: %v\n%s", err, output)
	}
}
