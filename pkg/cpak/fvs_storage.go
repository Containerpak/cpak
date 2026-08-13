/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/types"
	dabadee "github.com/mirkobrombin/dabadee/v2/pkg/store"
	"golang.org/x/sys/unix"
)

const storageMigrationReserve = 256 << 20

type StorageMigrationProgress struct {
	Layer       int
	Layers      int
	LayerBytes  int64
	LayerTotal  int64
	Bytes       int64
	TotalBytes  int64
	LayerDigest string
}

type StorageMigrationHandler func(func(func(StorageMigrationProgress)) error) error

func (c *Cpak) SetStorageMigrationHandler(handler StorageMigrationHandler) {
	c.storageMigration = handler
}

func (c *Cpak) fvsRoot() string {
	return c.GetInStoreDir("fvs")
}

func (c *Cpak) fvsBlocksPath() string {
	return filepath.Join(c.fvsRoot(), "blocks")
}

func (c *Cpak) fvsLayersPath() string {
	return filepath.Join(c.fvsRoot(), "layers")
}

func (c *Cpak) fvsLayerPath(digest string) string {
	return filepath.Join(c.fvsLayersPath(), digest)
}

func (c *Cpak) beginFVSLayerSnapshot(digest string, options fvsrepo.SnapshotOptions) (string, *fvsrepo.SnapshotWriter, error) {
	layers, err := c.GetInStoreDirMkdir("fvs", "layers")
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(c.fvsBlocksPath(), 0o755); err != nil {
		return "", nil, err
	}
	temporary, err := os.MkdirTemp(layers, digest+".partial-")
	if err != nil {
		return "", nil, err
	}
	if _, err := fvsrepo.InitWithOptions(temporary, fvsrepo.InitOptions{BlocksPath: c.fvsBlocksPath()}); err != nil {
		os.RemoveAll(temporary)
		return "", nil, err
	}
	writer, err := fvsrepo.BeginSnapshotContext(c.Ctx, temporary, options)
	if err != nil {
		os.RemoveAll(temporary)
		return "", nil, err
	}
	return temporary, writer, nil
}

func (c *Cpak) fvsLayerAvailable(digest string) (bool, error) {
	states, err := fvsrepo.States(c.fvsLayerPath(digest))
	if errors.Is(err, fvsrepo.ErrNotInitialized) || os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(states) > 0, nil
}

func (c *Cpak) storedLayerAvailable(digest string) (bool, error) {
	available, err := c.fvsLayerAvailable(digest)
	if err != nil || available {
		return available, err
	}
	info, err := os.Stat(c.GetInStoreDir("layers", digest))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (c *Cpak) fvsContentIndex() (map[string]fvsrepo.FileEntry, error) {
	index := make(map[string]fvsrepo.FileEntry)
	entries, err := os.ReadDir(c.fvsLayersPath())
	if os.IsNotExist(err) {
		return index, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.Contains(entry.Name(), ".partial") {
			continue
		}
		repository := c.fvsLayerPath(entry.Name())
		states, err := fvsrepo.States(repository)
		if err != nil || len(states) == 0 {
			if errors.Is(err, fvsrepo.ErrNotInitialized) {
				continue
			}
			return nil, err
		}
		files, err := fvsrepo.StateFiles(repository, states[0].ID)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.ContentDigest != "" && (file.Kind == "" || file.Kind == string(fvsrepo.EntryFile)) {
				index[file.ContentDigest] = file
			}
		}
	}
	return index, nil
}

func (c *Cpak) fvsRepositories() ([]string, error) {
	entries, err := os.ReadDir(c.fvsLayersPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	repositories := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && !strings.Contains(entry.Name(), ".partial") {
			repositories = append(repositories, c.fvsLayerPath(entry.Name()))
		}
	}
	return repositories, nil
}

