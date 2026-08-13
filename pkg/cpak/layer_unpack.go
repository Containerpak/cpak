/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

const (
	mediaOCILayerTar     = "application/vnd.oci.image.layer.v1.tar"
	mediaOCILayerGzip    = "application/vnd.oci.image.layer.v1.tar+gzip"
	mediaOCILayerZstd    = "application/vnd.oci.image.layer.v1.tar+zstd"
	mediaDockerLayerGzip = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

type layerDirectory struct {
	path string
	mode fs.FileMode
}

type layerHardlink struct {
	path   string
	target string
}

func unpackLayer(ctx context.Context, compressed io.Reader, mediaType, root string, storage *store.Store) error {
	reader, closeReader, err := layerReader(compressed, mediaType)
	if err != nil {
		return err
	}
	defer closeReader()

	tarReader := tar.NewReader(reader)
	directories := make([]layerDirectory, 0, 64)
	hardlinks := make([]layerHardlink, 0, 16)
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
		path, pathErr := layerPath(root, header.Name)
		if pathErr != nil {
			return pathErr
		}
		if path == "" {
			continue
		}
		mode := tarFileMode(header.Mode)
		switch header.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(path, 0755); err != nil {
				return fmt.Errorf("unpack layer: create directory: %w", err)
			}
			directories = append(directories, layerDirectory{path: path, mode: mode})
		case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
			metadata := store.Metadata{Mode: mode, UID: os.Getuid(), GID: os.Getgid()}
			if _, err = storage.Import(ctx, path, io.LimitReader(tarReader, header.Size), store.ImportOptions{Metadata: &metadata}); err != nil {
				return fmt.Errorf("unpack layer: import %s: %w", header.Name, err)
			}
		case tar.TypeSymlink:
			if err = replaceSymlink(path, header.Linkname); err != nil {
				return fmt.Errorf("unpack layer: create symlink %s: %w", header.Name, err)
			}
		case tar.TypeLink:
			target, targetErr := layerPath(root, header.Linkname)
			if targetErr != nil || target == "" {
				return fmt.Errorf("unpack layer: invalid hardlink target %q", header.Linkname)
			}
			hardlinks = append(hardlinks, layerHardlink{path: path, target: target})
		case tar.TypeFifo:
			if err = replaceFIFO(path, uint32(mode.Perm())); err != nil {
				return fmt.Errorf("unpack layer: create fifo %s: %w", header.Name, err)
			}
		case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			continue
		default:
			return fmt.Errorf("unpack layer: unsupported tar entry %q with type %d", header.Name, header.Typeflag)
		}
	}
	if _, err = io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("unpack layer: finish compressed stream: %w", err)
	}
	for _, link := range hardlinks {
		if err = replaceHardlink(link.target, link.path); err != nil {
			return fmt.Errorf("unpack layer: create hardlink: %w", err)
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err = os.Chmod(directories[index].path, directories[index].mode); err != nil {
			return fmt.Errorf("unpack layer: set directory mode: %w", err)
		}
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

func layerPath(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." {
		return "", nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unpack layer: path escapes root: %q", name)
	}
	if clean == "dev" || strings.HasPrefix(clean, "dev"+string(filepath.Separator)) {
		return "", nil
	}
	target := filepath.Join(root, clean)
	current := root
	parts := strings.Split(filepath.Dir(clean), string(filepath.Separator))
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("unpack layer: inspect path %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("unpack layer: parent is not a directory: %q", name)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("unpack layer: create parent: %w", err)
	}
	return target, nil
}

func tarFileMode(mode int64) fs.FileMode {
	result := fs.FileMode(mode & 0777)
	if mode&04000 != 0 {
		result |= os.ModeSetuid
	}
	if mode&02000 != 0 {
		result |= os.ModeSetgid
	}
	if mode&01000 != 0 {
		result |= os.ModeSticky
	}
	return result
}

func replaceSymlink(path, target string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, path)
}

func replaceHardlink(target, path string) error {
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("target is not a regular file")
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Link(target, path)
}

func replaceFIFO(path string, mode uint32) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syscall.Mkfifo(path, mode)
}
