/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNormalizeGrantsKeepsTheLeastRestrictiveRule(t *testing.T) {
	grants := normalizeGrants([]PathGrant{
		{Path: "/readonly", ReadOnly: true},
		{Path: "/readonly", ReadOnly: true, WriteFiles: true},
		{Path: "/readonly", ReadOnly: false},
		{Path: "relative", ReadOnly: false},
	})
	if len(grants) != 1 {
		t.Fatalf("grants: got %v", grants)
	}
	if grants[0].ReadOnly {
		t.Fatalf("grant must keep writable permission: %v", grants[0])
	}
}

func TestNormalizeGrantsKeepsExistingFileWrites(t *testing.T) {
	grants := normalizeGrants([]PathGrant{
		{Path: "/proc", ReadOnly: true},
		{Path: "/proc", ReadOnly: true, WriteFiles: true},
	})
	if len(grants) != 1 || !grants[0].ReadOnly || !grants[0].WriteFiles {
		t.Fatalf("grant must keep existing file writes: %v", grants)
	}
}

func TestSeccompBlocksNamespaceSyscalls(t *testing.T) {
	runSandboxHelper(t, "seccomp", "", "")
}

func TestSeccompAllowsNestedUserNamespacesWhenRequested(t *testing.T) {
	if output, err := sandboxHelperOutput("nested-userns", "", ""); err != nil {
		t.Skipf("host policy does not permit nested mounts: %v\n%s", err, output)
	}
	runSandboxHelper(t, "seccomp-userns", "", "")
}

func TestSeccompMountSyscallsFollowNestedUserNamespaceOverride(t *testing.T) {
	profiles, supported := seccompProfiles()
	if !supported {
		t.Skip("unsupported audit architecture")
	}
	for _, profile := range profiles {
		blocked := seccompProfileFilter(profile, false, false)
		allowed := seccompProfileFilter(profile, true, false)
		for _, number := range profile.namespaceMount {
			deny := bpfDeny(number, uint32(unix.EPERM))
			if !containsFilterSequence(blocked, deny) {
				t.Fatalf("namespace mount syscall %d is not blocked", number)
			}
			if containsFilterSequence(allowed, deny) {
				t.Fatalf("namespace mount syscall %d remains blocked", number)
			}
		}
	}
}

func TestSeccompPtraceFollowsProcessNamespaceIsolation(t *testing.T) {
	profiles, supported := seccompProfiles()
	if !supported {
		t.Skip("unsupported audit architecture")
	}
	for _, profile := range profiles {
		deny := bpfDeny(profile.ptrace, uint32(unix.EPERM))
		if !containsFilterSequence(seccompProfileFilter(profile, true, false), deny) {
			t.Fatalf("ptrace syscall %d is not blocked", profile.ptrace)
		}
		if containsFilterSequence(seccompProfileFilter(profile, true, true), deny) {
			t.Fatalf("ptrace syscall %d remains blocked", profile.ptrace)
		}
	}
}

