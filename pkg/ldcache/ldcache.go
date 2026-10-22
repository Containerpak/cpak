/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package ldcache writes the dynamic linker cache glibc reads from
// /etc/ld.so.cache, in its current format, for a root that is not the one
// cpak runs in.
//
// It exists so that cpak never has to run a program to index a root it is
// about to start an application in: the host's ldconfig may be missing or of
// another glibc, and the root's own ldconfig belongs to the package, which is
// code cpak only runs inside the sandbox it builds.
package ldcache

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

const (
	cacheMagic   = "glibc-ld.so.cache"
	cacheVersion = "1.1"

	flagELFLibc6   = 0x0003
	flagX8664Lib64 = 0x0300
	flagAArch64    = 0x0a00

	endianLittle = 2

	headerSize = 48
	entrySize  = 24

	maxConfigIncludes = 64
	maxLibraries      = 1 << 16
)

type library struct {
	soname string
	path   string
	flags  int32
}

// ReadConfig answers with the directories an ld.so.conf names, in order,
// following its include lines. Every path is read inside root.
func ReadConfig(root *os.Root, name string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	includes := 0
	var read func(name string) error
	read = func(name string) error {
		data, err := root.ReadFile(strings.TrimPrefix(name, "/"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("ldcache: read %s: %w", name, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if index := strings.IndexByte(line, '#'); index >= 0 {
				line = line[:index]
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if pattern, ok := strings.CutPrefix(line, "include"); ok && (pattern == "" || pattern[0] == ' ' || pattern[0] == '\t') {
				includes++
				if includes > maxConfigIncludes {
					return errors.New("ldcache: too many include lines")
				}
				pattern = strings.TrimSpace(pattern)
				if !path.IsAbs(pattern) {
					pattern = path.Join(path.Dir(name), pattern)
				}
				matches, err := fs.Glob(root.FS(), strings.TrimPrefix(path.Clean(pattern), "/"))
				if err != nil {
					return fmt.Errorf("ldcache: include %s: %w", pattern, err)
				}
				sort.Strings(matches)
				for _, match := range matches {
					if err := read("/" + match); err != nil {
						return err
					}
				}
				continue
			}
			if !path.IsAbs(line) {
				continue
			}
			dir := path.Clean(line)
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
		return nil
	}
	if err := read(name); err != nil {
		return nil, err
	}
	return dirs, nil
}

// Build answers with a cache for the shared libraries in dirs. A library is
// found where ldconfig would find it: a file named lib* or ld-* holding .so,
// that is an ELF shared object of an architecture the cache can describe. The
// first directory that provides a soname wins, as it does for ldconfig.
func Build(root *os.Root, dirs []string) ([]byte, error) {
	var libraries []library
	provided := map[string]bool{}
	for _, dir := range dirs {
		found, err := scanDirectory(root, dir)
		if err != nil {
			return nil, err
		}
		for _, lib := range found {
			key := fmt.Sprintf("%s\x00%d", lib.soname, lib.flags)
			if provided[key] {
				continue
			}
			provided[key] = true
			libraries = append(libraries, lib)
			if len(libraries) > maxLibraries {
				return nil, errors.New("ldcache: too many libraries")
			}
		}
	}
	return encode(libraries), nil
}

func scanDirectory(root *os.Root, dir string) ([]library, error) {
	relative := strings.TrimPrefix(path.Clean(dir), "/")
	entries, err := fs.ReadDir(root.FS(), relative)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
		return nil, nil
	}
	if err != nil {
		// A directory the configuration names but the root does not let
		// cpak read is one the linker cannot use either.
		return nil, nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if (strings.HasPrefix(name, "lib") || strings.HasPrefix(name, "ld-")) && strings.Contains(name, ".so") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	present := map[string]bool{}
	for _, name := range names {
		present[name] = true
	}
	var found []library
	sonames := map[string]int{}
	for _, name := range names {
		soname, flags, ok := inspect(root, path.Join(relative, name))
		if !ok {
			continue
		}
		if soname == "" {
			soname = name
		}
		target := name
		if present[soname] {
			target = soname
		}
		lib := library{soname: soname, path: path.Join("/", relative, target), flags: flags}
		if index, seen := sonames[soname+string(rune(flags))]; seen {
			found[index] = lib
			continue
		}
		sonames[soname+string(rune(flags))] = len(found)
		found = append(found, lib)
	}
	return found, nil
}

// inspect reads the soname and cache flags of a shared object, answering
// false for anything else, which is what most of a lib directory is.
func inspect(root *os.Root, name string) (string, int32, bool) {
	file, err := root.Open(name)
	if err != nil {
		return "", 0, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 64 {
		return "", 0, false
	}
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil || string(magic[:]) != elf.ELFMAG {
		return "", 0, false
	}
	object, err := elf.NewFile(file)
	if err != nil {
		return "", 0, false
	}
	defer object.Close()
	if object.Type != elf.ET_DYN {
		return "", 0, false
	}
	var flags int32
	switch {
	case object.Class == elf.ELFCLASS64 && object.Machine == elf.EM_X86_64:
		flags = flagELFLibc6 | flagX8664Lib64
	case object.Class == elf.ELFCLASS64 && object.Machine == elf.EM_AARCH64:
		flags = flagELFLibc6 | flagAArch64
	case object.Class == elf.ELFCLASS32 && object.Machine == elf.EM_386:
		flags = flagELFLibc6
	default:
		return "", 0, false
	}
	sonames, err := object.DynString(elf.DT_SONAME)
	if err != nil {
		return "", 0, false
	}
	if len(sonames) > 0 {
		soname := sonames[0]
		if soname == "" || strings.ContainsAny(soname, "/\x00") {
			return "", 0, false
		}
		return soname, flags, true
	}
	return "", flags, true
}

// compare is glibc's _dl_cache_libcmp: runs of digits compare as numbers.
func compare(first, second string) int {
	i, j := 0, 0
	for i < len(first) {
		switch {
		case isDigit(first[i]) && j < len(second) && isDigit(second[j]):
			left, right := 0, 0
			for i < len(first) && isDigit(first[i]) {
				left = left*10 + int(first[i]-'0')
				i++
			}
			for j < len(second) && isDigit(second[j]) {
				right = right*10 + int(second[j]-'0')
				j++
			}
			if left != right {
				return left - right
			}
		case isDigit(first[i]):
			return 1
		case j < len(second) && isDigit(second[j]):
			return -1
		case j >= len(second):
			return int(first[i])
		case first[i] != second[j]:
			return int(first[i]) - int(second[j])
		default:
			i++
			j++
		}
	}
	if j < len(second) {
		return -int(second[j])
	}
	return 0
}

func isDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

// encode lays the cache out the way ldconfig does: the header, the entries
// sorted in descending order of their names, which is the order the linker's
// binary search expects, then the strings, every offset counted from the start
// of the file.
func encode(libraries []library) []byte {
	sort.SliceStable(libraries, func(first, second int) bool {
		order := compare(libraries[first].soname, libraries[second].soname)
		if order != 0 {
			return order > 0
		}
		return libraries[first].flags > libraries[second].flags
	})
	stringsStart := headerSize + entrySize*len(libraries)
	var table bytes.Buffer
	offsets := map[string]uint32{}
	intern := func(value string) uint32 {
		if offset, ok := offsets[value]; ok {
			return offset
		}
		offset := uint32(stringsStart + table.Len())
		table.WriteString(value)
		table.WriteByte(0)
		offsets[value] = offset
		return offset
	}
	entries := make([]byte, 0, entrySize*len(libraries))
	for _, lib := range libraries {
		entries = binary.LittleEndian.AppendUint32(entries, uint32(lib.flags))
		entries = binary.LittleEndian.AppendUint32(entries, intern(lib.soname))
		entries = binary.LittleEndian.AppendUint32(entries, intern(lib.path))
		entries = binary.LittleEndian.AppendUint32(entries, 0)
		entries = binary.LittleEndian.AppendUint64(entries, 0)
	}
	header := make([]byte, 0, headerSize)
	header = append(header, cacheMagic...)
	header = append(header, cacheVersion...)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(libraries)))
	header = binary.LittleEndian.AppendUint32(header, uint32(table.Len()))
	header = append(header, endianLittle, 0, 0, 0)
	header = binary.LittleEndian.AppendUint32(header, 0)
	header = append(header, make([]byte, 12)...)
	out := append(header, entries...)
	return append(out, table.Bytes()...)
}
