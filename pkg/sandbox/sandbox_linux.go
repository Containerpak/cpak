/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package sandbox

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

var ErrUnavailable = errors.New("sandbox feature is unavailable")

type PathGrant struct {
	Path     string
	ReadOnly bool
}

func ApplyLandlock(grants []PathGrant) (int, error) {
	version, err := landlockVersion()
	if err != nil {
		return 0, err
	}

	handled := landlockAccess(version)
	ruleset, err := landlockCreate(&unix.LandlockRulesetAttr{Access_fs: handled}, 0)
	if err != nil {
		return 0, fmt.Errorf("create landlock ruleset: %w", err)
	}
	defer unix.Close(ruleset)

	for _, grant := range normalizeGrants(grants) {
		fd, openErr := unix.Open(grant.Path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return 0, fmt.Errorf("open landlock path %s: %w", grant.Path, openErr)
		}
		access := landlockReadAccess(version)
		if !grant.ReadOnly {
			access = handled
		}
		addErr := landlockAddPathRule(ruleset, access, fd)
		closeErr := unix.Close(fd)
		if addErr != nil {
			return 0, fmt.Errorf("add landlock rule for %s: %w", grant.Path, addErr)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("close landlock path %s: %w", grant.Path, closeErr)
		}
	}

	if err = enableNoNewPrivileges(); err != nil {
		return 0, err
	}
	if err = landlockRestrictSelf(ruleset); err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
			return 0, ErrUnavailable
		}
		return 0, fmt.Errorf("restrict with landlock: %w", err)
	}
	return version, nil
}

func ApplySeccomp(allowUserNamespaces bool) error {
	architecture, supported := auditArchitecture()
	if !supported {
		return ErrUnavailable
	}
	if err := enableNoNewPrivileges(); err != nil {
		return err
	}
	filter := seccompFilter(architecture, allowUserNamespaces)
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
			return ErrUnavailable
		}
		return fmt.Errorf("install seccomp filter: %w", err)
	}
	return nil
}

func enableNoNewPrivileges() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("enable no new privileges: %w", err)
	}
	return nil
}

func normalizeGrants(grants []PathGrant) []PathGrant {
	merged := make(map[string]PathGrant, len(grants))
	for _, grant := range grants {
		path := filepath.Clean(grant.Path)
		if !filepath.IsAbs(path) {
			continue
		}
		current, exists := merged[path]
		if !exists || !grant.ReadOnly {
			merged[path] = PathGrant{Path: path, ReadOnly: grant.ReadOnly}
			continue
		}
		merged[path] = current
	}
	paths := make([]string, 0, len(merged))
	for path := range merged {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]PathGrant, 0, len(paths))
	for _, path := range paths {
		result = append(result, merged[path])
	}
	return result
}

func landlockVersion() (int, error) {
	version, err := landlockCreate(nil, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) {
			return 0, ErrUnavailable
		}
		return 0, fmt.Errorf("query landlock version: %w", err)
	}
	return version, nil
}

func landlockCreate(attributes *unix.LandlockRulesetAttr, flags int) (int, error) {
	var pointer uintptr
	var size uintptr
	if attributes != nil {
		pointer = uintptr(unsafe.Pointer(attributes))
		size = unsafe.Sizeof(*attributes)
	}
	result, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET, pointer, size, uintptr(flags), 0, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}

func landlockAddPathRule(ruleset int, access uint64, parent int) error {
	attributes := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(parent)}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(ruleset),
		unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attributes)),
		0,
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockRestrictSelf(ruleset int) error {
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockAccess(version int) uint64 {
	access := uint64(
		unix.LANDLOCK_ACCESS_FS_EXECUTE |
			unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_READ_FILE |
			unix.LANDLOCK_ACCESS_FS_READ_DIR |
			unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
			unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
			unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
			unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
			unix.LANDLOCK_ACCESS_FS_MAKE_REG |
			unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
			unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_SYM,
	)
	if version >= 2 {
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if version >= 3 {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if version >= 5 {
		access |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return access
}

func landlockReadAccess(version int) uint64 {
	access := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	if version >= 5 {
		access |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return access
}

func seccompFilter(architecture uint32, allowUserNamespaces bool) []unix.SockFilter {
	filter := []unix.SockFilter{
		bpfLoad(4),
		bpfJump(unix.BPF_JEQ, architecture, 1, 0),
		bpfReturn(unix.SECCOMP_RET_KILL_PROCESS),
		bpfLoad(0),
		bpfJump(unix.BPF_JGE, 0x40000000, 0, 1),
		bpfReturn(unix.SECCOMP_RET_KILL_PROCESS),
	}

	for _, number := range []int{
		unix.SYS_PTRACE,
		unix.SYS_SETNS,
		unix.SYS_BPF,
		unix.SYS_PERF_EVENT_OPEN,
		unix.SYS_MOUNT,
		unix.SYS_UMOUNT2,
		unix.SYS_PIVOT_ROOT,
		unix.SYS_OPEN_TREE,
		unix.SYS_MOVE_MOUNT,
		unix.SYS_FSOPEN,
		unix.SYS_FSCONFIG,
		unix.SYS_FSMOUNT,
		unix.SYS_FSPICK,
		unix.SYS_MOUNT_SETATTR,
		unix.SYS_KEXEC_LOAD,
		unix.SYS_REBOOT,
		unix.SYS_INIT_MODULE,
		unix.SYS_FINIT_MODULE,
		unix.SYS_DELETE_MODULE,
		unix.SYS_SWAPON,
		unix.SYS_SWAPOFF,
	} {
		filter = append(filter, bpfDeny(uint32(number), uint32(unix.EPERM))...)
	}

	if !allowUserNamespaces {
		filter = append(filter, bpfDeny(uint32(unix.SYS_UNSHARE), uint32(unix.EPERM))...)
		filter = append(filter, bpfDeny(uint32(unix.SYS_CLONE3), uint32(unix.ENOSYS))...)
		filter = append(filter,
			bpfJump(unix.BPF_JEQ, uint32(unix.SYS_CLONE), 0, 4),
			bpfLoad(16),
			unix.SockFilter{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: uint32(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWCGROUP)},
			bpfJump(unix.BPF_JEQ, 0, 1, 0),
			bpfReturn(uint32(unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM))),
		)
	}
	filter = append(filter, bpfReturn(unix.SECCOMP_RET_ALLOW))
	return filter
}

func bpfLoad(offset uint32) unix.SockFilter {
	return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: offset}
}

func bpfJump(operation uint16, value uint32, trueOffset, falseOffset uint8) unix.SockFilter {
	return unix.SockFilter{Code: unix.BPF_JMP | operation | unix.BPF_K, Jt: trueOffset, Jf: falseOffset, K: value}
}

func bpfReturn(value uint32) unix.SockFilter {
	return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: value}
}

func bpfDeny(number, errno uint32) []unix.SockFilter {
	return []unix.SockFilter{
		bpfJump(unix.BPF_JEQ, number, 0, 1),
		bpfReturn(uint32(unix.SECCOMP_RET_ERRNO) | errno),
	}
}

func auditArchitecture() (uint32, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, true
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, true
	case "386":
		return unix.AUDIT_ARCH_I386, true
	case "riscv64":
		return unix.AUDIT_ARCH_RISCV64, true
	default:
		return 0, false
	}
}