func containsFilterSequence(filter, sequence []unix.SockFilter) bool {
	for index := 0; index+len(sequence) <= len(filter); index++ {
		if slices.Equal(filter[index:index+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

func TestSeccompFiltersEndWithAllow(t *testing.T) {
	profiles, supported := seccompProfiles()
	if !supported {
		t.Skip("unsupported audit architecture")
	}
	for _, allowUserNamespaces := range []bool{false, true} {
		filter := seccompFilter(profiles, allowUserNamespaces, false)
		last := filter[len(filter)-1]
		if last.Code != unix.BPF_RET|unix.BPF_K || last.K != unix.SECCOMP_RET_ALLOW {
			t.Fatalf("allow user namespaces %t: final instruction does not allow", allowUserNamespaces)
		}
	}
}

func TestSeccompAcceptsTheNativeAndCompatibleAuditArchitectures(t *testing.T) {
	profiles, supported := seccompProfiles()
	if !supported {
		t.Skip("unsupported audit architecture")
	}
	switch runtime.GOARCH {
	case "amd64":
		if len(profiles) != 2 {
			t.Fatalf("compatible profiles: %v", profiles)
		}
	default:
		if len(profiles) != 1 {
			t.Fatalf("native profiles: %v", profiles)
		}
	}
}

func TestLinuxI386SeccompProfileBlocksPrivilegedSyscalls(t *testing.T) {
	profile := linuxI386SeccompProfile()
	if profile.architecture != unix.AUDIT_ARCH_I386 || profile.clone != 120 || profile.unshare != 310 || profile.clone3 != 435 {
		t.Fatalf("i386 profile: %+v", profile)
	}
	for _, syscallNumber := range []uint32{26, 88, 128, 283, 286, 287, 288, 336, 346, 357, 374, 425, 426, 427} {
		if !slices.Contains(profile.blocked, syscallNumber) {
			t.Fatalf("i386 privileged syscall %d is not blocked", syscallNumber)
		}
	}
	for _, syscallNumber := range []uint32{21, 52, 217, 428, 442} {
		if !slices.Contains(profile.namespaceMount, syscallNumber) {
			t.Fatalf("i386 namespace mount syscall %d is not blocked", syscallNumber)
		}
	}
}

func TestSeccompBlocksKernelAttackSurface(t *testing.T) {
	profiles, supported := seccompProfiles()
	if !supported {
		t.Skip("unsupported audit architecture")
	}
	for _, syscallNumber := range []uint32{
		uint32(unix.SYS_IO_URING_SETUP),
		uint32(unix.SYS_IO_URING_ENTER),
		uint32(unix.SYS_IO_URING_REGISTER),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_ADD_KEY),
		uint32(unix.SYS_REQUEST_KEY),
		uint32(unix.SYS_KEYCTL),
	} {
		if !slices.Contains(profiles[0].blocked, syscallNumber) {
			t.Fatalf("native syscall %d is not blocked", syscallNumber)
		}
	}
}

func TestLandlockBlocksUnlistedPaths(t *testing.T) {
	directory := t.TempDir()
	allowed := filepath.Join(directory, "allowed")
	denied := filepath.Join(directory, "denied")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	runSandboxHelper(t, "landlock", allowed, denied)
}

func TestLandlockReadOnlyGrant(t *testing.T) {
	directory := t.TempDir()
	readonly := filepath.Join(directory, "readonly")
	if err := os.MkdirAll(readonly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readonly, "existing"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSandboxHelper(t, "readonly", readonly, "")
}

func TestLandlockAllowsWritableChildOfReadOnlyRoot(t *testing.T) {
	directory := t.TempDir()
	writable := filepath.Join(directory, "writable")
	if err := os.MkdirAll(writable, 0o755); err != nil {
		t.Fatal(err)
	}
	runSandboxHelper(t, "readonly-root", writable, "")
}

func TestLandlockDeviceGrantExcludesDirectoryAccess(t *testing.T) {
	access := landlockGrantAccess(7, PathGrant{}, unix.S_IFCHR)
	directoryAccess := landlockAccess(7) &^ landlockFileAccess(7)
	if access&directoryAccess != 0 {
		t.Fatalf("device grant contains directory access: %#x", access)
	}
	if access&unix.LANDLOCK_ACCESS_FS_WRITE_FILE == 0 || access&unix.LANDLOCK_ACCESS_FS_IOCTL_DEV == 0 {
		t.Fatalf("device grant is missing writable device access: %#x", access)
	}
}

func TestLandlockExistingFileGrantExcludesDirectoryMutation(t *testing.T) {
	access := landlockGrantAccess(7, PathGrant{ReadOnly: true, WriteFiles: true}, unix.S_IFDIR)
	if access&unix.LANDLOCK_ACCESS_FS_WRITE_FILE == 0 || access&unix.LANDLOCK_ACCESS_FS_TRUNCATE == 0 {
		t.Fatalf("existing file grant is missing file writes: %#x", access)
	}
	for _, denied := range []uint64{
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR,
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE,
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR,
		unix.LANDLOCK_ACCESS_FS_MAKE_REG,
		unix.LANDLOCK_ACCESS_FS_REFER,
	} {
		if access&denied != 0 {
			t.Fatalf("existing file grant contains directory mutation %#x: %#x", denied, access)
		}
	}
}

func TestLandlockAllowsExistingFileWritesOnly(t *testing.T) {
	directory := t.TempDir()
	existing := filepath.Join(directory, "existing")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSandboxHelper(t, "existing-files", directory, "")
}

func TestLandlockAllowsDeviceGrant(t *testing.T) {
	runSandboxHelper(t, "landlock-device", "/dev/null", "")
}

func TestSandboxHelper(t *testing.T) {
	mode := os.Getenv("CPAK_SANDBOX_HELPER")
	if mode == "" {
		return
	}

	switch mode {
	case "seccomp":
		if err := ApplySeccomp(false, false); err != nil {
			if errors.Is(err, ErrUnavailable) {
				os.Exit(77)
			}
			failSandboxHelper(err)
		}
		mode, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0)
		if err != nil || mode != unix.SECCOMP_MODE_FILTER {
			failSandboxHelper("seccomp mode: %d %v", mode, err)
		}
		if _, _, errno := unix.Syscall(unix.SYS_UNSHARE, uintptr(syscall.CLONE_NEWUSER), 0, 0); errno != unix.EPERM {
			failSandboxHelper("unshare error: %v", errno)
		}
		for _, number := range []uintptr{
			unix.SYS_IO_URING_SETUP,
			unix.SYS_IO_URING_ENTER,
			unix.SYS_IO_URING_REGISTER,
			unix.SYS_USERFAULTFD,
			unix.SYS_ADD_KEY,
			unix.SYS_REQUEST_KEY,
			unix.SYS_KEYCTL,
		} {
			if _, _, errno := unix.Syscall(number, 0, 0, 0); errno != unix.EPERM {
				failSandboxHelper("kernel attack surface syscall %d error: %v", number, errno)
			}
		}
		if err := exec.Command("/bin/true").Run(); err != nil {
			failSandboxHelper("exec true: %v", err)
		}
		os.Exit(0)
	case "seccomp-userns":
		if err := ApplySeccomp(true, false); err != nil {
			if errors.Is(err, ErrUnavailable) {
				os.Exit(77)
			}
			failSandboxHelper(err)
		}
		if output, err := nestedUserNamespaceOutput(); err != nil {
			failSandboxHelper("nested mount remained filtered: %v: %s", err, output)
		}
		os.Exit(0)
	case "nested-userns":
		if output, err := nestedUserNamespaceOutput(); err != nil {
			failSandboxHelper("nested mount denied by host policy: %v: %s", err, output)
		}
		os.Exit(0)
	case "seccomp-userns-child":
		temporary, err := os.MkdirTemp("/tmp", "cpak-seccomp-userns-")
		if err != nil {
			failSandboxHelper(err)
		}
		defer os.RemoveAll(temporary)
		if err = syscall.Mount("tmpfs", temporary, "tmpfs", syscall.MS_NODEV|syscall.MS_NOSUID, ""); err != nil {
			failSandboxHelper("mount tmpfs: %v", err)
		}
		defer syscall.Unmount(temporary, syscall.MNT_DETACH)
		if err = syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_SLAVE, ""); err != nil {
			failSandboxHelper("make / slave: %v", err)
		}
		os.Exit(0)
	case "landlock":
		allowed := os.Getenv("CPAK_SANDBOX_ALLOWED")
		denied := os.Getenv("CPAK_SANDBOX_DENIED")
		if _, err := ApplyLandlock([]PathGrant{{Path: allowed}}); err != nil {
			if errors.Is(err, ErrUnavailable) {
				os.Exit(77)
			}
			failSandboxHelper(err)
		}
		if err := os.WriteFile(filepath.Join(allowed, "allowed"), []byte("ok"), 0o644); err != nil {
			failSandboxHelper("write allowed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(denied, "denied"), []byte("no"), 0o644); !errors.Is(err, unix.EACCES) {
			failSandboxHelper("write denied: %v", err)
		}
		os.Exit(0)
	case "readonly":
		readonly := os.Getenv("CPAK_SANDBOX_ALLOWED")
		if _, err := ApplyLandlock([]PathGrant{{Path: readonly, ReadOnly: true}}); err != nil {
			if errors.Is(err, ErrUnavailable) {
				os.Exit(77)
			}
			failSandboxHelper(err)
		}
		if _, err := os.ReadFile(filepath.Join(readonly, "existing")); err != nil {
			failSandboxHelper("read allowed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(readonly, "new"), []byte("no"), 0o644); !errors.Is(err, unix.EACCES) {
			failSandboxHelper("write readonly: %v", err)
		}
		os.Exit(0)
	case "readonly-root":
		writable := os.Getenv("CPAK_SANDBOX_ALLOWED")
		if _, err := ApplyLandlock([]PathGrant{{Path: "/", ReadOnly: true}, {Path: writable}}); err != nil {
			if errors.Is(err, ErrUnavailable) {
				os.Exit(77)
			}
			failSandboxHelper(err)
		}
		if err := os.WriteFile(filepath.Join(writable, "new"), []byte("ok"), 0o644); err != nil {
			failSandboxHelper("write writable child: %v", err)
		}
		os.Exit(0)
	case "landlock-device":
		device := os.Getenv("CPAK_SANDBOX_ALLOWED")
		if _, err := ApplyLandlock([]PathGrant{{Path: device}}); err != nil {
			if errors.Is(err, ErrUnavailable) {
				os.Exit(77)
			}
			failSandboxHelper(err)
		}
		file, err := os.OpenFile(device, os.O_WRONLY, 0)
		if err != nil {
			failSandboxHelper("open device: %v", err)
		}
		if _, err = file.Write([]byte("cpak")); err != nil {
			failSandboxHelper("write device: %v", err)
		}
		if err = file.Close(); err != nil {
			failSandboxHelper("close device: %v", err)
		}
		os.Exit(0)
	case "existing-files":
		directory := os.Getenv("CPAK_SANDBOX_ALLOWED")
		existing := filepath.Join(directory, "existing")
		if _, err := ApplyLandlock([]PathGrant{{Path: directory, ReadOnly: true, WriteFiles: true}}); err != nil {
			if errors.Is(err, ErrUnavailable) {
				os.Exit(77)
			}
			failSandboxHelper(err)
		}
		if err := os.WriteFile(existing, []byte("new"), 0o644); err != nil {
			failSandboxHelper("write existing file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(directory, "new"), []byte("no"), 0o644); !errors.Is(err, unix.EACCES) {
			failSandboxHelper("create file: %v", err)
		}
		if err := os.Remove(existing); !errors.Is(err, unix.EACCES) {
			failSandboxHelper("remove file: %v", err)
		}
		os.Exit(0)
	default:
		os.Exit(1)
	}
}

func failSandboxHelper(format any, values ...any) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(fmt.Sprint(format), values...))
	os.Exit(1)
}

func nestedUserNamespaceOutput() ([]byte, error) {
	command := exec.Command(os.Args[0], "-test.run=^TestSandboxHelper$")
	command.Env = append(os.Environ(), "CPAK_SANDBOX_HELPER=seccomp-userns-child")
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		Credential:                 &syscall.Credential{Uid: 0, Gid: 0},
	}
	return command.CombinedOutput()
}

func runSandboxHelper(t *testing.T, mode, allowed, denied string) {
	t.Helper()
	output, err := sandboxHelperOutput(mode, allowed, denied)
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 77 {
		t.Skip("kernel does not provide this sandbox feature")
	}
	if err != nil {
		t.Fatalf("sandbox helper failed: %v\n%s", err, output)
	}
}

func sandboxHelperOutput(mode, allowed, denied string) ([]byte, error) {
	command := exec.Command(os.Args[0], "-test.run=^TestSandboxHelper$")
	command.Env = append(os.Environ(), "CPAK_SANDBOX_HELPER="+mode, "CPAK_SANDBOX_ALLOWED="+allowed, "CPAK_SANDBOX_DENIED="+denied)
	return command.CombinedOutput()
}
