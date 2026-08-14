/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/klauspost/compress/zstd"
	"github.com/mirkobrombin/cpak/pkg/oci"
)

const (
	chunkedManifestChecksum = "io.github.containers.zstd-chunked.manifest-checksum"
	chunkedManifestPosition = "io.github.containers.zstd-chunked.manifest-position"
	chunkedManifestType     = 1
	maximumChunkedManifest  = 64 << 20
	maximumPartialFiles     = 128
	minimumReusableDivisor  = 5
)

type chunkedPosition struct {
	offset             int64
	compressedLength   int64
	uncompressedLength int64
}

type chunkedTOC struct {
	Version int            `json:"version"`
	Entries []chunkedEntry `json:"entries"`
}

type chunkedEntry struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Linkname    string `json:"linkName,omitempty"`
	Mode        int64  `json:"mode,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Digest      string `json:"digest,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
	EndOffset   int64  `json:"endOffset,omitempty"`
	ChunkSize   int64  `json:"chunkSize,omitempty"`
	ChunkOffset int64  `json:"chunkOffset,omitempty"`
	ChunkDigest string `json:"chunkDigest,omitempty"`
	ChunkType   string `json:"chunkType,omitempty"`
}

type chunkedFileReader struct {
	source         io.Reader
	segments       []chunkedEntry
	index          int
	decoder        *zstd.Decoder
	compressed     *io.LimitedReader
	segmentRead    int64
	zerosRemaining int64
}

func (c *Cpak) downloadChunkedLayer(client *oci.Client, ref oci.Reference, layer oci.Descriptor, digest string) (supported bool, err error) {
	positionValue, hasPosition := layer.Annotations[chunkedManifestPosition]
	checksumValue, hasChecksum := layer.Annotations[chunkedManifestChecksum]
	if !hasPosition && !hasChecksum {
		return false, nil
	}
	if layer.MediaType != mediaOCILayerZstd || !hasPosition || !hasChecksum {
		return true, fmt.Errorf("chunked layer annotations are incomplete")
	}
	position, err := parseChunkedPosition(positionValue, layer.Size)
	if err != nil {
		return true, err
	}
	checksum, err := parseSHA256Digest(checksumValue)
	if err != nil {
		return true, err
	}
	compressedManifest, err := readBlobRange(c.Ctx, client, ref, layer, position.offset, position.compressedLength)
	if err != nil {
		return true, err
	}
	hash := sha256.Sum256(compressedManifest)
	if !strings.EqualFold(hex.EncodeToString(hash[:]), checksum) {
		return true, fmt.Errorf("chunked manifest digest mismatch")
	}
	manifest, err := decodeChunkedManifest(compressedManifest, position.uncompressedLength)
	if err != nil {
		return true, err
	}

	destination, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{Message: "pull " + digest})
	if err != nil {
		return true, err
	}
	known, err := c.fvsContentIndex()
	if err != nil {
		_ = writer.Abort()
		_ = os.RemoveAll(destination)
		return true, err
	}
	published := false
	defer func() {
		if !published {
			_ = writer.Abort()
			_ = os.RemoveAll(destination)
		}
	}()
	worthwhile, err := chunkedPullWorthwhile(c.Ctx, manifest, layer, known)
	if err != nil {
		return true, err
	}
	if !worthwhile {
		return false, nil
	}
	if err = applyChunkedTOC(c.Ctx, client, ref, layer, manifest, writer, known); err != nil {
		return true, err
	}
	if _, err = writer.Commit(); err != nil {
		return true, err
	}
	if err = publishFVSLayer(destination, c.fvsLayerPath(digest)); err != nil {
		return true, err
	}
	published = true
	return true, nil
}

