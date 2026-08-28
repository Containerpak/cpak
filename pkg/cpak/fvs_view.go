/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
)

type fvsViewEntry struct {
	file       fvsrepo.FileEntry
	repository string
	state      string
}

const (
	desktopEntrySizeLimit = 256 << 10
	iconSizeLimit         = 8 << 20
)

var errFVSExportLimit = errors.New("FVS export limit exceeded")

type boundedExportBuffer struct {
	bytes.Buffer
	limit int64
}

func (b *boundedExportBuffer) Write(data []byte) (int, error) {
	if int64(b.Len())+int64(len(data)) > b.limit {
		return 0, errFVSExportLimit
	}
	return b.Buffer.Write(data)
}

func (c *Cpak) fvsMergedEntries(layers []string) (map[string]fvsViewEntry, error) {
	result := make(map[string]fvsViewEntry)
	deleteEntry := func(name string, children bool) {
		prefix := name + "/"
		for existing := range result {
			if existing == name || children && strings.HasPrefix(existing, prefix) {
				delete(result, existing)
			}
		}
	}
	for _, layer := range layers {
		available, err := c.ensureFVSLayer(layer)
		if err != nil {
			return nil, err
		}
		if !available {
			return nil, fmt.Errorf("layer %s is not available", layer)
		}
		repository := c.fvsLayerPath(layer)
		states, err := fvsrepo.States(repository)
		if err != nil || len(states) == 0 {
			return nil, fmt.Errorf("read layer %s: %w", layer, err)
		}
		files, err := fvsrepo.StateFiles(repository, states[0].ID)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			clean := strings.Trim(filepath.ToSlash(file.Path), "/")
			base := path.Base(clean)
			if !strings.HasPrefix(base, ".wh.") {
				continue
			}
			directory := strings.Trim(path.Dir(clean), "/")
			if base == ".wh..wh..opq" {
				prefix := directory
				if prefix != "" {
					prefix += "/"
				}
				for existing := range result {
					if strings.HasPrefix(existing, prefix) && existing != directory {
						deleteEntry(existing, true)
					}
				}
				continue
			}
			target := strings.Trim(path.Join(directory, strings.TrimPrefix(base, ".wh.")), "/")
			deleteEntry(target, true)
		}
		for _, file := range files {
			clean := strings.Trim(filepath.ToSlash(file.Path), "/")
			if clean == "" || strings.HasPrefix(path.Base(clean), ".wh.") {
				continue
			}
			file.Path = clean
			result[clean] = fvsViewEntry{file: file, repository: repository, state: states[0].ID}
		}
	}
	return result, nil
}

func fvsViewFileData(ctx context.Context, entries map[string]fvsViewEntry, name string, limit int64) ([]byte, error) {
	for attempts := 0; attempts < 16; attempts++ {
		entry, ok := entries[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		switch entry.file.Kind {
		case "", string(fvsrepo.EntryFile):
			if entry.file.Size < 0 || entry.file.Size > limit {
				return nil, fmt.Errorf("FVS entry %s exceeds the %d byte export limit", name, limit)
			}
			output := boundedExportBuffer{limit: limit}
			if err := fvsrepo.WriteFile(ctx, entry.repository, entry.state, name, &output); err != nil {
				if errors.Is(err, errFVSExportLimit) {
					return nil, fmt.Errorf("FVS entry %s exceeds the %d byte export limit", name, limit)
				}
				return nil, err
			}
			return output.Bytes(), nil
		case string(fvsrepo.EntryHardlink):
			name = strings.Trim(entry.file.Link, "/")
		case string(fvsrepo.EntrySymlink):
			target := entry.file.Link
			if path.IsAbs(target) {
				name = strings.Trim(path.Clean(target), "/")
			} else {
				name = strings.Trim(path.Clean(path.Join(path.Dir(name), target)), "/")
			}
		default:
			return nil, fmt.Errorf("FVS entry %s is not a file", name)
		}
	}
	return nil, fmt.Errorf("FVS link cycle at %s", name)
}
