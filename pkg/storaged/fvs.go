package storaged

import (
	"context"
	"errors"
	"fmt"
	"os"

	storage "github.com/containerpak/storage/pkg/driver"
	fvsrepo "github.com/fvs-lab/fvs2/repo"
)

// FVS exposes FVS layer checkouts through the cpak storage driver protocol.
type FVS struct {
	roots roots
}

// NewFVS creates the default cpak storage driver.
func NewFVS(sourceRoot, driverRoot string) (*FVS, error) {
	roots, err := newRoots(sourceRoot, driverRoot)
	if err != nil {
		return nil, err
	}
	return &FVS{roots: roots}, nil
}

func (d *FVS) Probe(context.Context) (storage.Info, error) {
	return storage.Info{
		Name: "fvs", Version: driverVersion, Protocol: storage.ProtocolVersion,
		Capabilities: []string{storage.MethodPrepare, storage.MethodRemove, storage.MethodGC, storage.MethodVerify},
	}, nil
}

func (d *FVS) Prepare(ctx context.Context, request storage.PrepareRequest) (storage.PrepareResult, error) {
	if err := validateRequestLayers(request.Layers); err != nil {
		return storage.PrepareResult{}, err
	}
	result := storage.PrepareResult{LowerDirs: make([]string, 0, len(request.Layers))}
	for index := len(request.Layers) - 1; index >= 0; index-- {
		layer := request.Layers[index]
		repository := d.roots.repository(layer)
		states, err := fvsrepo.States(repository)
		if err != nil {
			return storage.PrepareResult{}, err
		}
		if len(states) == 0 {
			return storage.PrepareResult{}, fmt.Errorf("storaged: layer %s has no state", layer)
		}
		checkout, err := fvsrepo.CheckoutContext(ctx, repository, states[0].ID, fvsrepo.CheckoutOptions{
			To: d.roots.checkout(layer), ClearPrivilegedBits: request.ClearPrivilegedBits,
			OverlayWhiteouts: request.OverlayWhiteouts, PruneLooseBlocks: request.PruneLooseBlocks,
			ExistingOnly: request.ExistingOnly,
		})
		if err != nil {
			return storage.PrepareResult{}, err
		}
		root, err := storage.ValidateDriverPath(d.roots.driver, checkout.Root)
		if err != nil {
			return storage.PrepareResult{}, err
		}
		result.LowerDirs = append(result.LowerDirs, root)
	}
	return result, nil
}

func (d *FVS) Remove(_ context.Context, request storage.RemoveRequest) (storage.RemoveResult, error) {
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
	return result, nil
}

func (d *FVS) GC(ctx context.Context, request storage.GCRequest) (storage.GCResult, error) {
	return collectDriverGarbage(ctx, d.roots.driver, request.LiveLayers, request.Apply)
}

func (d *FVS) Verify(ctx context.Context, request storage.VerifyRequest) (storage.VerifyResult, error) {
	if err := validateRequestLayers(request.Layers); err != nil {
		return storage.VerifyResult{}, err
	}
	result := storage.VerifyResult{}
	for _, layer := range request.Layers {
		err := d.verifyLayer(ctx, layer)
		if err != nil && request.Repair {
			states, stateErr := fvsrepo.States(d.roots.repository(layer))
			if stateErr != nil {
				return result, stateErr
			}
			if len(states) == 0 {
				return result, fmt.Errorf("storaged: layer %s has no state", layer)
			}
			_, err = fvsrepo.CheckoutContext(ctx, d.roots.repository(layer), states[0].ID, fvsrepo.CheckoutOptions{
				To: d.roots.checkout(layer), ClearPrivilegedBits: true,
				OverlayWhiteouts: true, PruneLooseBlocks: true, ReplaceExisting: true,
			})
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

func (d *FVS) verifyLayer(ctx context.Context, layer string) error {
	states, err := fvsrepo.States(d.roots.repository(layer))
	if err != nil {
		return err
	}
	if len(states) == 0 {
		return fmt.Errorf("storaged: layer %s has no state", layer)
	}
	return fvsrepo.VerifyCheckoutContext(ctx, d.roots.repository(layer), states[0].ID, fvsrepo.CheckoutOptions{
		To: d.roots.checkout(layer), ClearPrivilegedBits: true,
		OverlayWhiteouts: true, PruneLooseBlocks: true,
	})
}
