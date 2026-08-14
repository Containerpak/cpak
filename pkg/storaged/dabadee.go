package storaged

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	storage "github.com/containerpak/storage/pkg/driver"
	fvsrepo "github.com/fvs-lab/fvs2/repo"
	dabadee "github.com/mirkobrombin/dabadee/v2/pkg/store"
)

const dabadeeReady = ".dabadee-ready"

// DaBaDee exposes DaBaDee-backed layer checkouts.
type DaBaDee struct {
	roots roots
}

// NewDaBaDee creates the compatibility storage driver.
func NewDaBaDee(sourceRoot, driverRoot string) (*DaBaDee, error) {
	roots, err := newRoots(sourceRoot, driverRoot)
	if err != nil {
		return nil, err
	}
	return &DaBaDee{roots: roots}, nil
}

func (d *DaBaDee) Probe(context.Context) (storage.Info, error) {
	return storage.Info{
		Name: "dabadee", Version: driverVersion, Protocol: storage.ProtocolVersion,
		Capabilities: []string{storage.MethodPrepare, storage.MethodRemove, storage.MethodGC, storage.MethodVerify},
	}, nil
}

func (d *DaBaDee) Prepare(ctx context.Context, request storage.PrepareRequest) (storage.PrepareResult, error) {
	if err := validateRequestLayers(request.Layers); err != nil {
		return storage.PrepareResult{}, err
	}
	result := storage.PrepareResult{LowerDirs: make([]string, 0, len(request.Layers))}
	for index := len(request.Layers) - 1; index >= 0; index-- {
		root, err := d.prepareLayer(ctx, request.Layers[index], request, false)
		if err != nil {
			return storage.PrepareResult{}, err
		}
		result.LowerDirs = append(result.LowerDirs, root)
	}
	return result, nil
}

func (d *DaBaDee) prepareLayer(ctx context.Context, layer string, request storage.PrepareRequest, replace bool) (string, error) {
	destination := d.roots.checkout(layer)
	ready := filepath.Join(destination, dabadeeReady)
	if _, err := os.Stat(ready); err == nil && !replace {
		return storage.ValidateDriverPath(d.roots.driver, filepath.Join(destination, "rootfs"))
	} else if request.ExistingOnly {
		return "", fvsrepo.ErrCheckoutMissing
	}
	states, err := fvsrepo.States(d.roots.repository(layer))
	if err != nil {
		return "", err
	}
	if len(states) == 0 {
		return "", fmt.Errorf("storaged: layer %s has no state", layer)
	}
	checkout, err := fvsrepo.CheckoutContext(ctx, d.roots.repository(layer), states[0].ID, fvsrepo.CheckoutOptions{
		To: destination, ClearPrivilegedBits: request.ClearPrivilegedBits,
		OverlayWhiteouts: request.OverlayWhiteouts, PruneLooseBlocks: request.PruneLooseBlocks,
		ReplaceExisting: replace,
	})
	if err != nil {
		return "", err
	}
	store, err := dabadee.Open(dabadee.Options{
		Root: filepath.Join(d.roots.driver, "objects"), PreserveMetadata: true,
	})
	if err != nil {
		return "", err
	}
	_, dedupErr := store.DeduplicateTree(ctx, checkout.Root)
	closeErr := store.Close()
	if dedupErr != nil {
		return "", dedupErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.WriteFile(ready, []byte(states[0].ID+"\n"), 0o644); err != nil {
		return "", err
	}
	return storage.ValidateDriverPath(d.roots.driver, checkout.Root)
}

func (d *DaBaDee) Remove(ctx context.Context, request storage.RemoveRequest) (storage.RemoveResult, error) {
	if err := validateRequestLayers(request.Layers); err != nil {
		return storage.RemoveResult{}, err
	}
	result := storage.RemoveResult{}
	for _, layer := range request.Layers {
		path := d.roots.checkout(layer)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return result, err
		}
		if err := os.RemoveAll(path); err != nil {
			return result, err
		}
		result.Removed++
	}
	store, err := dabadee.Open(dabadee.Options{Root: filepath.Join(d.roots.driver, "objects"), PreserveMetadata: true})
	if err != nil {
		return result, err
	}
	_, gcErr := store.GC(ctx)
	closeErr := store.Close()
	return result, errors.Join(gcErr, closeErr)
}

func (d *DaBaDee) GC(ctx context.Context, request storage.GCRequest) (storage.GCResult, error) {
	result, err := collectDriverGarbage(ctx, d.roots.driver, request.LiveLayers, request.Apply)
	if err != nil || !request.Apply {
		return result, err
	}
	store, err := dabadee.Open(dabadee.Options{Root: filepath.Join(d.roots.driver, "objects"), PreserveMetadata: true})
	if err != nil {
		return result, err
	}
	_, gcErr := store.GC(ctx)
	closeErr := store.Close()
	return result, errors.Join(gcErr, closeErr)
}

func (d *DaBaDee) Verify(ctx context.Context, request storage.VerifyRequest) (storage.VerifyResult, error) {
	if err := validateRequestLayers(request.Layers); err != nil {
		return storage.VerifyResult{}, err
	}
	result := storage.VerifyResult{}
	for _, layer := range request.Layers {
		err := d.verifyLayer(ctx, layer)
		if err != nil && request.Repair {
			_, err = d.prepareLayer(ctx, layer, storage.PrepareRequest{
				Layers: []string{layer}, ClearPrivilegedBits: true,
				OverlayWhiteouts: true, PruneLooseBlocks: true,
			}, true)
			if err == nil {
				err = d.verifyLayer(ctx, layer)
				if err == nil {
					result.Repaired++
				}
			}
		}
		if err != nil {
			return result, err
		}
		result.Verified++
	}
	return result, nil
}

func (d *DaBaDee) verifyLayer(ctx context.Context, layer string) error {
	states, err := fvsrepo.States(d.roots.repository(layer))
	if err != nil {
		return err
	}
	if len(states) == 0 {
		return fmt.Errorf("storaged: layer %s has no state", layer)
	}
	ready, err := os.ReadFile(filepath.Join(d.roots.checkout(layer), dabadeeReady))
	if err != nil || string(ready) != states[0].ID+"\n" {
		return fvsrepo.ErrCheckoutMissing
	}
	return fvsrepo.VerifyCheckoutContext(ctx, d.roots.repository(layer), states[0].ID, fvsrepo.CheckoutOptions{
		To: d.roots.checkout(layer), ClearPrivilegedBits: true,
		OverlayWhiteouts: true, PruneLooseBlocks: true,
	})
}
