/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// TestSetupMountPointsMasksCpakState mounts for real, because where cpak's own
// state ends up is decided by the mounts around the mask: the host scope binds
// the whole host at /run/host and an override binds a directory the manifest
// named, and the store of every other application sits under both.
func TestSetupMountPointsMasksCpakState(t *testing.T) {
	if os.Getenv("CPAK_STATE_MASK_TEST") == "1" {
		runCpakStateMaskTest(t)
		return
	}
	command := exec.Command("unshare", "--user", "--map-root-user", "--mount", "--pid", "--fork", os.Args[0], "-test.run=^TestSetupMountPointsMasksCpakState$")
	command.Env = append(os.Environ(), "CPAK_STATE_MASK_TEST=1")
	if output, err := command.CombinedOutput(); err != nil {
		if bytes.Contains(output, []byte("/proc/self/uid_map: Operation not permitted")) {
			t.Skip("user namespaces are unavailable")
		}
		t.Fatalf("cpak state mask subprocess: %v\n%s", err, output)
	}
}

func runCpakStateMaskTest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CPAK_SERVICE_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	// The tree is not where the default layout would put it, which is what
	// CPAK_INSTALLATION_PATH does and what cpak does to itself to install a
	// local package. It is under the home so that both the host scope and the
	// home override reach it.
	store := filepath.Join(home, "relocated", "store")
	token := filepath.Join(store, "containers", "system-broker.token")
	if err := os.MkdirAll(filepath.Dir(token), 0755); err != nil {
		t.Fatal(err)
	}
	// The token is the identity the broker resolves a policy from, so a
	// container that reads another one holds it.
	if err := os.WriteFile(token, []byte("another container"), 0600); err != nil {
		t.Fatal(err)
	}
	// The rootfs is a mount of its own so that detaching it takes the mounts
	// made below it with it, and the temporary directory can be removed.
	rootFs := t.TempDir()
	if err := syscall.Mount("tmpfs", rootFs, "tmpfs", 0, ""); err != nil {
		t.Fatalf("prepare the rootfs: %v", err)
	}
	defer syscall.Unmount(rootFs, syscall.MNT_DETACH)

	c := &SpawnCmd{
		StateDir:  t.TempDir(),
		MaskState: types.CpakStateDirectories(home, store),
	}
	if _, err := c.setupMountPoints(os.Getuid(), rootFs, []string{home}, nil, true); err != nil {
		t.Fatalf("set up the mount points: %v", err)
	}
	for _, path := range []string{
		filepath.Join(rootFs, "run/host", token),
		filepath.Join(rootFs, token),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s is readable inside the container: %v", path, err)
		}
	}
}
