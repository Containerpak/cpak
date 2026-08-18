/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The helpers here exist so that the object a caller verified is the object
// the caller then uses. A name can be repointed between the check and the
// mount, an open descriptor cannot, so a path is resolved once by the kernel
// and everything after that travels as a descriptor.

// ErrResolveUnsupported reports that the restricted resolution this package is
// built on is not available. It is returned instead of a plain open, because a
// plain open follows the symlinks and the absolute paths that openat2 was
// asked to refuse, and a caller that accepted the weaker call would verify one
// object and mount another.
var ErrResolveUnsupported = errors.New("openat2 with a restricted resolution is not available")

// ErrDetachedMountUnsupported reports that a verified descriptor cannot be
// cloned into a detached mount here. A caller must refuse rather than bind by
// path, which reopens by name what was verified by descriptor.
var ErrDetachedMountUnsupported = errors.New("open_tree with OPEN_TREE_CLONE is not available")

// resolveRestrictions refuses every way out of the trusted root: an absolute
// path, a symlink at any component including the last one, and a climb above
// the root. RESOLVE_NO_XDEV is deliberately absent, because a trusted root
// legitimately spans mount points, the layer store being one of them.
const resolveRestrictions = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS

// OpenBeneath opens relative under root and returns its descriptor. root is
// the trust anchor: it is opened by name, so it must be a path the caller
// already trusts, while everything below it is resolved by the kernel in a
// single call that no concurrent rename can step into.
func OpenBeneath(root, relative string, flags int) (int, error) {
	if root == "" || !filepath.IsAbs(root) {
		return -1, fmt.Errorf("trusted root must be an absolute path: %q", root)
	}
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open trusted root %s: %w", root, err)
	}
	defer unix.Close(rootFD)
	return OpenBeneathAt(rootFD, relative, flags)
}

// OpenBeneathAt resolves relative against a directory descriptor the caller
// already trusts, for callers that hold one root open across several lookups.
// An empty relative path names the root itself.
func OpenBeneathAt(rootFD int, relative string, flags int) (int, error) {
	path, err := beneathPath(relative)
	if err != nil {
		return -1, err
	}
	if flags&unix.O_CREAT != 0 || flags&unix.O_TMPFILE == unix.O_TMPFILE {
		return -1, fmt.Errorf("resolution must not create %q", relative)
	}
	how := unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC,
		Resolve: resolveRestrictions,
	}
	fd, err := unix.Openat2(rootFD, path, &how)
	if err == nil {
		return fd, nil
	}
	if unsupportedSyscall(err) {
		return -1, fmt.Errorf("resolve %q: %w", relative, ErrResolveUnsupported)
	}
	return -1, fmt.Errorf("resolve %q beneath the trusted root: %w", relative, err)
}

// CheckResolveSupport proves that this kernel performs the restricted
// resolution before anything depends on it, so a host that cannot enforce the
// guarantee is refused up front and never halfway through a launch.
func CheckResolveSupport() error {
	fd, err := OpenBeneath("/", ".", unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// CloneDescriptorMount clones what fd is open on into a detached mount and
// returns its descriptor, which AttachDescriptorMountPrepared then moves into
// place. The clone is taken from the descriptor and never from a path, so the
// object that was checked is the object that gets attached. recursive carries
// the mounts below fd along with it.
func CloneDescriptorMount(fd int, recursive bool) (int, error) {
	flags := uint(unix.OPEN_TREE_CLONE | unix.OPEN_TREE_CLOEXEC | unix.AT_EMPTY_PATH)
	if recursive {
		flags |= uint(unix.AT_RECURSIVE)
	}
	treeFD, err := unix.OpenTree(fd, "", flags)
	if err == nil {
		return treeFD, nil
	}
	if unsupportedSyscall(err) {
		return -1, fmt.Errorf("clone descriptor mount: %w", ErrDetachedMountUnsupported)
	}
	return -1, fmt.Errorf("clone descriptor mount: %w", err)
}

// DescriptorKind is the type of object a descriptor is open on. It is kept
// apart from the permission bits so a caller can refuse a change of type
// without decoding a raw mode itself.
type DescriptorKind uint8

const (
	DescriptorKindUnknown DescriptorKind = iota
	DescriptorKindDirectory
	DescriptorKindRegular
	DescriptorKindSymlink
	DescriptorKindBlockDevice
	DescriptorKindCharDevice
	DescriptorKindFIFO
	DescriptorKindSocket
)

// String names the kind for a refusal message.
func (k DescriptorKind) String() string {
	switch k {
	case DescriptorKindDirectory:
		return "directory"
	case DescriptorKindRegular:
		return "regular file"
	case DescriptorKindSymlink:
		return "symlink"
	case DescriptorKindBlockDevice:
		return "block device"
	case DescriptorKindCharDevice:
		return "character device"
	case DescriptorKindFIFO:
		return "fifo"
	case DescriptorKindSocket:
		return "socket"
	default:
		return "unknown object"
	}
}

// DescriptorIdentity names the object behind a descriptor. A caller records it
// when it verifies something and compares it again before using it, so an
// object replaced under an unchanged name is caught.
type DescriptorIdentity struct {
	Device uint64
	Inode  uint64
	Mode   uint32
	Kind   DescriptorKind
}

// IdentifyDescriptor reports what fd is open on. It answers for an O_PATH
// descriptor too, so a caller can identify an object it never opened for
// reading.
func IdentifyDescriptor(fd int) (DescriptorIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return DescriptorIdentity{}, fmt.Errorf("stat descriptor: %w", err)
	}
	mode := uint32(stat.Mode)
	return DescriptorIdentity{
		Device: uint64(stat.Dev),
		Inode:  uint64(stat.Ino),
		Mode:   mode & 0o7777,
		Kind:   descriptorKind(mode),
	}, nil
}

// Same reports whether two identities are the same object in the same shape.
// The permission bits are part of the answer on purpose: a caller comparing
// only the inode would accept a file that gained the set-user-ID bit between
// the check and the use.
func (d DescriptorIdentity) Same(other DescriptorIdentity) bool {
	return d == other
}

// String renders an identity for a refusal message.
func (d DescriptorIdentity) String() string {
	return fmt.Sprintf("%s %d:%d mode %04o", d.Kind, d.Device, d.Inode, d.Mode)
}

func beneathPath(relative string) (string, error) {
	if relative == "" || relative == "." {
		return ".", nil
	}
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must be relative to the trusted root: %q", relative)
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".." {
			return "", fmt.Errorf("path leaves the trusted root: %q", relative)
		}
	}
	return relative, nil
}

// A kernel that lacks the call answers ENOSYS, a seccomp filter that does not
// know it answers EPERM, and neither can be told apart from here. Both mean
// the guarantee is missing, which is why they are reported as such instead of
// being retried with a weaker call.
func unsupportedSyscall(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EOPNOTSUPP)
}

func descriptorKind(mode uint32) DescriptorKind {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return DescriptorKindDirectory
	case unix.S_IFREG:
		return DescriptorKindRegular
	case unix.S_IFLNK:
		return DescriptorKindSymlink
	case unix.S_IFBLK:
		return DescriptorKindBlockDevice
	case unix.S_IFCHR:
		return DescriptorKindCharDevice
	case unix.S_IFIFO:
		return DescriptorKindFIFO
	case unix.S_IFSOCK:
		return DescriptorKindSocket
	default:
		return DescriptorKindUnknown
	}
}
