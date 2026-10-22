/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package ldcache

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sharedObject builds the smallest ELF shared object debug/elf reads a soname
// from: a dynamic section naming it and the string table it points into.
func sharedObject(soname string, class elf.Class, machine elf.Machine) []byte {
	dynstr := append([]byte{0}, append([]byte(soname), 0)...)
	shstrtab := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")
	var dynamic bytes.Buffer
	is64 := class == elf.ELFCLASS64
	writeWord := func(value uint64) {
		if is64 {
			binary.Write(&dynamic, binary.LittleEndian, value)
		} else {
			binary.Write(&dynamic, binary.LittleEndian, uint32(value))
		}
	}
	writeWord(uint64(elf.DT_SONAME))
	writeWord(1)
	writeWord(uint64(elf.DT_NULL))
	writeWord(0)

	headerSize, sectionSize := 52, 40
	if is64 {
		headerSize, sectionSize = 64, 64
	}
	dynstrOffset := headerSize
	dynamicOffset := dynstrOffset + len(dynstr)
	for dynamicOffset%8 != 0 {
		dynamicOffset++
	}
	shstrtabOffset := dynamicOffset + dynamic.Len()
	sectionsOffset := shstrtabOffset + len(shstrtab)
	for sectionsOffset%8 != 0 {
		sectionsOffset++
	}

	var out bytes.Buffer
	ident := []byte{0x7f, 'E', 'L', 'F', byte(class), byte(elf.ELFDATA2LSB), 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	out.Write(ident)
	binary.Write(&out, binary.LittleEndian, uint16(elf.ET_DYN))
	binary.Write(&out, binary.LittleEndian, uint16(machine))
	binary.Write(&out, binary.LittleEndian, uint32(1))
	if is64 {
		binary.Write(&out, binary.LittleEndian, uint64(0))
		binary.Write(&out, binary.LittleEndian, uint64(0))
		binary.Write(&out, binary.LittleEndian, uint64(sectionsOffset))
	} else {
		binary.Write(&out, binary.LittleEndian, uint32(0))
		binary.Write(&out, binary.LittleEndian, uint32(0))
		binary.Write(&out, binary.LittleEndian, uint32(sectionsOffset))
	}
	binary.Write(&out, binary.LittleEndian, uint32(0))
	binary.Write(&out, binary.LittleEndian, uint16(headerSize))
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(sectionSize))
	binary.Write(&out, binary.LittleEndian, uint16(4))
	binary.Write(&out, binary.LittleEndian, uint16(3))
	out.Write(dynstr)
	for out.Len() < dynamicOffset {
		out.WriteByte(0)
	}
	out.Write(dynamic.Bytes())
	out.Write(shstrtab)
	for out.Len() < sectionsOffset {
		out.WriteByte(0)
	}
	section := func(name uint32, kind elf.SectionType, offset, size int, link uint32, entsize int) {
		binary.Write(&out, binary.LittleEndian, name)
		binary.Write(&out, binary.LittleEndian, uint32(kind))
		word := func(value uint64) {
			if is64 {
				binary.Write(&out, binary.LittleEndian, value)
			} else {
				binary.Write(&out, binary.LittleEndian, uint32(value))
			}
		}
		word(0)
		word(0)
		word(uint64(offset))
		word(uint64(size))
		binary.Write(&out, binary.LittleEndian, link)
		binary.Write(&out, binary.LittleEndian, uint32(0))
		word(8)
		word(uint64(entsize))
	}
	section(0, elf.SHT_NULL, 0, 0, 0, 0)
	section(1, elf.SHT_STRTAB, dynstrOffset, len(dynstr), 0, 0)
	entsize := 8
	if is64 {
		entsize = 16
	}
	section(9, elf.SHT_DYNAMIC, dynamicOffset, dynamic.Len(), 1, entsize)
	section(18, elf.SHT_STRTAB, shstrtabOffset, len(shstrtab), 0, 0)
	return out.Bytes()
}

func writeFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

type entry struct {
	flags  uint32
	soname string
	path   string
}