func (c *Cpak) removeApplicationLayers(removed types.Application) error {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	apps, err := store.GetApplications()
	closeErr := store.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	history, err := c.rollbackHistoryApplications()
	if err != nil {
		return err
	}
	apps = append(apps, history...)
	referenced := make(map[string]bool)
	for _, app := range apps {
		for _, layer := range app.ParsedLayers {
			referenced[layer] = true
		}
	}
	seen := make(map[string]bool)
	for _, layer := range removed.ParsedLayers {
		if seen[layer] || referenced[layer] {
			continue
		}
		seen[layer] = true
		for _, path := range []string{c.fvsLayerPath(layer), c.GetInStoreDir("layers", layer)} {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove layer %s: %w", layer, err)
			}
		}
	}
	return nil
}

func (c *Cpak) ensureFVSLayer(digest string) (bool, error) {
	return c.ensureFVSLayerProgress(digest, nil)
}

func (c *Cpak) ensureFVSLayerProgress(digest string, progress func(int64)) (bool, error) {
	available, err := c.fvsLayerAvailable(digest)
	if err != nil {
		return false, err
	}
	legacy := c.GetInStoreDir("layers", digest)
	if available {
		if err := c.removeLegacyLayer(legacy); err != nil {
			return false, err
		}
		return true, nil
	}
	info, err := os.Stat(legacy)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("legacy layer %s is not a directory", digest)
	}
	size, err := legacyLayerSize(legacy)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(c.fvsRoot(), 0o755); err != nil {
		return false, err
	}
	if err := ensureMigrationSpace(c.fvsRoot(), size); err != nil {
		return false, fmt.Errorf("migrate layer %s: %w", digest, err)
	}
	if err := c.migrateLegacyLayer(digest, legacy, progress); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Cpak) migrateLegacyLayer(digest, legacy string, progress func(int64)) error {
	temporary, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{
		Message:       "migrate " + digest,
		ComputeSHA256: true,
	})
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := writer.AddTreeProgress(legacy, progress); err != nil {
		_ = writer.Abort()
		return fmt.Errorf("migrate layer %s: %w", digest, err)
	}
	result, err := writer.Commit()
	if err != nil {
		return fmt.Errorf("commit migrated layer %s: %w", digest, err)
	}
	if err := verifyMigratedLayer(legacy, temporary, result.StateID); err != nil {
		return fmt.Errorf("verify migrated layer %s: %w", digest, err)
	}
	if err := publishFVSLayer(temporary, c.fvsLayerPath(digest)); err != nil {
		return err
	}
	return c.removeLegacyLayer(legacy)
}

func (c *Cpak) ensureFVSLayers(layers []string) error {
	pending, sizes, total, err := c.pendingLegacyLayers(layers)
	if err != nil {
		return err
	}
	run := func(progress func(StorageMigrationProgress)) error {
		if err := os.MkdirAll(c.fvsRoot(), 0o755); err != nil {
			return err
		}
		completed := int64(0)
		index := 0
		migrated := make(map[string]bool)
		for _, layer := range layers {
			layerTotal, migrates := sizes[layer]
			if !migrates || migrated[layer] {
				available, layerErr := c.ensureFVSLayer(layer)
				if layerErr != nil {
					return layerErr
				}
				if !available {
					return fmt.Errorf("layer %s is not available", layer)
				}
				continue
			}
			index++
			if err := ensureMigrationSpace(c.fvsRoot(), layerTotal); err != nil {
				return fmt.Errorf("migrate layer %s: %w", layer, err)
			}
			base := completed
			report := func(layerBytes int64) {
				if progress == nil {
					return
				}
				progress(StorageMigrationProgress{
					Layer: index, Layers: len(pending), LayerBytes: layerBytes,
					LayerTotal: layerTotal, Bytes: base + layerBytes,
					TotalBytes: total, LayerDigest: layer,
				})
			}
			report(0)
			available, layerErr := c.ensureFVSLayerProgress(layer, report)
			if layerErr != nil {
				return layerErr
			}
			if !available {
				return fmt.Errorf("layer %s is not available", layer)
			}
			completed = base + layerTotal
			migrated[layer] = true
			report(layerTotal)
		}
		return nil
	}
	if len(pending) > 0 && c.storageMigration != nil {
		return c.storageMigration(run)
	}
	return run(nil)
}

