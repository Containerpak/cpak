package cpak

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

type GarbageItem struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type GarbageReport struct {
	Applied        bool          `json:"applied"`
	Layers         []GarbageItem `json:"layers"`
	Cache          []GarbageItem `json:"cache"`
	Objects        int           `json:"objects"`
	Chunks         int           `json:"chunks"`
	ObjectBytes    int64         `json:"object_bytes"`
	FVSBlocks      int           `json:"fvs_blocks"`
	FVSObjects     int           `json:"fvs_objects"`
	FVSObjectBytes int64         `json:"fvs_object_bytes"`
	LegacyObjects  int           `json:"legacy_objects"`
	LegacyChunks   int           `json:"legacy_chunks"`
	LegacyBytes    int64         `json:"legacy_bytes"`
	DriverLayers   int           `json:"driver_layers"`
	DriverBytes    int64         `json:"driver_bytes"`
	Bytes          int64         `json:"bytes"`
}

func (c *Cpak) CollectGarbage(apply bool) (GarbageReport, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return GarbageReport{}, err
	}
	apps, err := store.GetApplications()
	closeErr := store.Close()
	if err != nil {
		return GarbageReport{}, err
	}
	if closeErr != nil {
		return GarbageReport{}, closeErr
	}
	return c.collectGarbageReport(apps, apply)
}

func (c *Cpak) collectGarbage(apps []types.Application, repair bool) error {
	report, err := c.collectGarbageReport(apps, repair)
	if err == nil && !repair {
		for _, item := range report.Layers {
			logger.Printf("Layer %s is not referenced by any installed application.", item.Path)
		}
		for _, item := range report.Cache {
			logger.Printf("Cached layer %s can be removed.", item.Path)
		}
	}
	return err
}

func (c *Cpak) collectGarbageReport(apps []types.Application, apply bool) (GarbageReport, error) {
	report := GarbageReport{Applied: apply, Layers: []GarbageItem{}, Cache: []GarbageItem{}}
	history, err := c.rollbackHistoryApplications()
	if err != nil {
		return report, err
	}
	installedLayers, err := c.installedStorageLayersFrom(apps)
	if err != nil {
		return report, err
	}
	apps = append(apps, history...)
	referencedLayers := map[string]struct{}{}
	for _, app := range apps {
		for _, layer := range app.ParsedLayers {
			referencedLayers[layer] = struct{}{}
		}
	}
	for _, layer := range installedLayers {
		referencedLayers[layer] = struct{}{}
	}

	report.Layers, err = c.collectOrphanedLayers(referencedLayers, apply)
	if err != nil {
		return report, err
	}
	report.Cache, err = c.collectCachedLayers(apply)
	if err != nil {
		return report, err
	}
	repositories, err := c.fvsRepositories()
	if err != nil {
		return report, err
	}
	result, err := fvsrepo.GCShared(c.Ctx, c.fvsBlocksPath(), repositories, fvsrepo.GCOptions{DryRun: !apply})
	if err != nil {
		return report, err
	}
	report.FVSBlocks = result.RemovedBlocks
	report.FVSObjects = result.RemovedObjects
	report.FVSObjectBytes = result.RemovedObjectBytes
	report.Objects = result.RemovedBlocks + result.RemovedObjects
	report.ObjectBytes = result.RemovedBytes + result.RemovedObjectBytes
	driverResult, err := c.collectStorageDriverGarbage(referencedLayers, apply)
	if err != nil {
		return report, err
	}
	report.DriverLayers = len(driverResult.Layers)
	report.DriverBytes = driverResult.Bytes
	legacyOptions := c.daBaDeeStoreOptions()
	if _, statErr := os.Stat(legacyOptions.Root); statErr == nil {
		dedupStore, openErr := store.Open(legacyOptions)
		if openErr != nil {
			return report, openErr
		}
		var legacyResult store.GCResult
		if apply {
			legacyResult, err = dedupStore.GC(c.Ctx)
		} else {
			released := make([]string, 0, len(report.Layers))
			for _, item := range report.Layers {
				released = append(released, item.Path)
			}
			legacyResult, err = dedupStore.PlanGC(c.Ctx, released)
		}
		closeErr := dedupStore.Close()
		if err != nil {
			return report, err
		}
		if closeErr != nil {
			return report, closeErr
		}
		report.Objects += legacyResult.Objects
		report.Chunks += legacyResult.Chunks
		report.ObjectBytes += legacyResult.Bytes
		report.LegacyObjects = legacyResult.Objects
		report.LegacyChunks = legacyResult.Chunks
		report.LegacyBytes = legacyResult.Bytes
	}
	for _, item := range append(append([]GarbageItem{}, report.Layers...), report.Cache...) {
		report.Bytes += item.Bytes
	}
	report.Bytes += report.ObjectBytes
	report.Bytes += report.DriverBytes
	return report, nil
}

func (c *Cpak) collectOrphanedLayers(referenced map[string]struct{}, apply bool) ([]GarbageItem, error) {
	items := []GarbageItem{}
	for _, layersDir := range []string{c.fvsLayersPath(), c.GetInStoreDir("layers")} {
		entries, err := os.ReadDir(layersDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, exists := referenced[entry.Name()]; exists {
				continue
			}
			path := filepath.Join(layersDir, entry.Name())
			item, err := garbageItem(path)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			if apply {
				if err := os.RemoveAll(path); err != nil {
					return nil, fmt.Errorf("remove orphaned layer %s: %w", path, err)
				}
			}
		}
	}
	return items, nil
}

func (c *Cpak) collectCachedLayers(apply bool) ([]GarbageItem, error) {
	items := []GarbageItem{}
	entries, err := os.ReadDir(c.Options.CachePath)
	if os.IsNotExist(err) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join(c.Options.CachePath, entry.Name())
		item, err := garbageItem(path)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if !apply {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return nil, fmt.Errorf("remove cached layer %s: %w", path, err)
		}
	}
	return items, nil
}

func garbageItem(path string) (GarbageItem, error) {
	item := GarbageItem{Path: path}
	err := filepath.WalkDir(path, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item.Bytes += info.Size()
		return nil
	})
	return item, err
}
