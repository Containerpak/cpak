/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

const hostPasswd = `root:x:0:0:root:/root:/bin/bash
sinty:x:1000:1000:sinty user:/home/sinty:/sbin/nologin
mirko:x:1001:1002:Mirko Brombin:/var/home/mirko:/usr/bin/ush
`

const hostGroup = `root:x:0:
wheel:x:10:mirko
sinty:x:1000:
mirko:x:1002:
`

func TestContainerPasswdDescribesTheCaller(t *testing.T) {
	exists := map[string]bool{"/bin/bash": true, "/bin/sh": true}
	got := string(containerPasswd([]byte(hostPasswd), "1001", func(shell string) bool { return exists[shell] }))
	want := `root:x:0:0:root:/root:/bin/bash
mirko:x:1000:1000:Mirko Brombin:/var/home/mirko:/bin/bash
`
	if got != want {
		t.Fatalf("passwd =\n%s", got)
	}
}

func TestContainerPasswdKeepsAShellTheContainerHas(t *testing.T) {
	got := string(containerPasswd([]byte(hostPasswd), "1001", func(string) bool { return true }))
	want := `root:x:0:0:root:/root:/bin/bash
mirko:x:1000:1000:Mirko Brombin:/var/home/mirko:/usr/bin/ush
`
	if got != want {
		t.Fatalf("passwd =\n%s", got)
	}
}

func TestContainerPasswdLeavesAnUnknownCallerAlone(t *testing.T) {
	got := string(containerPasswd([]byte(hostPasswd), "4242", func(string) bool { return true }))
	if got != hostPasswd {
		t.Fatalf("passwd =\n%s", got)
	}
}

func TestContainerGroupDescribesTheCaller(t *testing.T) {
	got := string(containerGroup([]byte(hostGroup), "1002"))
	want := `root:x:0:
wheel:x:10:mirko
mirko:x:1000:
`
	if got != want {
		t.Fatalf("group =\n%s", got)
	}
}

func TestOutsideIdentityReadsTheMappedRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uid_map")
	if err := os.WriteFile(path, []byte("         0       1001          1\n         1     100000      65535\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if id, ok := outsideIdentity(path); !ok || id != "1001" {
		t.Fatalf("outside identity = %q %v", id, ok)
	}
	if _, ok := outsideIdentity(filepath.Join(t.TempDir(), "missing")); ok {
		t.Fatal("a missing map gave an identity")
	}
}
