/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestTarUnpackReadsGzipWithoutTheHostTarCommand(t *testing.T) {
	archive := writeTestTar(t, true, []tar.Header{{Name: "usr/bin/demo", Mode: 04755, Size: 4}}, [][]byte{[]byte("demo")})
	destination := t.TempDir()
	if err := TarUnpack(archive, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "usr/bin/demo"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "demo" {
		t.Fatalf("content = %q", content)
	}
	info, err := os.Stat(filepath.Join(destination, "usr/bin/demo"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("unsafe mode survived extraction: %v", info.Mode())
	}
}

func TestTarUnpackRejectsPathsAndLinksOutsideTheDestination(t *testing.T) {
	tests := []tar.Header{
		{Name: "../outside", Mode: 0644, Size: 1},
		{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "../../outside"},
		{Name: "absolute", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "device", Typeflag: tar.TypeChar},
	}
	for _, header := range tests {
		header := header
		t.Run(header.Name, func(t *testing.T) {
			payload := []byte(nil)
			if header.Size > 0 {
				payload = []byte("x")
			}
			archive := writeTestTar(t, false, []tar.Header{header}, [][]byte{payload})
			if err := TarUnpack(archive, t.TempDir()); err == nil {
				t.Fatal("unsafe archive entry was accepted")
			}
		})
	}
}

func writeTestTar(t *testing.T, compressed bool, headers []tar.Header, contents [][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.tar")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var writer io.Writer = file
	var gzipWriter *gzip.Writer
	if compressed {
		gzipWriter = gzip.NewWriter(file)
		writer = gzipWriter
	}
	archive := tar.NewWriter(writer)
	for index := range headers {
		if err = archive.WriteHeader(&headers[index]); err != nil {
			t.Fatal(err)
		}
		if len(contents[index]) > 0 {
			if _, err = archive.Write(contents[index]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if gzipWriter != nil {
		if err = gzipWriter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