func chunkedPullWorthwhile(ctx context.Context, manifest chunkedTOC, layer oci.Descriptor, known map[string]fvsrepo.FileEntry) (bool, error) {
	var compressedBytes int64
	var reusableBytes int64
	missingFiles := 0
	for index := 0; index < len(manifest.Entries); index++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		entry := manifest.Entries[index]
		if entry.Type == "chunk" {
			return false, fmt.Errorf("unpack chunked layer: orphan chunk entry")
		}
		if entry.Type != "reg" || entry.Size == 0 {
			continue
		}
		segments, next, err := chunkedFileSegments(manifest.Entries, index, layer.Size)
		if err != nil {
			return false, err
		}
		index = next - 1
		digest, err := parseSHA256Digest(entry.Digest)
		if err != nil {
			return false, fmt.Errorf("unpack chunked layer: invalid file digest")
		}
		fileBytes := segments[len(segments)-1].EndOffset - segments[0].Offset
		if fileBytes > layer.Size || compressedBytes > math.MaxInt64-fileBytes {
			return false, fmt.Errorf("unpack chunked layer: invalid file ranges")
		}
		compressedBytes += fileBytes
		if reusable, ok := known["sha256:"+digest]; ok && reusable.Size == entry.Size {
			reusableBytes += fileBytes
		} else {
			missingFiles++
		}
	}
	minimumReusable := compressedBytes / minimumReusableDivisor
	if compressedBytes%minimumReusableDivisor != 0 {
		minimumReusable++
	}
	if compressedBytes == 0 || reusableBytes < minimumReusable {
		return false, nil
	}
	return missingFiles <= maximumPartialFiles, nil
}

func parseChunkedPosition(value string, layerSize int64) (chunkedPosition, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 4 {
		return chunkedPosition{}, fmt.Errorf("invalid chunked manifest position")
	}
	values := make([]int64, len(parts))
	for index, part := range parts {
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil || value < 0 {
			return chunkedPosition{}, fmt.Errorf("invalid chunked manifest position")
		}
		values[index] = value
	}
	if values[3] != chunkedManifestType || values[1] == 0 || values[2] == 0 || values[1] > maximumChunkedManifest || values[2] > maximumChunkedManifest || values[0] > layerSize-values[1] {
		return chunkedPosition{}, fmt.Errorf("invalid chunked manifest position")
	}
	return chunkedPosition{offset: values[0], compressedLength: values[1], uncompressedLength: values[2]}, nil
}

func parseSHA256Digest(value string) (string, error) {
	digest, found := strings.CutPrefix(value, "sha256:")
	if !found || len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("invalid sha256 digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("invalid sha256 digest")
	}
	return digest, nil
}

func decodeChunkedManifest(compressed []byte, expectedLength int64) (chunkedTOC, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(compressed), zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(maximumChunkedManifest))
	if err != nil {
		return chunkedTOC{}, fmt.Errorf("decode chunked manifest: %w", err)
	}
	defer decoder.Close()
	data, err := io.ReadAll(io.LimitReader(decoder, expectedLength+1))
	if err != nil {
		return chunkedTOC{}, fmt.Errorf("decode chunked manifest: %w", err)
	}
	if int64(len(data)) != expectedLength {
		return chunkedTOC{}, fmt.Errorf("chunked manifest size mismatch")
	}
	var manifest chunkedTOC
	if err = json.Unmarshal(data, &manifest); err != nil {
		return chunkedTOC{}, fmt.Errorf("decode chunked manifest: %w", err)
	}
	if manifest.Version != 1 || len(manifest.Entries) > 1_000_000 {
		return chunkedTOC{}, fmt.Errorf("unsupported chunked manifest")
	}
	return manifest, nil
}

