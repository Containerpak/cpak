/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNormalizeGrantsKeepsTheLeastRestrictiveRule(t *testing.T) {
	grants := normalizeGrants([]PathGrant{
		{Path: "/readonly", ReadOnly: true},
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

func TestSeccompBlocksNamespaceSyscalls(t *testing.T) {
	runSandboxHelper(t, "seccomp", "", "")
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

func TestSandboxHelper(t *testing.T) {
	mode := os.Getenv("CPAK_SANDBOX_HELPER")
	if mode == "" {
		return
	}

	switch mode {
	case "seccomp":
		if err := ApplySeccomp(); err != nil {
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
		if err := exec.Command("/bin/true").Run(); err != nil {
			failSandboxHelper("exec true: %v", err)
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
	default:
		os.Exit(1)
	}
}

func failSandboxHelper(format any, values ...any) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(fmt.Sprint(format), values...))
	os.Exit(1)
}

func runSandboxHelper(t *testing.T, mode, allowed, denied string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestSandboxHelper$")
	command.Env = append(os.Environ(), "CPAK_SANDBOX_HELPER="+mode, "CPAK_SANDBOX_ALLOWED="+allowed, "CPAK_SANDBOX_DENIED="+denied)
	output, err := command.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 77 {
		t.Skip("kernel does not provide this sandbox feature")
	}
	if err != nil {
		t.Fatalf("sandbox helper failed: %v\n%s", err, output)
	}
}
