package cpak

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

func TestDownloadChunkedLayerSkipsKnownFiles(t *testing.T) {
	known := []byte("already in the object store")
	missing := []byte("download only this file")
	blob, descriptor, ranges := testChunkedBlob(t, []testChunkedFile{
		{name: "usr/share/known", mode: 0644, content: known},
		{name: "usr/share/missing", mode: 0644, content: missing},
	})
	requested := make([]string, 0, 3)
	server := testRangeRegistry(t, blob, &requested)
	defer server.Close()
	ref, err := oci.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}

	cp := newTestCpak(t)
	storage, err := store.Open(cp.daBaDeeStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	metadata := &store.Metadata{Mode: 0644, UID: os.Getuid(), GID: os.Getgid()}
	seed := filepath.Join(t.TempDir(), "known")
	if _, err = storage.Import(context.Background(), seed, bytes.NewReader(known), store.ImportOptions{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}

	digest := strings.TrimPrefix(descriptor.Digest, "sha256:")
	supported, err := cp.downloadChunkedLayer(&oci.Client{HTTP: server.Client()}, ref, descriptor, digest)
	if err != nil || !supported {
		t.Fatalf("chunked pull: supported=%v err=%v", supported, err)
	}
	for name, expected := range map[string][]byte{"known": known, "missing": missing} {
		got, readErr := os.ReadFile(cp.GetInStoreDir("layers", digest, "usr/share", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("%s: %q", name, got)
		}
	}
	if containsString(requested, ranges["known"]) {
		t.Fatalf("known payload was downloaded: %v", requested)
	}
	if !containsString(requested, ranges["missing"]) || !containsString(requested, ranges["manifest"]) {
		t.Fatalf("required ranges were not downloaded: %v", requested)
	}
}

func TestDownloadChunkedLayerAvoidsColdPartialPull(t *testing.T) {
	blob, descriptor, ranges := testChunkedBlob(t, []testChunkedFile{
		{name: "usr/share/first", mode: 0644, content: []byte("first")},
		{name: "usr/share/second", mode: 0644, content: []byte("second")},
	})
	requested := make([]string, 0, 2)
	server := testRangeRegistry(t, blob, &requested)
	defer server.Close()
	ref, err := oci.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}
	cp := newTestCpak(t)
	digest := strings.TrimPrefix(descriptor.Digest, "sha256:")
	supported, err := cp.downloadChunkedLayer(&oci.Client{HTTP: server.Client()}, ref, descriptor, digest)
	if supported || err != nil {
		t.Fatalf("cold chunked pull: supported=%v err=%v", supported, err)
	}
	if len(requested) != 1 || requested[0] != ranges["manifest"] {
		t.Fatalf("cold pull requested file payloads: %v", requested)
	}
}

func TestDownloadChunkedLayerRestoresZeroChunks(t *testing.T) {
	known := bytes.Repeat([]byte("known"), 32)
	missing := append(append([]byte("before"), make([]byte, 64)...), []byte("after")...)
	blob, descriptor, ranges := testChunkedBlobWithZeros(t, known)
	requested := make([]string, 0, 3)
	server := testRangeRegistry(t, blob, &requested)
	defer server.Close()
	ref, err := oci.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}
	cp := newTestCpak(t)
	storage, err := store.Open(cp.daBaDeeStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	metadata := &store.Metadata{Mode: 0644, UID: os.Getuid(), GID: os.Getgid()}
	seed := filepath.Join(t.TempDir(), "known")
	if _, err = storage.Import(context.Background(), seed, bytes.NewReader(known), store.ImportOptions{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}

	digest := strings.TrimPrefix(descriptor.Digest, "sha256:")
	supported, err := cp.downloadChunkedLayer(&oci.Client{HTTP: server.Client()}, ref, descriptor, digest)
	if err != nil || !supported {
		t.Fatalf("chunked pull: supported=%v err=%v", supported, err)
	}
	got, err := os.ReadFile(cp.GetInStoreDir("layers", digest, "usr/share/missing"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, missing) {
		t.Fatalf("zero chunk content: %q", got)
	}
	if !containsString(requested, ranges["missing"]) || !containsString(requested, ranges["manifest"]) {
		t.Fatalf("required ranges were not downloaded: %v", requested)
	}
}

func TestDownloadLayerFallsBackFromInvalidChunkedMetadata(t *testing.T) {
	blob := testLayer(t, mediaOCILayerZstd, []testLayerEntry{
		{name: "app/value", mode: 0644, content: []byte("fallback")},
	})
	hash := sha256.Sum256(blob)
	digest := hex.EncodeToString(hash[:])
	descriptor := oci.Descriptor{
		MediaType: mediaOCILayerZstd,
		Digest:    "sha256:" + digest,
		Size:      int64(len(blob)),
		Annotations: map[string]string{
			chunkedManifestPosition: fmt.Sprintf("0:%d:1:1", len(blob)),
			chunkedManifestChecksum: "sha256:" + strings.Repeat("0", 64),
		},
	}
	requested := make([]string, 0, 2)
	server := testRangeRegistry(t, blob, &requested)
	defer server.Close()
	ref, err := oci.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}
	cp := newTestCpak(t)
	if err = cp.downloadLayer(&oci.Client{HTTP: server.Client()}, ref, descriptor, digest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cp.GetInStoreDir("layers", digest, "app/value"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fallback" || !containsString(requested, "") {
		t.Fatalf("fallback result=%q requests=%v", got, requested)
	}
}

func TestParseChunkedPositionRejectsOutOfBoundsRange(t *testing.T) {
	for _, value := range []string{"", "1:2:3", "9:2:3:1", "0:0:3:1", "0:2:3:2", "-1:2:3:1"} {
		if _, err := parseChunkedPosition(value, 10); err == nil {
			t.Fatalf("position was accepted: %q", value)
		}
	}
}

type testChunkedFile struct {
	name    string
	mode    int64
	content []byte
}

func testChunkedBlob(t *testing.T, files []testChunkedFile) ([]byte, oci.Descriptor, map[string]string) {
	t.Helper()
	var blob bytes.Buffer
	entries := make([]chunkedEntry, 0, len(files))
	ranges := make(map[string]string)
	for _, file := range files {
		offset := int64(blob.Len())
		writeZstdFrame(t, &blob, file.content)
		end := int64(blob.Len())
		hash := sha256.Sum256(file.content)
		entries = append(entries, chunkedEntry{
			Type:      "reg",
			Name:      file.name,
			Mode:      file.mode,
			Size:      int64(len(file.content)),
			Digest:    "sha256:" + hex.EncodeToString(hash[:]),
			Offset:    offset,
			EndOffset: end,
		})
		ranges[filepath.Base(file.name)] = byteRange(offset, end-offset)
	}
	manifest, err := json.Marshal(chunkedTOC{Version: 1, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	var compressedManifest bytes.Buffer
	writeZstdFrame(t, &compressedManifest, manifest)
	manifestOffset := int64(blob.Len()) + 8
	if _, err = blob.Write([]byte{0x50, 0x2a, 0x4d, 0x18}); err != nil {
		t.Fatal(err)
	}
	if err = binary.Write(&blob, binary.LittleEndian, uint32(compressedManifest.Len())); err != nil {
		t.Fatal(err)
	}
	if _, err = blob.Write(compressedManifest.Bytes()); err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(compressedManifest.Bytes())
	blobHash := sha256.Sum256(blob.Bytes())
	descriptor := oci.Descriptor{
		MediaType: mediaOCILayerZstd,
		Digest:    "sha256:" + hex.EncodeToString(blobHash[:]),
		Size:      int64(blob.Len()),
		Annotations: map[string]string{
			chunkedManifestPosition: fmt.Sprintf("%d:%d:%d:1", manifestOffset, compressedManifest.Len(), len(manifest)),
			chunkedManifestChecksum: "sha256:" + hex.EncodeToString(manifestHash[:]),
		},
	}
	ranges["manifest"] = byteRange(manifestOffset, int64(compressedManifest.Len()))
	return blob.Bytes(), descriptor, ranges
}

func testChunkedBlobWithZeros(t *testing.T, known []byte) ([]byte, oci.Descriptor, map[string]string) {
	t.Helper()
	var blob bytes.Buffer
	entries := make([]chunkedEntry, 0, 4)
	ranges := make(map[string]string)
	knownOffset := int64(blob.Len())
	writeZstdFrame(t, &blob, known)
	knownEnd := int64(blob.Len())
	knownHash := sha256.Sum256(known)
	entries = append(entries, chunkedEntry{
		Type: "reg", Name: "usr/share/known", Mode: 0644, Size: int64(len(known)),
		Digest: "sha256:" + hex.EncodeToString(knownHash[:]), Offset: knownOffset, EndOffset: knownEnd,
	})
	ranges["known"] = byteRange(knownOffset, knownEnd-knownOffset)

	missingOffset := int64(blob.Len())
	writeZstdFrame(t, &blob, []byte("before"))
	zeroOffset := int64(blob.Len())
	if _, err := blob.Write([]byte{0x50, 0x2a, 0x4d, 0x18, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	afterOffset := int64(blob.Len())
	writeZstdFrame(t, &blob, []byte("after"))
	missingEnd := int64(blob.Len())
	missing := append(append([]byte("before"), make([]byte, 64)...), []byte("after")...)
	missingHash := sha256.Sum256(missing)
	entries = append(entries,
		chunkedEntry{
			Type: "reg", Name: "usr/share/missing", Mode: 0644, Size: int64(len(missing)),
			Digest: "sha256:" + hex.EncodeToString(missingHash[:]), Offset: missingOffset, EndOffset: missingEnd, ChunkSize: 6,
		},
		chunkedEntry{Type: "chunk", Name: "usr/share/missing", Offset: zeroOffset, ChunkSize: 64, ChunkType: "zeros"},
		chunkedEntry{Type: "chunk", Name: "usr/share/missing", Offset: afterOffset, ChunkSize: 5},
	)
	ranges["missing"] = byteRange(missingOffset, missingEnd-missingOffset)
	return finishTestChunkedBlob(t, &blob, entries, ranges)
}

func finishTestChunkedBlob(t *testing.T, blob *bytes.Buffer, entries []chunkedEntry, ranges map[string]string) ([]byte, oci.Descriptor, map[string]string) {
	t.Helper()
	manifest, err := json.Marshal(chunkedTOC{Version: 1, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	var compressedManifest bytes.Buffer
	writeZstdFrame(t, &compressedManifest, manifest)
	manifestOffset := int64(blob.Len()) + 8
	if _, err = blob.Write([]byte{0x50, 0x2a, 0x4d, 0x18}); err != nil {
		t.Fatal(err)
	}
	if err = binary.Write(blob, binary.LittleEndian, uint32(compressedManifest.Len())); err != nil {
		t.Fatal(err)
	}
	if _, err = blob.Write(compressedManifest.Bytes()); err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(compressedManifest.Bytes())
	blobHash := sha256.Sum256(blob.Bytes())
	descriptor := oci.Descriptor{
		MediaType: mediaOCILayerZstd,
		Digest:    "sha256:" + hex.EncodeToString(blobHash[:]),
		Size:      int64(blob.Len()),
		Annotations: map[string]string{
			chunkedManifestPosition: fmt.Sprintf("%d:%d:%d:1", manifestOffset, compressedManifest.Len(), len(manifest)),
			chunkedManifestChecksum: "sha256:" + hex.EncodeToString(manifestHash[:]),
		},
	}
	ranges["manifest"] = byteRange(manifestOffset, int64(compressedManifest.Len()))
	return blob.Bytes(), descriptor, ranges
}

func writeZstdFrame(t *testing.T, destination *bytes.Buffer, content []byte) {
	t.Helper()
	encoder, err := zstd.NewWriter(destination, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = encoder.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatal(err)
	}
}

func testRangeRegistry(t *testing.T, content []byte, requested *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rangeValue := request.Header.Get("Range")
		*requested = append(*requested, rangeValue)
		if rangeValue == "" {
			writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
			_, _ = writer.Write(content)
			return
		}
		parts := strings.Split(strings.TrimPrefix(rangeValue, "bytes="), "-")
		if len(parts) != 2 {
			http.Error(writer, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, startErr := strconv.ParseInt(parts[0], 10, 64)
		end, endErr := strconv.ParseInt(parts[1], 10, 64)
		if startErr != nil || endErr != nil || start < 0 || end < start || end >= int64(len(content)) {
			http.Error(writer, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		body := content[start : end+1]
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(body)
	}))
}

func byteRange(offset, length int64) string {
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
