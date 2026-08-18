/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/logger"
)

// A pull binds a layer at the one instant the link is provable: the download
// has just been hashed and it is the blob the registry named. This file is for
// the layers that never passed through that instant, which are the ones already
// in the store when a binding was first written, the ones migrated out of the
// legacy directory layout, and the ones cpak builds itself.
//
// A backfill measures nothing. It reads the state the store holds at the moment
// it runs and writes that down as the state the layer is. A layer whose content
// was changed before the backfill ran is recorded as changed, and every launch
// afterwards recognises the change. That is trust on first use: honest on a
// machine whose owner is trusted, worth nothing as evidence to anyone else, and
// never a replacement for pulling the application again. Nothing here verifies
// anything, and nothing that calls it may describe it as verification.

// LayerBindingBackfill is what one pass over the installed applications did.
type LayerBindingBackfill struct {
	// Bound names the layers this pass wrote a record for, taken from the store
	// as it stands now.
	Bound []string `json:"bound"`

	// Unchanged names the layers that already had a record, which this pass did
	// not read and did not touch.
	Unchanged []string `json:"unchanged"`

	// Refused names the layers that are still unbound.
	Refused []LayerBindingRefusal `json:"refused"`
}

// LayerBindingRefusal is a layer the backfill left unbound, and what stopped it.
type LayerBindingRefusal struct {
	Layer  string `json:"layer"`
	Reason string `json:"reason"`
}

// BackfillLayerBindings records a binding for every layer an installed
// application references that has none. It walks the store rather than one
// application, because a layer dependency and an addon are installed
// applications of their own and their layers compose the launches of others.
//
// A layer that cannot be bound does not stop the pass: it is reported, so that
// the caller can say which layers are still not answerable to a digest.
func (c *Cpak) BackfillLayerBindings() (LayerBindingBackfill, error) {
	report := LayerBindingBackfill{}
	apps, err := c.GetInstalledApps()
	if err != nil {
		return report, err
	}
	bindings, err := c.layerBindings()
	if err != nil {
		return report, err
	}
	seen := make(map[string]bool)
	for _, app := range apps {
		for _, layer := range app.ParsedLayers {
			if layer == "" || seen[layer] {
				continue
			}
			seen[layer] = true
			_, found, lookupErr := bindings.Lookup(layer)
			if lookupErr != nil {
				report.Refused = append(report.Refused, LayerBindingRefusal{Layer: layer, Reason: lookupErr.Error()})
				continue
			}
			if found {
				report.Unchanged = append(report.Unchanged, layer)
				continue
			}
			if bindErr := c.recordLayerBinding(layer); bindErr != nil {
				report.Refused = append(report.Refused, LayerBindingRefusal{Layer: layer, Reason: bindErr.Error()})
				continue
			}
			report.Bound = append(report.Bound, layer)
		}
	}
	return report, nil
}

// bindBuiltLayers records a binding for the layers cpak built itself. A runtime
// layer and a locale layer are never downloaded under the digest they are
// stored as, so no pull will ever bind them and this is the only place that
// can. What is bound here is what the build just produced, except for a layer
// the builder found already in the store and returned without rebuilding, which
// is bound on the same trust as a backfill.
func (c *Cpak) bindBuiltLayers(before, after []string) error {
	built := make(map[string]bool, len(before))
	for _, layer := range before {
		built[layer] = true
	}
	bindings, err := c.layerBindings()
	if err != nil {
		return err
	}
	for _, layer := range after {
		if built[layer] {
			continue
		}
		_, found, err := bindings.Lookup(layer)
		if err != nil {
			return fmt.Errorf("read the binding of layer %s: %w", layer, err)
		}
		if found {
			continue
		}
		// A layer still held in the legacy directory layout has no state to
		// name yet. It gets one when it is migrated, and the migration binds it
		// there, so nothing is decided here.
		available, err := c.fvsLayerAvailable(layer)
		if err != nil {
			return fmt.Errorf("read the state of layer %s: %w", layer, err)
		}
		if !available {
			logger.Printf("Warning: the built layer %s holds no state to bind yet", layer)
			continue
		}
		if err = c.recordLayerBinding(layer); err != nil {
			return err
		}
	}
	return nil
}