// parse reads a cache back the way ld.so does, so the test holds the layout
// and not merely the encoder's own idea of it.
func parse(t *testing.T, data []byte) []entry {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("glibc-ld.so.cache1.1")) {
		t.Fatalf("cache does not start with the new-format magic: %q", data[:20])
	}
	count := binary.LittleEndian.Uint32(data[20:24])
	if data[28] != 2 {
		t.Fatalf("cache is not marked little-endian: %d", data[28])
	}
	text := func(offset uint32) string {
		if int(offset) >= len(data) {
			t.Fatalf("string offset %d is outside the cache", offset)
		}
		end := bytes.IndexByte(data[offset:], 0)
		return string(data[offset : int(offset)+end])
	}
	var entries []entry
	for index := range count {
		at := 48 + 24*int(index)
		entries = append(entries, entry{
			flags:  binary.LittleEndian.Uint32(data[at:]),
			soname: text(binary.LittleEndian.Uint32(data[at+4:])),
			path:   text(binary.LittleEndian.Uint32(data[at+8:])),
		})
	}
	return entries
}

func TestCacheListsLibrariesTheWayTheLinkerSearchesThem(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app/lib/libapp.so.1.2.3", sharedObject("libapp.so.1", elf.ELFCLASS64, elf.EM_X86_64))
	if err := os.Symlink("libapp.so.1.2.3", filepath.Join(dir, "app/lib/libapp.so.1")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "usr/lib/libapp.so.1", sharedObject("libapp.so.1", elf.ELFCLASS64, elf.EM_X86_64))
	writeFile(t, dir, "usr/lib/libz.so.1", sharedObject("libz.so.1", elf.ELFCLASS64, elf.EM_X86_64))
	writeFile(t, dir, "usr/lib/libold.so.2", sharedObject("libold.so.2", elf.ELFCLASS32, elf.EM_386))
	writeFile(t, dir, "usr/lib/libarm.so.1", sharedObject("libarm.so.1", elf.ELFCLASS32, elf.EM_ARM))
	writeFile(t, dir, "usr/lib/libnotes.so.txt", []byte("not an object"))
	writeFile(t, dir, "usr/lib/README.so", sharedObject("libreadme.so", elf.ELFCLASS64, elf.EM_X86_64))
	writeFile(t, dir, "etc/ld.so.conf", []byte("# order matters\ninclude /etc/ld.so.conf.d/*.conf\n/app/lib\n"))
	writeFile(t, dir, "etc/ld.so.conf.d/runtime.conf", []byte("/usr/lib\n/missing\n"))

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dirs, err := ReadConfig(root, "/etc/ld.so.conf")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dirs, []string{"/usr/lib", "/missing", "/app/lib"}) {
		t.Fatalf("configuration read as %v", dirs)
	}
	data, err := Build(root, []string{"/app/lib", "/usr/lib"})
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, data)
	want := []entry{
		{flags: 0x0303, soname: "libz.so.1", path: "/usr/lib/libz.so.1"},
		{flags: 0x0003, soname: "libold.so.2", path: "/usr/lib/libold.so.2"},
		{flags: 0x0303, soname: "libapp.so.1", path: "/app/lib/libapp.so.1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cache holds\n%+v\nwant\n%+v", got, want)
	}
}

func TestNumbersCompareAsNumbers(t *testing.T) {
	cases := []struct {
		first, second string
		sign          int
	}{
		{"libfoo.so.10", "libfoo.so.9", 1},
		{"libfoo.so.2", "libfoo.so.2", 0},
		{"liba.so", "libb.so", -1},
		{"libfoo.so.1", "libfoo.so", 1},
		{"libfoo2.so", "libfoo.so", 1},
	}
	for _, test := range cases {
		got := compare(test.first, test.second)
		if (got > 0) != (test.sign > 0) || (got < 0) != (test.sign < 0) {
			t.Fatalf("compare(%q, %q) = %d, want the sign of %d", test.first, test.second, got, test.sign)
		}
	}
}

// A configuration is read inside the root it describes, so an include that
// climbs out of it reads nothing of the host.
func TestConfigurationStaysInsideItsRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "etc/ld.so.conf", []byte("include ../../../../../../etc/*.conf\n/usr/lib\n"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dirs, err := ReadConfig(root, "/etc/ld.so.conf")
	if err == nil && len(dirs) != 1 {
		t.Fatalf("an include outside the root was followed: %v", dirs)
	}
	if err != nil && !strings.Contains(err.Error(), "include") {
		t.Fatalf("unexpected error: %v", err)
	}
}
