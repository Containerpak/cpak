/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The fallback used when mount_setattr is missing or refused restricts the
// mount it is handed. Every caller here hands it a recursive bind, so what it
// has to reach is the whole tree.
func TestMountTreeTargetsWalksTheWholeBind(t *testing.T) {
	table := strings.Join([]string{
		"21 20 0:20 / /run/host rw,relatime shared:2 - tmpfs tmpfs rw",
		"22 21 0:21 / /run/host/home rw,relatime shared:3 - ext4 /dev/sda2 rw",
		"23 22 0:22 / /run/host/home/user/media rw,relatime shared:4 - ext4 /dev/sdb1 rw",
		"24 21 0:23 / /run/host/media/My\\040Disk rw,relatime shared:5 - vfat /dev/sdc1 rw",
		"25 20 0:24 / /run/hostile rw,relatime shared:6 - tmpfs tmpfs rw",
		"26 20 0:25 / /home rw,relatime shared:7 - ext4 /dev/sda2 rw",
		"short line",
	}, "\n")

	got, err := mountTreeTargetsFrom(strings.NewReader(table), "/run/host")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/run/host",
		"/run/host/home",
		"/run/host/home/user/media",
		"/run/host/media/My Disk",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mount tree targets: got %v, want %v", got, want)
	}
}

// A path that is not a mount point at all still has to reach the remount, which
// is what refuses it. Answering with an empty list would turn the refusal into
// a restriction nobody applied.
func TestMountTreeTargetsAlwaysLeadsWithTheDestination(t *testing.T) {
	got, err := mountTreeTargetsFrom(strings.NewReader(""), "/run/host/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"/run/host"}) {
		t.Fatalf("mount tree targets: got %v, want [/run/host]", got)
	}
}

const mountTreeChildVariable = "CPAK_MOUNT_TREE_CHILD"

// The tree walk is the part that has to hold against a real kernel, so this
// runs it against real mounts in a namespace of its own: a tmpfs carrying a
// second tmpfs, bound recursively somewhere else, restricted through the
// fallback the way a pre-5.12 kernel would take it.
func TestRestrictMountTreeReachesASubmountOfARecursiveBind(t *testing.T) {
	if os.Getenv(mountTreeChildVariable) == "1" {
		restrictMountTreeInNamespace(t)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=TestRestrictMountTreeReachesASubmountOfARecursiveBind", "-test.v")
	command.Env = append(os.Environ(), mountTreeChildVariable+"=1")
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("this host does not hand out unprivileged user namespaces")
		}
		t.Fatalf("restricted mount tree in a namespace: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "--- SKIP") {
		t.Skipf("the namespace could not be prepared:\n%s", output)
	}
}

func restrictMountTreeInNamespace(t *testing.T) {
	t.Helper()
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		t.Skipf("the namespace does not own its mounts: %v", err)
	}
	root, err := os.MkdirTemp("", "cpak-mount-tree")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	defer func() {
		for _, path := range []string{destination, filepath.Join(source, "submount"), source} {
			_ = syscall.Unmount(path, syscall.MNT_DETACH)
		}
		_ = os.RemoveAll(root)
	}()
	for _, path := range []string{source, destination} {
		if err = os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err = MountTmpfsPrepared(source); err != nil {
		t.Skipf("this namespace cannot mount a tmpfs: %v", err)
	}
	submount := filepath.Join(source, "submount")
	if err = os.MkdirAll(submount, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = MountTmpfsPrepared(submount); err != nil {
		t.Fatal(err)
	}
	if err = MountBindPrepared(source, destination); err != nil {
		t.Fatal(err)
	}

	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV)
	// What the old fallback did, and what it left behind: the top of the bind
	// is read-only and the mount under it is not.
	if err = syscall.Mount("", destination, "", flags, ""); err != nil {
		t.Fatal(err)
	}
	if writable(t, filepath.Join(destination, "top")) {
		t.Fatal("the remount left the top of the bind writable")
	}
	if !writable(t, filepath.Join(destination, "submount", "file")) {
		t.Fatal("the submount was already read-only, so this host cannot show the hole")
	}

	if err = restrictMountTree(destination, flags, "read-only"); err != nil {
		t.Fatal(err)
	}
	if writable(t, filepath.Join(destination, "submount", "file")) {
		t.Fatal("the submount of a recursive bind kept the writes the caller asked to take away")
	}

	// A destination that is not a mount point is still refused, which is what
	// the single remount did before anything walked the tree.
	if err = restrictMountTree(filepath.Join(root, "plain"), flags, "read-only"); err == nil {
		t.Fatal("a path that is not a mount point was reported as restricted")
	}
	measureMountSetattrRefusals(t, root)
}

// measureMountSetattrRefusals is where the reading of EINVAL comes from. It is
// measured rather than assumed, on this kernel, at every run: mount_setattr
// takes the call on a mount and answers EINVAL on a path that is not one, so
// EINVAL is cpak asking wrongly and never a kernel that has no mount_setattr.
// Whoever changes that reading has to change this test first.
func measureMountSetattrRefusals(t *testing.T, root string) {
	t.Helper()
	mount := filepath.Join(root, "measured")
	plain := filepath.Join(root, "measured-plain")
	for _, path := range []string{mount, plain} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := MountTmpfsPrepared(mount); err != nil {
		t.Skipf("this namespace cannot mount a tmpfs: %v", err)
	}
	defer func() { _ = syscall.Unmount(mount, syscall.MNT_DETACH) }()

	attributes := &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}
	if err := unix.MountSetattr(unix.AT_FDCWD, mount, unix.AT_RECURSIVE, attributes); err != nil {
		t.Fatalf("this kernel refused mount_setattr on a mount point: %v", err)
	}
	if err := unix.MountSetattr(unix.AT_FDCWD, plain, unix.AT_RECURSIVE, attributes); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("mount_setattr on a plain directory: got %v, want EINVAL", err)
	}

	// So a refusal that is EINVAL is reported, and the weaker mechanism is not
	// quietly put in its place. Before this, the same call fell through to the
	// remount, which answers EINVAL for that same directory: nothing was
	// recovered, and an attribute an older kernel does not know would have been
	// dropped with no error at all.
	err := restrictBindMount(plain, false)
	if err == nil {
		t.Fatal("a refused mount_setattr was reported as a restriction")
	}
	if !strings.Contains(err.Error(), "restrict bind mount") {
		t.Fatalf("EINVAL was answered by the fallback instead of being reported: %v", err)
	}
}

func writable(t *testing.T, path string) bool {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, syscall.EROFS) {
			return false
		}
		t.Fatalf("write %s: %v", path, err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return true
}

// The fallback exists for a kernel that cannot take the call. EINVAL is not
// that: mount_setattr answers it for a path that is not the root of a mount and
// for attributes it will not take, and both are cpak asking wrongly. Reading it
// as a missing feature is how the day cpak asks for an attribute MS_REMOUNT
// cannot express ends in a restriction nobody applied and nobody was told about.
func TestOnlyAKernelThatCannotAnswerReachesTheFallback(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "the kernel has no mount_setattr", err: unix.ENOSYS, want: true},
		{name: "the filesystem will not take it", err: unix.EOPNOTSUPP, want: true},
		{name: "the path or the arguments were wrong", err: unix.EINVAL},
		{name: "not permitted here", err: unix.EPERM},
		{name: "wrapped on its way up", err: fmt.Errorf("restrict: %w", unix.ENOSYS), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mountSetattrUnsupported(test.err); got != test.want {
				t.Fatalf("takes the fallback: got %t, want %t", got, test.want)
			}
		})
	}
}
