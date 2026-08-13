/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/klauspost/compress/zstd"
)

const (
	mediaOCILayerTar     = "application/vnd.oci.image.layer.v1.tar"
	mediaOCILayerGzip    = "application/vnd.oci.image.layer.v1.tar+gzip"
	mediaOCILayerZstd    = "application/vnd.oci.image.layer.v1.tar+zstd"
	mediaDockerLayerGzip = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

func unpackLayer(ctx context.Context, compressed io.Reader, mediaType string, writer *fvsrepo.SnapshotWriter) error {
	reader, closeReader, err := layerReader(compressed, mediaType)
	if err != nil {
		return err
	}
	defer closeReader()

	tarReader := tar.NewReader(reader)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		header, readErr := tarReader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("unpack layer: read tar header: %w", readErr)
		}
		name, pathErr := layerEntryPath(header.Name)
		if pathErr != nil {
			return pathErr
		}
		if name == "" {
			continue
		}
		entry := fvsrepo.Entry{
			Path:    name,
			Mode:    uint32(header.Mode & 0o7777),
			ModTime: header.ModTime,
		}
		switch header.Typeflag {
		case tar.TypeDir:
			entry.Kind = fvsrepo.EntryDir
			err = writer.Add(entry, nil)
		case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
			entry.Kind = fvsrepo.EntryFile
			entry.Size = header.Size
			err = writer.Add(entry, io.LimitReader(tarReader, header.Size))
		case tar.TypeSymlink:
			entry.Kind = fvsrepo.EntrySymlink
			entry.Link = header.Linkname
			err = writer.Add(entry, nil)
		case tar.TypeLink:
			target, targetErr := layerEntryPath(header.Linkname)
			if targetErr != nil || target == "" {
				return fmt.Errorf("unpack layer: invalid hardlink target %q", header.Linkname)
			}
			entry.Kind = fvsrepo.EntryHardlink
			entry.Link = target
			err = writer.Add(entry, nil)
		case tar.TypeFifo:
			entry.Kind = fvsrepo.EntryFIFO
			err = writer.Add(entry, nil)
		case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			continue
		default:
			return fmt.Errorf("unpack layer: unsupported tar entry %q with type %d", header.Name, header.Typeflag)
		}
		if err != nil {
			return fmt.Errorf("unpack layer: import %s: %w", header.Name, err)
		}
	}
	if _, err = io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("unpack layer: finish compressed stream: %w", err)
	}
	return nil
}

func layerReader(reader io.Reader, mediaType string) (io.Reader, func() error, error) {
	switch mediaType {
	case mediaOCILayerTar, "":
		return reader, func() error { return nil }, nil
	case mediaOCILayerGzip, mediaDockerLayerGzip:
		decoded, err := gzip.NewReader(reader)
		if err != nil {
			return nil, nil, fmt.Errorf("unpack layer: open gzip stream: %w", err)
		}
		return decoded, decoded.Close, nil
	case mediaOCILayerZstd:
		decoded, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			return nil, nil, fmt.Errorf("unpack layer: open zstd stream: %w", err)
		}
		return decoded, func() error { decoded.Close(); return nil }, nil
	default:
		return nil, nil, fmt.Errorf("unpack layer: unsupported media type %q", mediaType)
	}
}

func layerEntryPath(name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == "." {
		return "", nil
	}
	if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unpack layer: path escapes root: %q", name)
	}
	if clean == "dev" || strings.HasPrefix(clean, "dev/") {
		return "", nil
	}
	return clean, nil
}
