/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/klauspost/compress/zstd"
)

const (
	mediaOCILayerTar     = "application/vnd.oci.image.layer.v1.tar"
	mediaOCILayerGzip    = "application/vnd.oci.image.layer.v1.tar+gzip"
	mediaOCILayerZstd    = "application/vnd.oci.image.layer.v1.tar+zstd"
	mediaDockerLayerTar  = "application/vnd.docker.image.rootfs.diff.tar"
	mediaDockerLayerGzip = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

func unpackLayer(ctx context.Context, compressed io.Reader, mediaType string, writer *fvsrepo.SnapshotWriter) error {
	_, err := unpackLayerEntries(ctx, compressed, mediaType, writer, nil)
	return err
}

func unpackLayerPaths(ctx context.Context, compressed io.Reader, mediaType string, writer *fvsrepo.SnapshotWriter, prefixes []string) (int, error) {
	cleaned := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		value, err := layerEntryPath(prefix)
		if err != nil || value == "" {
			return 0, fmt.Errorf("unpack layer: invalid path prefix %q", prefix)
		}
		cleaned = append(cleaned, value)
	}
	return unpackLayerEntries(ctx, compressed, mediaType, writer, cleaned)
}

func layerPathsIncludingLinks(compressed io.Reader, mediaType string, prefixes []string) ([]string, error) {
	reader, closeReader, err := layerReader(compressed, mediaType)
	if err != nil {
		return nil, err
	}
	defer closeReader()

	links := make(map[string]string)
	tarReader := tar.NewReader(reader)
	for {
		header, readErr := tarReader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("inspect layer links: %w", readErr)
		}
		if header.Typeflag != tar.TypeSymlink {
			continue
		}
		name, pathErr := layerEntryPath(header.Name)
		if pathErr != nil || name == "" {
			return nil, fmt.Errorf("inspect layer links: invalid path %q", header.Name)
		}
		target := strings.ReplaceAll(header.Linkname, "\\", "/")
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join(path.Dir(name), target)
		}
		target, pathErr = layerEntryPath(target)
		if pathErr != nil || target == "" {
			return nil, fmt.Errorf("inspect layer links: invalid target %q", header.Linkname)
		}
		links[name] = target
	}

	selected := make(map[string]bool, len(prefixes))
	for _, prefix := range prefixes {
		selected[prefix] = true
	}
	for changed := true; changed; {
		changed = false
		values := make([]string, 0, len(selected))
		for value := range selected {
			values = append(values, value)
		}
		for name, target := range links {
			_, direct := selectedLayerEntry(name, values)
			if direct && !selected[target] {
				selected[target] = true
				changed = true
			}
		}
	}
	result := make([]string, 0, len(selected))
	for value := range selected {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func unpackLayerEntries(ctx context.Context, compressed io.Reader, mediaType string, writer *fvsrepo.SnapshotWriter, prefixes []string) (int, error) {
	reader, closeReader, err := layerReader(compressed, mediaType)
	if err != nil {
		return 0, err
	}
	defer closeReader()

	selected := 0
	tarReader := tar.NewReader(reader)
	for {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		header, readErr := tarReader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, fmt.Errorf("unpack layer: read tar header: %w", readErr)
		}
		name, pathErr := layerEntryPath(header.Name)
		if pathErr != nil {
			return 0, pathErr
		}
		if name == "" {
			continue
		}
		include, direct := selectedLayerEntry(name, prefixes)
		if !include {
			continue
		}
		if direct {
			selected++
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
				return 0, fmt.Errorf("unpack layer: invalid hardlink target %q", header.Linkname)
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
			return 0, fmt.Errorf("unpack layer: unsupported tar entry %q with type %d", header.Name, header.Typeflag)
		}
		if err != nil {
			return 0, fmt.Errorf("unpack layer: import %s: %w", header.Name, err)
		}
	}
	if _, err = io.Copy(io.Discard, reader); err != nil {
		return 0, fmt.Errorf("unpack layer: finish compressed stream: %w", err)
	}
	return selected, nil
}

func selectedLayerEntry(name string, prefixes []string) (include, direct bool) {
	if len(prefixes) == 0 {
		return true, true
	}
	for _, prefix := range prefixes {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true, true
		}
		if strings.HasPrefix(prefix, name+"/") {
			include = true
		}
	}
	return include, false
}

func layerReader(reader io.Reader, mediaType string) (io.Reader, func() error, error) {
	switch mediaType {
	case mediaOCILayerTar, mediaDockerLayerTar, "":
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
	name = strings.TrimLeft(strings.ReplaceAll(name, "\\", "/"), "/")
	clean := path.Clean(name)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unpack layer: path escapes root: %q", name)
	}
	if clean == "dev" || strings.HasPrefix(clean, "dev/") {
		return "", nil
	}
	return clean, nil
}
