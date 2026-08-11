package cpak

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func (c *Cpak) collectGarbage(apps []types.Application, repair bool) error {
	referencedLayers := map[string]struct{}{}
	for _, app := range apps {
		for _, layer := range app.ParsedLayers {
			referencedLayers[layer] = struct{}{}
		}
	}

	if err := c.collectOrphanedLayers(referencedLayers, repair); err != nil {
		return err
	}
	return c.collectCachedLayers(repair)
}

func (c *Cpak) collectOrphanedLayers(referenced map[string]struct{}, repair bool) error {
	layersDir := c.GetInStoreDir("layers")
	entries, err := os.ReadDir(layersDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, exists := referenced[entry.Name()]; exists {
			continue
		}
		path := filepath.Join(layersDir, entry.Name())
		if !repair {
			logger.Printf("Layer %s is not referenced by any installed application.", path)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove orphaned layer %s: %w", path, err)
		}
	}
	return nil
}

func (c *Cpak) collectCachedLayers(repair bool) error {
	entries, err := os.ReadDir(c.Options.CachePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(c.Options.CachePath, entry.Name())
		if !repair {
			logger.Printf("Cached layer %s can be removed.", path)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove cached layer %s: %w", path, err)
		}
	}
	return nil
}