func applyChunkedTOC(ctx context.Context, client *oci.Client, ref oci.Reference, layer oci.Descriptor, manifest chunkedTOC, writer *fvsrepo.SnapshotWriter, known map[string]fvsrepo.FileEntry) error {
	for index := 0; index < len(manifest.Entries); index++ {
		entry := manifest.Entries[index]
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type == "chunk" {
			return fmt.Errorf("unpack chunked layer: orphan chunk entry")
		}
		name, err := layerEntryPath(entry.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		snapshotEntry := fvsrepo.Entry{Path: name, Mode: uint32(entry.Mode & 0o7777)}
		switch entry.Type {
		case "dir":
			snapshotEntry.Kind = fvsrepo.EntryDir
			err = writer.Add(snapshotEntry, nil)
		case "reg":
			var segments []chunkedEntry
			if entry.Size > 0 {
				var next int
				var segmentErr error
				segments, next, segmentErr = chunkedFileSegments(manifest.Entries, index, layer.Size)
				if segmentErr != nil {
					return segmentErr
				}
				index = next - 1
			}
			if err = applyChunkedFile(ctx, client, ref, layer, entry, segments, snapshotEntry, writer, known); err != nil {
				return err
			}
		case "symlink":
			snapshotEntry.Kind = fvsrepo.EntrySymlink
			snapshotEntry.Link = entry.Linkname
			err = writer.Add(snapshotEntry, nil)
		case "hardlink":
			target, targetErr := layerEntryPath(entry.Linkname)
			if targetErr != nil || target == "" {
				return fmt.Errorf("unpack chunked layer: invalid hardlink target %q", entry.Linkname)
			}
			snapshotEntry.Kind = fvsrepo.EntryHardlink
			snapshotEntry.Link = target
			err = writer.Add(snapshotEntry, nil)
		case "fifo":
			snapshotEntry.Kind = fvsrepo.EntryFIFO
			err = writer.Add(snapshotEntry, nil)
		default:
			return fmt.Errorf("unpack chunked layer: unsupported entry type %q", entry.Type)
		}
		if err != nil {
			return fmt.Errorf("unpack chunked layer: import %s: %w", entry.Name, err)
		}
	}
	return nil
}

func chunkedFileSegments(entries []chunkedEntry, index int, layerSize int64) ([]chunkedEntry, int, error) {
	entry := entries[index]
	if entry.Size < 0 || entry.Offset < 0 || entry.EndOffset <= entry.Offset || entry.EndOffset > layerSize {
		return nil, index, fmt.Errorf("unpack chunked layer: invalid file range")
	}
	end := index + 1
	for end < len(entries) && entries[end].Type == "chunk" {
		if entries[end].Name != entry.Name {
			return nil, index, fmt.Errorf("unpack chunked layer: invalid chunk sequence")
		}
		end++
	}
	segments := append([]chunkedEntry(nil), entries[index:end]...)
	if len(segments) == 1 && segments[0].ChunkSize == 0 {
		segments[0].ChunkSize = entry.Size
	}
	var uncompressed int64
	for segmentIndex := range segments {
		segment := &segments[segmentIndex]
		if segment.ChunkSize <= 0 || (segment.ChunkType != "" && segment.ChunkType != "zeros") {
			return nil, index, fmt.Errorf("unpack chunked layer: invalid file chunk")
		}
		if segmentIndex > 0 && segment.Offset <= segments[segmentIndex-1].Offset {
			return nil, index, fmt.Errorf("unpack chunked layer: invalid file chunk")
		}
		segment.EndOffset = entry.EndOffset
		if segmentIndex+1 < len(segments) {
			segment.EndOffset = segments[segmentIndex+1].Offset
		}
		if segment.Offset < entry.Offset || segment.EndOffset <= segment.Offset || segment.EndOffset > entry.EndOffset {
			return nil, index, fmt.Errorf("unpack chunked layer: invalid file chunk")
		}
		if uncompressed > entry.Size-segment.ChunkSize {
			return nil, index, fmt.Errorf("unpack chunked layer: file chunk size mismatch")
		}
		uncompressed += segment.ChunkSize
	}
	if uncompressed != entry.Size {
		return nil, index, fmt.Errorf("unpack chunked layer: file chunk size mismatch")
	}
	return segments, end, nil
}

func applyChunkedFile(ctx context.Context, client *oci.Client, ref oci.Reference, layer oci.Descriptor, entry chunkedEntry, segments []chunkedEntry, snapshotEntry fvsrepo.Entry, writer *fvsrepo.SnapshotWriter, known map[string]fvsrepo.FileEntry) error {
	if entry.Size < 0 {
		return fmt.Errorf("unpack chunked layer: invalid file size")
	}
	snapshotEntry.Kind = fvsrepo.EntryFile
	snapshotEntry.Size = entry.Size
	snapshotEntry.ContentDigest = entry.Digest
	if entry.Size == 0 {
		return writer.Add(snapshotEntry, strings.NewReader(""))
	}
	digest, err := parseSHA256Digest(entry.Digest)
	if err != nil {
		return fmt.Errorf("unpack chunked layer: invalid file digest")
	}
	if reusable, ok := known["sha256:"+digest]; ok && reusable.Size == entry.Size {
		reusable.Path = snapshotEntry.Path
		reusable.Mode = snapshotEntry.Mode
		return writer.AddReference(reusable)
	}
	compressed, err := client.BlobRange(ctx, ref, layer, entry.Offset, entry.EndOffset-entry.Offset)
	if err != nil {
		return err
	}
	defer compressed.Close()
	reader := &chunkedFileReader{source: compressed, segments: segments}
	defer reader.Close()
	return writer.Add(snapshotEntry, reader)
}

func (r *chunkedFileReader) Read(buffer []byte) (int, error) {
	for {
		if r.index >= len(r.segments) {
			return 0, io.EOF
		}
		segment := r.segments[r.index]
		if r.decoder == nil && r.zerosRemaining == 0 {
			compressedSize := segment.EndOffset - segment.Offset
			if segment.ChunkType == "zeros" {
				if _, err := io.CopyN(io.Discard, r.source, compressedSize); err != nil {
					return 0, fmt.Errorf("unpack chunked layer: read zero chunk: %w", err)
				}
				r.zerosRemaining = segment.ChunkSize
			} else {
				r.compressed = &io.LimitedReader{R: r.source, N: compressedSize}
				decoder, err := zstd.NewReader(r.compressed, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
				if err != nil {
					return 0, fmt.Errorf("unpack chunked layer: open file chunk: %w", err)
				}
				r.decoder = decoder
			}
		}
		if r.zerosRemaining > 0 {
			count := min(int64(len(buffer)), r.zerosRemaining)
			clear(buffer[:count])
			r.zerosRemaining -= count
			r.segmentRead += count
			if r.zerosRemaining == 0 {
				r.finishSegment()
			}
			return int(count), nil
		}
		remaining := segment.ChunkSize - r.segmentRead
		if remaining == 0 {
			var extra [1]byte
			if count, err := r.decoder.Read(extra[:]); count != 0 || err != io.EOF {
				if err == nil {
					err = fmt.Errorf("decoded data exceeds chunk size")
				}
				return 0, fmt.Errorf("unpack chunked layer: invalid file chunk: %w", err)
			}
			if err := r.closeDecoder(); err != nil {
				return 0, err
			}
			r.finishSegment()
			continue
		}
		if int64(len(buffer)) > remaining {
			buffer = buffer[:remaining]
		}
		count, err := r.decoder.Read(buffer)
		r.segmentRead += int64(count)
		if err == io.EOF && r.segmentRead != segment.ChunkSize {
			return count, fmt.Errorf("unpack chunked layer: file chunk size mismatch")
		}
		if count > 0 && err == io.EOF {
			err = nil
		}
		return count, err
	}
}

func (r *chunkedFileReader) finishSegment() {
	r.index++
	r.segmentRead = 0
	r.zerosRemaining = 0
}

func (r *chunkedFileReader) closeDecoder() error {
	if r.decoder != nil {
		r.decoder.Close()
		r.decoder = nil
	}
	if r.compressed != nil {
		if _, err := io.Copy(io.Discard, r.compressed); err != nil {
			return fmt.Errorf("unpack chunked layer: finish file chunk: %w", err)
		}
		if r.compressed.N != 0 {
			return fmt.Errorf("unpack chunked layer: compressed chunk size mismatch")
		}
		r.compressed = nil
	}
	return nil
}

func (r *chunkedFileReader) Close() error {
	return r.closeDecoder()
}

func readBlobRange(ctx context.Context, client *oci.Client, ref oci.Reference, descriptor oci.Descriptor, offset, length int64) ([]byte, error) {
	reader, err := client.BlobRange(ctx, ref, descriptor, offset, length)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, length+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != length {
		return nil, fmt.Errorf("oci: blob range size mismatch")
	}
	return data, nil
}
