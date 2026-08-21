package cpak

import (
	"errors"
	"os"
	"sort"

	storage "github.com/containerpak/storage/pkg/driver"
	storageindex "github.com/containerpak/storage/pkg/index"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type StorageStatus struct {
	Driver   string `json:"driver"`
	Apps     int    `json:"apps"`
	Layers   int    `json:"layers"`
	Prepared int    `json:"prepared"`
	Missing  int    `json:"missing"`
}

func (c *Cpak) PrepareInstalledStorage() (StorageStatus, error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return StorageStatus{}, err
	}
	layers, err := c.installedStorageLayersFrom(apps)
	if err != nil {
		return StorageStatus{}, err
	}
	name, err := c.storageDriverName()
	if err != nil {
		return StorageStatus{}, err
	}
	missing, err := c.missingStorageLayers(name, layers)
	if err != nil {
		return StorageStatus{}, err
	}
	if len(missing) > 0 {
		if _, err := c.prepareStorageDriver(missing); err != nil {
			return StorageStatus{Driver: name, Apps: len(apps), Layers: len(layers), Missing: len(missing)}, err
		}
	}
	return c.StorageStatus()
}

func (c *Cpak) VerifyPreparedStorage(repair bool) (storage.VerifyResult, error) {
	layers, err := c.installedStorageLayers()
	if err != nil {
		return storage.VerifyResult{}, err
	}
	if len(layers) == 0 {
		return storage.VerifyResult{}, nil
	}
	name, err := c.storageDriverName()
	if err != nil {
		return storage.VerifyResult{}, err
	}
	var result storage.VerifyResult
	err = c.withStorageDriver(name, func(handler storage.Handler) error {
		var verifyErr error
		result, verifyErr = handler.Verify(c.Ctx, storage.VerifyRequest{Layers: layers, Repair: repair})
		return verifyErr
	})
	if err != nil {
		return result, err
	}
	if repair {
		apps, appErr := c.GetInstalledApps()
		if appErr != nil {
			return result, appErr
		}
		for _, app := range apps {
			if err := c.PrepareApplicationStorage(app); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func (c *Cpak) StorageStatus() (StorageStatus, error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return StorageStatus{}, err
	}
	layers, err := c.installedStorageLayersFrom(apps)
	if err != nil {
		return StorageStatus{}, err
	}
	name, err := c.storageDriverName()
	if err != nil {
		return StorageStatus{}, err
	}
	status := StorageStatus{Driver: name, Apps: len(apps), Layers: len(layers)}
	missing, err := c.missingStorageLayers(name, layers)
	if err != nil {
		return StorageStatus{}, err
	}
	status.Prepared = len(layers) - len(missing)
	status.Missing = len(missing)
	return status, nil
}

func (c *Cpak) missingStorageLayers(name string, layers []string) ([]string, error) {
	index, err := storageindex.Load(c.storageDriverIndex(name))
	if errors.Is(err, os.ErrNotExist) {
		return append([]string(nil), layers...), nil
	}
	if err != nil {
		return nil, err
	}
	if index.Driver != name {
		return append([]string(nil), layers...), nil
	}
	missing := make([]string, 0)
	for _, layer := range layers {
		path, exists := index.Layers[layer]
		valid := false
		if exists {
			_, validateErr := storage.ValidateDriverPath(c.storageDriverRoot(name), path)
			valid = validateErr == nil
		}
		if !valid {
			missing = append(missing, layer)
		}
	}
	return missing, nil
}

func (c *Cpak) installedStorageLayers() ([]string, error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return nil, err
	}
	return c.installedStorageLayersFrom(apps)
}

func (c *Cpak) installedStorageLayersFrom(apps []types.Application) ([]string, error) {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, app := range apps {
		layers := app.ParsedLayers
		if app.Origin == "" {
			for _, layer := range layers {
				if _, exists := seen[layer]; !exists {
					seen[layer] = struct{}{}
					result = append(result, layer)
				}
			}
			continue
		}
		store, err := NewStore(c.Options.StorePath)
		if err != nil {
			return nil, err
		}
		components, err := c.resolveLayerDependenciesFromStore(app, store)
		if err == nil {
			var addons []types.Application
			addons, err = c.resolveEnabledAddonsFromStore(app, store)
			if err == nil {
				layers = composedLayers(app, components, addons)
				for _, layer := range layers {
					if _, exists := seen[layer]; !exists {
						seen[layer] = struct{}{}
						result = append(result, layer)
					}
				}
			}
		}
		closeErr := store.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if len(result) == 0 && len(apps) > 0 {
		return nil, errors.New("installed applications have no storage layers")
	}
	return result, nil
}

func (c *Cpak) removePreparedLayers(layers []string) error {
	if len(layers) == 0 {
		return nil
	}
	name, err := c.storageDriverName()
	if err != nil {
		return err
	}
	index, err := storageindex.Load(c.storageDriverIndex(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	prepared := make([]string, 0, len(layers))
	for _, layer := range layers {
		if _, exists := index.Layers[layer]; exists {
			prepared = append(prepared, layer)
		}
	}
	if len(prepared) == 0 {
		return nil
	}
	err = c.withStorageDriver(name, func(handler storage.Handler) error {
		_, removeErr := handler.Remove(c.Ctx, storage.RemoveRequest{Layers: prepared})
		return removeErr
	})
	if errors.Is(err, errStorageServiceMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	index.Remove(prepared)
	return storageindex.Write(c.storageDriverIndex(name), index)
}

func (c *Cpak) collectStorageDriverGarbage(live map[string]struct{}, apply bool) (storage.GCResult, error) {
	name, err := c.storageDriverName()
	if err != nil {
		return storage.GCResult{}, err
	}
	index, err := storageindex.Load(c.storageDriverIndex(name))
	if errors.Is(err, os.ErrNotExist) {
		return storage.GCResult{}, nil
	}
	if err != nil {
		return storage.GCResult{}, err
	}
	layers := make([]string, 0, len(live))
	for layer := range live {
		layers = append(layers, layer)
	}
	sort.Strings(layers)
	var result storage.GCResult
	err = c.withStorageDriver(name, func(handler storage.Handler) error {
		var gcErr error
		result, gcErr = handler.GC(c.Ctx, storage.GCRequest{LiveLayers: layers, Apply: apply})
		return gcErr
	})
	if err != nil {
		return result, err
	}
	if apply && len(result.Layers) > 0 {
		index.Remove(result.Layers)
		if err := storageindex.Write(c.storageDriverIndex(name), index); err != nil {
			return result, err
		}
	}
	return result, nil
}
