/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package ostree is a read-only client for OSTree repositories in archive
// mode, which is the form every Flatpak remote is published in. It resolves
// refs through a signed summary, walks a commit tree and streams file
// objects, checking every object against the checksum it is addressed by
// before a byte of it is handed on.
package ostree

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
)

const (
	commitType  = "(a{sv}aya(say)sstayay)"
	dirTreeType = "(a(say)a(sayay))"
	dirMetaType = "(uuua(ayay))"

	// archiveHeaderType is the header an archive repository stores in front
	// of the compressed content of a file object. fileHeaderType is the one
	// the object's checksum is taken over, which leaves the size out.
	archiveHeaderType = "(tuuuusa(ayay))"
	fileHeaderType    = "(uuuusa(ayay))"

	summaryType      = "(a(s(taya{sv}))a{sv})"
	summaryIndexType = "(a{s(ayaaya{sv})}a{sv})"
)

const (
	modeTypeMask = 0o170000
	modeRegular  = 0o100000
	modeSymlink  = 0o120000
	modeDir      = 0o040000
)

// Checksum is the SHA-256 an OSTree object is addressed by.
type Checksum [32]byte

func (c Checksum) String() string {
	return hex.EncodeToString(c[:])
}

// ParseChecksum reads the hexadecimal form of a checksum.
func ParseChecksum(value string) (Checksum, error) {
	var checksum Checksum
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(checksum) {
		return checksum, fmt.Errorf("ostree: invalid checksum %q", value)
	}
	copy(checksum[:], decoded)
	return checksum, nil
}

func checksumFrom(value any) (Checksum, error) {
	var checksum Checksum
	raw, ok := value.([]byte)
	if !ok || len(raw) != len(checksum) {
		return checksum, fmt.Errorf("%w: invalid object checksum", gvariant.ErrMalformed)
	}
	copy(checksum[:], raw)
	return checksum, nil
}

func objectPath(checksum Checksum, kind string) string {
	text := checksum.String()
	return "objects/" + text[:2] + "/" + text[2:] + "." + kind
}

func verifyObject(checksum Checksum, data []byte) error {
	if sha256.Sum256(data) != checksum {
		return verificationFailure(fmt.Errorf("ostree: object %s does not match its checksum", checksum))
	}
	return nil
}

// Commit is a decoded commit object.
type Commit struct {
	Checksum  Checksum
	Metadata  map[string]gvariant.Variant
	Parent    *Checksum
	Subject   string
	Body      string
	Timestamp time.Time
	RootTree  Checksum
	RootMeta  Checksum

	// Raw is the serialised commit, which is what its signatures sign.
	Raw []byte
}

// String answers with a metadata string, or nothing when the key is absent or
// carries another type.
func (c Commit) String(key string) string {
	value, ok := c.Metadata[key].Value.(string)
	if !ok {
		return ""
	}
	return value
}

// Strings answers with a metadata string array.
func (c Commit) Strings(key string) []string {
	items, ok := c.Metadata[key].Value.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			values = append(values, text)
		}
	}
	return values
}

func parseCommit(checksum Checksum, data []byte) (Commit, error) {
	if err := verifyObject(checksum, data); err != nil {
		return Commit{}, err
	}
	value, err := gvariant.Parse(commitType, data)
	if err != nil {
		return Commit{}, fmt.Errorf("ostree: decode commit %s: %w", checksum, err)
	}
	fields := value.([]any)
	metadata, err := gvariant.VariantDict(fields[0])
	if err != nil {
		return Commit{}, fmt.Errorf("ostree: decode commit %s metadata: %w", checksum, err)
	}
	commit := Commit{
		Checksum:  checksum,
		Metadata:  metadata,
		Subject:   fields[3].(string),
		Body:      fields[4].(string),
		Timestamp: time.Unix(int64(swap64(fields[5].(uint64))), 0).UTC(),
		Raw:       data,
	}
	if parent := fields[1].([]byte); len(parent) != 0 {
		value, err := checksumFrom(parent)
		if err != nil {
			return Commit{}, err
		}
		commit.Parent = &value
	}
	if commit.RootTree, err = checksumFrom(fields[6]); err != nil {
		return Commit{}, err
	}
	if commit.RootMeta, err = checksumFrom(fields[7]); err != nil {
		return Commit{}, err
	}
	return commit, nil
}

// OSTree writes the integers of its own objects big-endian into a format that
// is otherwise little-endian, so they come out of the decoder swapped.
func swap32(value uint32) uint32 {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], value)
	return binary.BigEndian.Uint32(raw[:])
}

func swap64(value uint64) uint64 {
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], value)
	return binary.BigEndian.Uint64(raw[:])
}

type dirTree struct {
	files []dirFile
	dirs  []dirChild
}

type dirFile struct {
	name     string
	checksum Checksum
}

type dirChild struct {
	name string
	tree Checksum
	meta Checksum
}

