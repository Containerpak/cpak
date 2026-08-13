package cpak

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/klauspost/compress/zstd"
)

type testLayerEntry struct {
	name     string
	typeflag byte
	mode     int64
	linkname string
	content  []byte
}

func TestUnpackLayerStreamsGzipIntoStore(t *testing.T) {
	content := []byte("shared layer content")
	layer := testLayer(t, mediaOCILayerGzip, []testLayerEntry{
		{name: "usr/share/first", typeflag: tar.TypeReg, mode: 0644, content: content},
		{name: "usr/share/second", typeflag: tar.TypeReg, mode: 0644, content: content},
	})
	files, err := unpackTestLayer(t, layer, mediaOCILayerGzip)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]fvsrepo.FileEntry, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	first := byPath["usr/share/first"]
	second := byPath["usr/share/second"]
	if len(first.Blocks) == 0 || len(second.Blocks) == 0 || first.Blocks[0] != second.Blocks[0] {
		t.Fatal("equal layer files do not share blocks")
	}
}

func TestUnpackLayerReadsZstd(t *testing.T) {
	layer := testLayer(t, mediaOCILayerZstd, []testLayerEntry{
		{name: "app/value", typeflag: tar.TypeReg, mode: 0755, content: []byte("zstd")},
	})
	files, err := unpackTestLayer(t, layer, mediaOCILayerZstd)
	if err != nil {
		t.Fatal(err)
	}
	var value fvsrepo.FileEntry
	for _, file := range files {
		if file.Path == "app/value" {
			value = file
			break
		}
	}
	if value.Path == "" || value.Mode != 0o755 || value.Size != 4 {
		t.Fatalf("files = %+v", files)
	}
}

func TestUnpackLayerRejectsPathTraversal(t *testing.T) {
	layer := testLayer(t, mediaOCILayerTar, []testLayerEntry{
		{name: "../escape", typeflag: tar.TypeReg, mode: 0644, content: []byte("escape")},
	})
	_, err := unpackTestLayer(t, layer, mediaOCILayerTar)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("path traversal was accepted: %v", err)
	}
}

func TestUnpackLayerRejectsSymlinkParent(t *testing.T) {
	layer := testLayer(t, mediaOCILayerTar, []testLayerEntry{
		{name: "escape", typeflag: tar.TypeSymlink, mode: 0777, linkname: "../outside"},
		{name: "escape/value", typeflag: tar.TypeReg, mode: 0644, content: []byte("escape")},
	})
	_, err := unpackTestLayer(t, layer, mediaOCILayerTar)
	if err == nil || !strings.Contains(err.Error(), "non-directory parent") {
		t.Fatalf("symlink parent was accepted: %v", err)
	}
}

func TestUnpackLayerRejectsHardlinkToSymlink(t *testing.T) {
	layer := testLayer(t, mediaOCILayerTar, []testLayerEntry{
		{name: "target", typeflag: tar.TypeSymlink, mode: 0777, linkname: "../outside"},
		{name: "copy", typeflag: tar.TypeLink, mode: 0644, linkname: "target"},
	})
	_, err := unpackTestLayer(t, layer, mediaOCILayerTar)
	if err == nil || !strings.Contains(err.Error(), "targets missing regular file") {
		t.Fatalf("hardlink to symlink was accepted: %v", err)
	}
}

func unpackTestLayer(t *testing.T, layer []byte, mediaType string) ([]fvsrepo.FileEntry, error) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "layer")
	if _, err := fvsrepo.Init(repository, 0); err != nil {
		return nil, err
	}
	writer, err := fvsrepo.BeginSnapshot(repository, fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		return nil, err
	}
	if err := unpackLayer(context.Background(), bytes.NewReader(layer), mediaType, writer); err != nil {
		_ = writer.Abort()
		return nil, err
	}
	result, err := writer.Commit()
	if err != nil {
		return nil, err
	}
	return fvsrepo.StateFiles(repository, result.StateID)
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
