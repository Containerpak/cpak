/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
	"github.com/mirkobrombin/cpak/pkg/sandbox"
	"golang.org/x/sys/unix"
)

func TestMountFileGrantInNamespace(t *testing.T) {
	if os.Getenv("CPAK_FILE_GRANT_LANDLOCK_READER") == "1" {
		runFileGrantLandlockReader(t)
		return
	}
	if os.Getenv("CPAK_FILE_GRANT_MOUNT_TEST") == "1" {
		runFileGrantMountTest(t)
		return
	}
	command := exec.Command("unshare", "--user", "--map-root-user", "--mount", os.Args[0], "-test.run=^TestMountFileGrantInNamespace$")
	command.Env = append(os.Environ(), "CPAK_FILE_GRANT_MOUNT_TEST=1")
	if output, err := command.CombinedOutput(); err != nil {
		if bytes.Contains(output, []byte("/proc/self/uid_map: Operation not permitted")) {
			t.Skip("user namespaces are unavailable")
		}
		t.Fatalf("file grant mount subprocess: %v\n%s", err, output)
	}
}

func runFileGrantMountTest(t *testing.T) {
	run := t.TempDir()
	if err := syscall.Mount(run, "/run", "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		t.Fatalf("isolate runtime directory: %v", err)
	}
	defer syscall.Unmount("/run", syscall.MNT_DETACH)
	if err := os.MkdirAll(filegrant.GuestRoot, 0555); err != nil {
		t.Fatalf("create grant root: %v", err)
	}
	source := t.TempDir()
	selected := filepath.Join(source, "setup.exe")
	sibling := filepath.Join(source, "data.bin")
	if err := os.WriteFile(selected, []byte("installer"), 0600); err != nil {
		t.Fatalf("write selected file: %v", err)
	}
	if err := os.WriteFile(sibling, []byte("payload"), 0600); err != nil {
		t.Fatalf("write sibling file: %v", err)
	}
	grantMounts, err := startGrantMountWorker()
	if err != nil {
		t.Fatalf("start grant mount worker: %v", err)
	}
	defer grantMounts.Close()

	exact, err := filegrant.Resolve("github.com/example/app", selected, filegrant.AccessReadOnly, filegrant.LifetimeSession, false)
	if err != nil {
		t.Fatalf("resolve exact grant: %v", err)
	}
	defer mountGrantForTest(t, exact, grantMounts)()
	if got, readErr := os.ReadFile(exact.Target); readErr != nil || string(got) != "installer" {
		t.Fatalf("read exact grant: got %q, err %v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(exact.Target), filepath.Base(sibling))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("exact grant exposed sibling: %v", statErr)
	}

	contextGrant, err := filegrant.Resolve("github.com/example/app", selected, filegrant.AccessReadOnly, filegrant.LifetimeSession, true)
	if err != nil {
		t.Fatalf("resolve context grant: %v", err)
	}
	defer mountGrantForTest(t, contextGrant, grantMounts)()
	if got, readErr := os.ReadFile(contextGrant.Target); readErr != nil || string(got) != "installer" {
		t.Fatalf("read context selection: got %q, err %v", got, readErr)
	}
	contextSibling := filepath.Join(contextGrant.MountTarget, filepath.Base(sibling))
	if got, readErr := os.ReadFile(contextSibling); readErr != nil || string(got) != "payload" {
		t.Fatalf("read context sibling: got %q, err %v", got, readErr)
	}
	if writeErr := os.WriteFile(contextSibling, []byte("changed"), 0600); writeErr == nil {
		t.Fatal("read-only context grant accepted a write")
	}

	saveGrant, err := filegrant.ResolveSave("github.com/example/save", filepath.Join(source, "report.txt"), filegrant.LifetimeSession)
	if err != nil {
		t.Fatalf("resolve save grant: %v", err)
	}
	defer mountGrantForTest(t, saveGrant, grantMounts)()
	if err = os.WriteFile(saveGrant.Target, []byte("report"), 0600); err != nil {
		t.Fatalf("write save grant: %v", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(source, "report.txt")); readErr != nil || string(got) != "report" {
		t.Fatalf("read saved file: got %q, err %v", got, readErr)
	}

	dynamic, err := filegrant.Resolve("github.com/example/dynamic", selected, filegrant.AccessReadOnly, filegrant.LifetimeSession, false)
	if err != nil {
		t.Fatalf("resolve dynamic grant: %v", err)
	}
	ready, release, err := os.Pipe()
	if err != nil {
		t.Fatalf("create reader pipe: %v", err)
	}
	reader := exec.Command(os.Args[0], "-test.run=^TestMountFileGrantInNamespace$")
	reader.Env = append(os.Environ(), "CPAK_FILE_GRANT_LANDLOCK_READER=1", "CPAK_FILE_GRANT_TARGET="+dynamic.Target)
	reader.ExtraFiles = []*os.File{ready}
	var output bytes.Buffer
	reader.Stdout = &output
	reader.Stderr = &output
	if err = reader.Start(); err != nil {
		t.Fatalf("start restricted reader: %v", err)
	}
	_ = ready.Close()
	defer mountGrantForTest(t, dynamic, grantMounts)()
	if _, err = release.Write([]byte{1}); err != nil {
		t.Fatalf("release restricted reader: %v", err)
	}
	_ = release.Close()
	if err = reader.Wait(); err != nil {
		t.Fatalf("restricted reader: %v\n%s", err, output.Bytes())
	}
	if strings.HasPrefix(output.String(), "LANDLOCK_UNAVAILABLE") {
		t.Log("kernel does not provide Landlock in the test namespace")
		return
	}
	if !strings.HasPrefix(output.String(), "installer") {
		t.Fatalf("restricted reader output: %q", output.String())
	}

	hidden := t.TempDir()
	hiddenFile := filepath.Join(hidden, "outside.dat")
	if err = os.WriteFile(hiddenFile, []byte("descriptor"), 0600); err != nil {
		t.Fatalf("write hidden source: %v", err)
	}
	hiddenGrant, err := filegrant.Resolve("github.com/example/hidden", hiddenFile, filegrant.AccessReadOnly, filegrant.LifetimeSession, false)
	if err != nil {
		t.Fatalf("resolve hidden grant: %v", err)
	}
	hiddenContext, err := filegrant.Resolve("github.com/example/hidden-context", hiddenFile, filegrant.AccessReadOnly, filegrant.LifetimeSession, true)
	if err != nil {
		t.Fatalf("resolve hidden context grant: %v", err)
	}
	hiddenFD, err := unix.Open(hiddenFile, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open hidden source: %v", err)
	}
	hiddenSource := os.NewFile(uintptr(hiddenFD), hiddenFile)
	defer hiddenSource.Close()
	hiddenDirectoryFD, err := unix.Open(hidden, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatalf("open hidden source directory: %v", err)
	}
	hiddenDirectory := os.NewFile(uintptr(hiddenDirectoryFD), hidden)
	defer hiddenDirectory.Close()
	if err = syscall.Mount("tmpfs", hidden, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0700"); err != nil {
		t.Fatalf("hide source path: %v", err)
	}
	defer syscall.Unmount(hidden, syscall.MNT_DETACH)
	if err = mountFileGrant(hiddenGrant, grantproto.Sources{Selected: hiddenSource, Mount: hiddenDirectory}, grantMounts); err != nil {
		t.Fatalf("mount descriptor with hidden source path: %v", err)
	}
	defer syscall.Unmount(hiddenGrant.MountTarget, syscall.MNT_DETACH)
	if got, readErr := os.ReadFile(hiddenGrant.Target); readErr != nil || string(got) != "descriptor" {
		t.Fatalf("read hidden descriptor: got %q, err %v", got, readErr)
	}
	if err = mountFileGrant(hiddenContext, grantproto.Sources{Selected: hiddenDirectory}, grantMounts); err != nil {
		t.Fatalf("mount directory descriptor with hidden source path: %v", err)
	}
	defer syscall.Unmount(hiddenContext.MountTarget, syscall.MNT_DETACH)
	if got, readErr := os.ReadFile(hiddenContext.Target); readErr != nil || string(got) != "descriptor" {
		t.Fatalf("read hidden directory descriptor: got %q, err %v", got, readErr)
	}
}

func runFileGrantLandlockReader(t *testing.T) {
	if _, err := sandbox.ApplyLandlock([]sandbox.PathGrant{{Path: "/", ReadOnly: true}, {Path: filegrant.GuestRoot}}); err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
			_, _ = fmt.Fprint(os.Stdout, "LANDLOCK_UNAVAILABLE")
			return
		}
		t.Fatalf("apply Landlock: %v", err)
	}
	ready := os.NewFile(3, "grant-ready")
	if ready == nil {
		t.Fatal("grant reader pipe is unavailable")
	}
	defer ready.Close()
	if _, err := io.ReadFull(ready, []byte{0}); err != nil {
		t.Fatalf("wait for dynamic grant: %v", err)
	}
	content, err := os.ReadFile(os.Getenv("CPAK_FILE_GRANT_TARGET"))
	if err != nil {
		t.Fatalf("read dynamic grant: %v", err)
	}
	_, _ = fmt.Fprint(os.Stdout, string(content))
}

func mountGrantForTest(t *testing.T, grant filegrant.Grant, grantMounts *grantMountWorker) func() {
	t.Helper()
	fd, err := unix.Open(grant.Source, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open grant source: %v", err)
	}
	source := os.NewFile(uintptr(fd), grant.Source)
	defer source.Close()
	mountSource, err := filegrant.OpenMountSource(grant)
	if err != nil {
		t.Fatalf("open grant mount source: %v", err)
	}
	if mountSource != nil {
		defer mountSource.Close()
	}
	if err = mountFileGrant(grant, grantproto.Sources{Selected: source, Mount: mountSource}, grantMounts); err != nil {
		t.Fatalf("mount file grant: %v", err)
	}
	return func() {
		_ = syscall.Unmount(grant.MountTarget, syscall.MNT_DETACH)
	}
}
