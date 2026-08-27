/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func TarUnpack(srcPath, dstPath string) error {
	source, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer source.Close()
	reader := bufio.NewReader(source)
	archiveReader, closeReader, err := decodedTarReader(reader)
	if err != nil {
		return err
	}
	defer closeReader()

	root, err := os.OpenRoot(dstPath)
	if err != nil {
		return err
	}
	defer root.Close()

	archive := tar.NewReader(archiveReader)
	for {
		header, readErr := archive.Next()
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read tar archive: %w", readErr)
		}
		name, pathErr := safeArchivePath(header.Name)
		if pathErr != nil {
			return pathErr
		}
		if name == "" || name == "dev" || strings.HasPrefix(name, "dev/") {
			continue
		}
		if err = unpackTarEntry(root, archive, header, name); err != nil {
			return fmt.Errorf("extract %s: %w", header.Name, err)
		}
	}
}

func decodedTarReader(reader *bufio.Reader) (io.Reader, func() error, error) {
	magic, err := reader.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		decoded, gzipErr := gzip.NewReader(reader)
		if gzipErr != nil {
			return nil, nil, gzipErr
		}
		return decoded, decoded.Close, nil
	}
	return reader, func() error { return nil }, nil
}

func safeArchivePath(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "", nil
	}
	if strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("archive path escapes destination: %q", value)
	}
	return filepath.FromSlash(cleaned), nil
}

func unpackTarEntry(root *os.Root, reader io.Reader, header *tar.Header, name string) error {
	mode := os.FileMode(header.Mode & 0o777)
	switch header.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(name, mode); err != nil {
			return err
		}
		return root.Chmod(name, mode)
	case tar.TypeReg, tar.TypeRegA:
		if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		if info, err := root.Lstat(name); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if err = root.Remove(name); err != nil {
				return err
			}
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return root.Chmod(name, mode)
	case tar.TypeSymlink:
		target := strings.ReplaceAll(header.Linkname, "\\", "/")
		if strings.HasPrefix(target, "/") {
			return fmt.Errorf("absolute symbolic link target: %q", header.Linkname)
		}
		resolved, err := safeArchivePath(path.Join(path.Dir(filepath.ToSlash(name)), target))
		if err != nil || resolved == "" {
			return fmt.Errorf("symbolic link escapes destination: %q", header.Linkname)
		}
		if err = root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		if _, err = root.Lstat(name); err == nil {
			if err = root.Remove(name); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		return root.Symlink(header.Linkname, name)
	case tar.TypeLink:
		target, err := safeArchivePath(header.Linkname)
		if err != nil || target == "" {
			return fmt.Errorf("invalid hard link target: %q", header.Linkname)
		}
		info, err := root.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("hard link target is not a regular file: %q", header.Linkname)
		}
		if err = root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		if _, err = root.Lstat(name); err == nil {
			if err = root.Remove(name); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		return root.Link(target, name)
	case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
		return nil
	default:
		return fmt.Errorf("unsupported tar entry type %d", header.Typeflag)
	}
}
