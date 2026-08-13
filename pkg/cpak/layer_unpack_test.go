package cpak

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

type testLayerEntry struct {
	name     string
	typeflag byte
	mode     int64
	linkname string
	content  []byte
}

func TestUnpackLayerStreamsGzipIntoStore(t *testing.T) {
	root := t.TempDir()
	storage, err := store.Open(store.Options{Root: filepath.Join(root, "objects"), PreserveMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	content := []byte("shared layer content")
	layer := testLayer(t, mediaOCILayerGzip, []testLayerEntry{
		{name: "usr/share/first", typeflag: tar.TypeReg, mode: 0644, content: content},
		{name: "usr/share/second", typeflag: tar.TypeReg, mode: 0644, content: content},
	})
	destination := filepath.Join(root, "layer")
	if err = unpackLayer(context.Background(), bytes.NewReader(layer), mediaOCILayerGzip, destination, storage); err != nil {
		t.Fatal(err)
	}

	first, err := os.Stat(filepath.Join(destination, "usr/share/first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(filepath.Join(destination, "usr/share/second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Sys().(*syscall.Stat_t).Ino != second.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("equal layer files do not share an object")
	}
}

func TestUnpackLayerReadsZstd(t *testing.T) {
	root := t.TempDir()
	storage, err := store.Open(store.Options{Root: filepath.Join(root, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	layer := testLayer(t, mediaOCILayerZstd, []testLayerEntry{
		{name: "app/value", typeflag: tar.TypeReg, mode: 0755, content: []byte("zstd")},
	})
	destination := filepath.Join(root, "layer")
	if err = unpackLayer(context.Background(), bytes.NewReader(layer), mediaOCILayerZstd, destination, storage); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "app/value"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "zstd" {
		t.Fatalf("content: %q", content)
	}
}

func TestUnpackLayerRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	storage, err := store.Open(store.Options{Root: filepath.Join(root, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	layer := testLayer(t, mediaOCILayerTar, []testLayerEntry{
		{name: "../escape", typeflag: tar.TypeReg, mode: 0644, content: []byte("escape")},
	})
	err = unpackLayer(context.Background(), bytes.NewReader(layer), mediaOCILayerTar, filepath.Join(root, "layer"), storage)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("path traversal was accepted: %v", err)
	}
}

func TestUnpackLayerRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	storage, err := store.Open(store.Options{Root: filepath.Join(root, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	layer := testLayer(t, mediaOCILayerTar, []testLayerEntry{
		{name: "escape", typeflag: tar.TypeSymlink, mode: 0777, linkname: "../outside"},
		{name: "escape/value", typeflag: tar.TypeReg, mode: 0644, content: []byte("escape")},
	})
	err = unpackLayer(context.Background(), bytes.NewReader(layer), mediaOCILayerTar, filepath.Join(root, "layer"), storage)
	if err == nil || !strings.Contains(err.Error(), "parent is not a directory") {
		t.Fatalf("symlink parent was accepted: %v", err)
	}
}

func TestUnpackLayerRejectsHardlinkToSymlink(t *testing.T) {
	root := t.TempDir()
	storage, err := store.Open(store.Options{Root: filepath.Join(root, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	layer := testLayer(t, mediaOCILayerTar, []testLayerEntry{
		{name: "target", typeflag: tar.TypeSymlink, mode: 0777, linkname: "../outside"},
		{name: "copy", typeflag: tar.TypeLink, mode: 0644, linkname: "target"},
	})
	err = unpackLayer(context.Background(), bytes.NewReader(layer), mediaOCILayerTar, filepath.Join(root, "layer"), storage)
	if err == nil || !strings.Contains(err.Error(), "target is not a regular file") {
		t.Fatalf("hardlink to symlink was accepted: %v", err)
	}
}

func testLayer(t *testing.T, mediaType string, entries []testLayerEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	var writer io.WriteCloser
	switch mediaType {
	case mediaOCILayerGzip, mediaDockerLayerGzip:
		writer = gzip.NewWriter(&output)
	case mediaOCILayerZstd:
		encoder, err := zstd.NewWriter(&output, zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		writer = encoder
	default:
		writer = nopWriteCloser{Writer: &output}
	}
	tarWriter := tar.NewWriter(writer)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
			Size:     int64(len(entry.content)),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.content) > 0 {
			if _, err := tarWriter.Write(entry.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}
