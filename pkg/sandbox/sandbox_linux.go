/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
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
	Path       string
	ReadOnly   bool
	WriteFiles bool
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
		var stat unix.Stat_t
		if statErr := unix.Fstat(fd, &stat); statErr != nil {
			unix.Close(fd)
			return 0, fmt.Errorf("stat landlock path %s: %w", grant.Path, statErr)
		}
		access := landlockGrantAccess(version, grant, stat.Mode)
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

func landlockGrantAccess(version int, grant PathGrant, mode uint32) uint64 {
	access := landlockReadAccess(version)
	if !grant.ReadOnly {
		access = landlockAccess(version)
	} else if grant.WriteFiles {
		access |= unix.LANDLOCK_ACCESS_FS_WRITE_FILE
		if version >= 3 {
			access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
		}
	}
	if mode&unix.S_IFMT != unix.S_IFDIR {
		access &= landlockFileAccess(version)
	}
	return access
}

func ApplySeccomp(allowUserNamespaces, allowPtrace bool) error {
	profiles, supported := seccompProfiles()
	if !supported {
		return ErrUnavailable
	}
	if err := enableNoNewPrivileges(); err != nil {
		return err
	}
	filter := seccompFilter(profiles, allowUserNamespaces, allowPtrace)
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
		if !exists || pathGrantAccess(grant) > pathGrantAccess(current) {
			grant.Path = path
			merged[path] = grant
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

func pathGrantAccess(grant PathGrant) int {
	if !grant.ReadOnly {
		return 2
	}
	if grant.WriteFiles {
		return 1
	}
	return 0
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

func landlockFileAccess(version int) uint64 {
	access := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_FILE)
	if version >= 3 {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if version >= 5 {
		access |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return access
}

type seccompProfile struct {
	architecture   uint32
	blocked        []uint32
	namespaceMount []uint32
	unshare        uint32
	clone3         uint32
	clone          uint32
	ptrace         uint32
}

func seccompFilter(profiles []seccompProfile, allowUserNamespaces, allowPtrace bool) []unix.SockFilter {
	filter := []unix.SockFilter{bpfLoad(4)}
	sections := make([][]unix.SockFilter, len(profiles))
	headerLength := 1 + len(profiles) + 1
	nextSection := headerLength
	for index, profile := range profiles {
		sections[index] = seccompProfileFilter(profile, allowUserNamespaces, allowPtrace)
		offset := nextSection - (1 + index) - 1
		filter = append(filter, bpfJump(unix.BPF_JEQ, profile.architecture, uint8(offset), 0))
		nextSection += len(sections[index])
	}
	filter = append(filter, bpfReturn(unix.SECCOMP_RET_KILL_PROCESS))
	for _, section := range sections {
		filter = append(filter, section...)
	}
	return filter
}

func seccompProfileFilter(profile seccompProfile, allowUserNamespaces, allowPtrace bool) []unix.SockFilter {
	filter := []unix.SockFilter{
		bpfLoad(0),
		bpfJump(unix.BPF_JGE, 0x40000000, 0, 1),
		bpfReturn(unix.SECCOMP_RET_KILL_PROCESS),
	}
	for _, number := range profile.blocked {
		if allowPtrace && number == profile.ptrace {
			continue
		}
		filter = append(filter, bpfDeny(number, uint32(unix.EPERM))...)
	}

	if !allowUserNamespaces {
		for _, number := range profile.namespaceMount {
			filter = append(filter, bpfDeny(number, uint32(unix.EPERM))...)
		}
		filter = append(filter, bpfDeny(profile.unshare, uint32(unix.EPERM))...)
		filter = append(filter, bpfDeny(profile.clone3, uint32(unix.ENOSYS))...)
		filter = append(filter,
			bpfJump(unix.BPF_JEQ, profile.clone, 0, 4),
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

func seccompProfiles() ([]seccompProfile, bool) {
	blocked := []uint32{
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_SETNS),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_PERF_EVENT_OPEN),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_REBOOT),
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_FINIT_MODULE),
		uint32(unix.SYS_DELETE_MODULE),
		uint32(unix.SYS_SWAPON),
		uint32(unix.SYS_SWAPOFF),
	}
	namespaceMount := []uint32{
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_OPEN_TREE),
		uint32(unix.SYS_MOVE_MOUNT),
		uint32(unix.SYS_FSOPEN),
		uint32(unix.SYS_FSCONFIG),
		uint32(unix.SYS_FSMOUNT),
		uint32(unix.SYS_FSPICK),
		uint32(unix.SYS_MOUNT_SETATTR),
	}
	native := seccompProfile{
		blocked:        blocked,
		namespaceMount: namespaceMount,
		unshare:        uint32(unix.SYS_UNSHARE),
		clone3:         uint32(unix.SYS_CLONE3),
		clone:          uint32(unix.SYS_CLONE),
		ptrace:         uint32(unix.SYS_PTRACE),
	}
	switch runtime.GOARCH {
	case "amd64":
		native.architecture = unix.AUDIT_ARCH_X86_64
		return []seccompProfile{native, linuxI386SeccompProfile()}, true
	case "arm64":
		native.architecture = unix.AUDIT_ARCH_AARCH64
	case "386":
		native.architecture = unix.AUDIT_ARCH_I386
	case "riscv64":
		native.architecture = unix.AUDIT_ARCH_RISCV64
	default:
		return nil, false
	}
	return []seccompProfile{native}, true
}

func linuxI386SeccompProfile() seccompProfile {
	return seccompProfile{
		architecture: unix.AUDIT_ARCH_I386,
		blocked: []uint32{
			26, 346, 357, 336, 283, 88, 128, 350, 129, 87, 115,
		},
		namespaceMount: []uint32{21, 52, 217, 428, 429, 430, 431, 432, 433, 442},
		unshare:        310,
		clone3:         435,
		clone:          120,
		ptrace:         26,
	}
}
