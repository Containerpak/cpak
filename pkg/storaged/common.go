// Package storaged implements the storage drivers shipped with cpak.
package storaged

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	storage "github.com/containerpak/storage/pkg/driver"
)

const driverVersion = "1"

const partialCheckoutGrace = 10 * time.Minute

type roots struct {
	source string
	driver string
}

func newRoots(source, driver string) (roots, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return roots{}, err
	}
	driver, err = filepath.Abs(driver)
	if err != nil {
		return roots{}, err
	}
	if source == driver {
		return roots{}, errors.New("storaged: source and driver roots must differ")
	}
	if err := os.MkdirAll(filepath.Join(driver, "layers"), 0o755); err != nil {
		return roots{}, err
	}
	return roots{source: source, driver: driver}, nil
}

func (r roots) repository(layer string) string {
	return filepath.Join(r.source, "layers", layer)
}

func (r roots) checkout(layer string) string {
	return filepath.Join(r.driver, "layers", layer)
}

func validateRequestLayers(layers []string) error {
	return storage.ValidateLayerIDs(layers)
}

func collectDriverGarbage(ctx context.Context, root string, live []string, apply bool) (storage.GCResult, error) {
	if len(live) > 0 {
		if err := storage.ValidateLayerIDs(live); err != nil {
			return storage.GCResult{}, err
		}
	}
	kept := make(map[string]struct{}, len(live))
	for _, layer := range live {
		kept[layer] = struct{}{}
	}
	entries, err := os.ReadDir(filepath.Join(root, "layers"))
	if errors.Is(err, os.ErrNotExist) {
		return storage.GCResult{}, nil
	}
	if err != nil {
		return storage.GCResult{}, err
	}
	result := storage.GCResult{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !entry.IsDir() {
			continue
		}
		if strings.Contains(entry.Name(), ".partial-") {
			info, err := entry.Info()
			if err != nil {
				return result, err
			}
			if time.Since(info.ModTime()) < partialCheckoutGrace {
				continue
			}
		}
		if _, exists := kept[entry.Name()]; exists {
			continue
		}
		path := filepath.Join(root, "layers", entry.Name())
		bytes, err := treeSize(path)
		if err != nil {
			return result, err
		}
		result.Layers = append(result.Layers, entry.Name())
		result.Bytes += bytes
		if apply {
			if err := os.RemoveAll(path); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func treeSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		size += info.Size()
		return nil
	})
	return size, err
}