func parseDirTree(checksum Checksum, data []byte) (dirTree, error) {
	if err := verifyObject(checksum, data); err != nil {
		return dirTree{}, err
	}
	value, err := gvariant.Parse(dirTreeType, data)
	if err != nil {
		return dirTree{}, fmt.Errorf("ostree: decode dirtree %s: %w", checksum, err)
	}
	fields := value.([]any)
	var tree dirTree
	for _, item := range fields[0].([]any) {
		entry := item.([]any)
		name := entry[0].(string)
		if err := validName(name); err != nil {
			return dirTree{}, err
		}
		sum, err := checksumFrom(entry[1])
		if err != nil {
			return dirTree{}, err
		}
		tree.files = append(tree.files, dirFile{name: name, checksum: sum})
	}
	for _, item := range fields[1].([]any) {
		entry := item.([]any)
		name := entry[0].(string)
		if err := validName(name); err != nil {
			return dirTree{}, err
		}
		treeSum, err := checksumFrom(entry[1])
		if err != nil {
			return dirTree{}, err
		}
		metaSum, err := checksumFrom(entry[2])
		if err != nil {
			return dirTree{}, err
		}
		tree.dirs = append(tree.dirs, dirChild{name: name, tree: treeSum, meta: metaSum})
	}
	return tree, nil
}

// validName keeps a tree entry to one path component. OSTree refuses the same
// names when it writes a tree; a repository that serves one anyway is trying
// to place a file outside the directory it is listed in.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("ostree: invalid tree entry name %q", name)
	}
	return nil
}

type dirMeta struct {
	uid, gid, mode uint32
}

func parseDirMeta(checksum Checksum, data []byte) (dirMeta, error) {
	if err := verifyObject(checksum, data); err != nil {
		return dirMeta{}, err
	}
	value, err := gvariant.Parse(dirMetaType, data)
	if err != nil {
		return dirMeta{}, fmt.Errorf("ostree: decode dirmeta %s: %w", checksum, err)
	}
	fields := value.([]any)
	meta := dirMeta{
		uid:  swap32(fields[0].(uint32)),
		gid:  swap32(fields[1].(uint32)),
		mode: swap32(fields[2].(uint32)),
	}
	if meta.mode&modeTypeMask != modeDir {
		return dirMeta{}, fmt.Errorf("ostree: dirmeta %s does not describe a directory", checksum)
	}
	return meta, nil
}

// FileHeader is what an archive file object says about the file it holds.
type FileHeader struct {
	Size   uint64
	UID    uint32
	GID    uint32
	Mode   uint32
	Rdev   uint32
	Target string
	xattrs any
}

func (h FileHeader) IsSymlink() bool { return h.Mode&modeTypeMask == modeSymlink }
func (h FileHeader) IsRegular() bool { return h.Mode&modeTypeMask == modeRegular }

// readArchiveHeader splits a .filez object into its header and the start of
// its compressed content.
func readArchiveHeader(data []byte) (FileHeader, int, error) {
	if len(data) < 8 {
		return FileHeader{}, 0, errors.New("ostree: truncated file object")
	}
	size := int(binary.BigEndian.Uint32(data[:4]))
	if size > len(data)-8 || size > 1<<20 {
		return FileHeader{}, 0, errors.New("ostree: file object header is out of range")
	}
	value, err := gvariant.Parse(archiveHeaderType, data[8:8+size])
	if err != nil {
		return FileHeader{}, 0, fmt.Errorf("ostree: decode file header: %w", err)
	}
	fields := value.([]any)
	header := FileHeader{
		Size:   swap64(fields[0].(uint64)),
		UID:    swap32(fields[1].(uint32)),
		GID:    swap32(fields[2].(uint32)),
		Mode:   swap32(fields[3].(uint32)),
		Rdev:   swap32(fields[4].(uint32)),
		Target: fields[5].(string),
		xattrs: fields[6],
	}
	if !header.IsRegular() && !header.IsSymlink() {
		return FileHeader{}, 0, fmt.Errorf("ostree: file object has unsupported mode %o", header.Mode)
	}
	return header, 8 + size, nil
}

// checksumPrefix is what the object checksum is taken over before the
// content: the header without its size, framed the way OSTree frames it on
// disk, a big-endian length and four bytes of padding.
func (h FileHeader) checksumPrefix() ([]byte, error) {
	encoded, err := gvariant.Encode(fileHeaderType, []any{
		swap32(h.UID), swap32(h.GID), swap32(h.Mode), swap32(h.Rdev), h.Target, h.xattrs,
	})
	if err != nil {
		return nil, err
	}
	prefix := make([]byte, 8, 8+len(encoded))
	binary.BigEndian.PutUint32(prefix, uint32(len(encoded)))
	return append(prefix, encoded...), nil
}

// cleanJoin joins a tree path the walk built from validated names.
func cleanJoin(parent, name string) string {
	if parent == "" {
		return name
	}
	return path.Join(parent, name)
}