func ensureMigrationSpace(path string, required int64) error {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return err
	}
	available := int64(stats.Bavail) * int64(stats.Bsize)
	if required > available-storageMigrationReserve {
		return fmt.Errorf("not enough free space: %d bytes required, %d bytes available", required+storageMigrationReserve, available)
	}
	return nil
}

func (c *Cpak) pendingLegacyLayers(layers []string) ([]string, map[string]int64, int64, error) {
	pending := make([]string, 0, len(layers))
	sizes := make(map[string]int64)
	seen := make(map[string]bool)
	var total int64
	for _, layer := range layers {
		if seen[layer] {
			continue
		}
		seen[layer] = true
		available, err := c.fvsLayerAvailable(layer)
		if err != nil || available {
			if err != nil {
				return nil, nil, 0, err
			}
			continue
		}
		legacy := c.GetInStoreDir("layers", layer)
		info, err := os.Stat(legacy)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, 0, err
		}
		if !info.IsDir() {
			return nil, nil, 0, fmt.Errorf("legacy layer %s is not a directory", layer)
		}
		size, err := legacyLayerSize(legacy)
		if err != nil {
			return nil, nil, 0, err
		}
		pending = append(pending, layer)
		sizes[layer] = size
		total += size
	}
	return pending, sizes, total, nil
}

func legacyLayerSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func publishFVSLayer(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	} else if states, statErr := fvsrepo.States(target); statErr != nil || len(states) == 0 {
		return err
	}
	return os.RemoveAll(source)
}

func verifyMigratedLayer(source, repository, state string) error {
	entries, err := fvsrepo.StateFiles(repository, state)
	if err != nil {
		return err
	}
	stored := make(map[string]fvsrepo.FileEntry, len(entries))
	for _, entry := range entries {
		stored[entry.Path] = entry
	}
	seen := 0
	err = filepath.WalkDir(source, func(name string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil || relative == "." {
			return err
		}
		if relative == ".fvs2" || strings.HasPrefix(relative, ".fvs2"+string(os.PathSeparator)) {
			if dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		entry, ok := stored[filepath.ToSlash(relative)]
		if !ok {
			return fmt.Errorf("entry %s is missing", relative)
		}
		seen++
		mode := uint32(info.Mode().Perm())
		if info.Mode()&os.ModeSetuid != 0 {
			mode |= 0o4000
		}
		if info.Mode()&os.ModeSetgid != 0 {
			mode |= 0o2000
		}
		if info.Mode()&os.ModeSticky != 0 {
			mode |= 0o1000
		}
		if entry.Mode != mode {
			return fmt.Errorf("entry %s mode changed", relative)
		}
		switch {
		case info.IsDir() && entry.Kind != string(fvsrepo.EntryDir):
			return fmt.Errorf("entry %s is no longer a directory", relative)
		case info.Mode().IsRegular() && entry.Size != info.Size():
			return fmt.Errorf("entry %s size changed", relative)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			if entry.Kind != string(fvsrepo.EntrySymlink) || entry.Link != target {
				return fmt.Errorf("entry %s symlink changed", relative)
			}
		case info.Mode()&os.ModeNamedPipe != 0 && entry.Kind != string(fvsrepo.EntryFIFO):
			return fmt.Errorf("entry %s is no longer a fifo", relative)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(stored) {
		return fmt.Errorf("state contains %d entries, source contains %d", len(stored), seen)
	}
	return nil
}

func (c *Cpak) removeLegacyLayer(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	options := c.daBaDeeStoreOptions()
	if _, err := os.Stat(options.Root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	legacyStore, err := dabadee.Open(options)
	if err != nil {
		return err
	}
	defer legacyStore.Close()
	_, err = legacyStore.GC(c.Ctx)
	return err
}
