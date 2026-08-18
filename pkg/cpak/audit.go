/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// A launch is gated on two things the store keeps about itself: the binding
// that ties a layer digest to the state the store produced for it, and the
// shape that state gives the prepared checkout of the layer. Nothing ever named
// either of them outside a launch. An installation made before bindings existed
// carries none, so the gate has nothing to derive from and protects it exactly
// zero, and the flag that would give it bindings is reachable only by somebody
// who already knows it is there. This file is the part that says so.
//
// It reports and it writes nothing, and what it reports is deliberately narrow.
// A binding says which state the store produced for a layer. Whether that state
// holds what the publisher shipped depends on when the binding was written, and
// the binding does not say: a pull writes one from a download the registry
// digest answered for, a backfill writes one from whatever was in the store at
// that moment, and no later reading tells the two apart. Nothing here may be
// worded so that a reader can come away believing their store was checked.

// ApplicationIntegrity is what the store can say about one installed
// application, counted over the layers a launch of it composes rather than the
// ones its own manifest lists, because the composed set is what gets mounted.
type ApplicationIntegrity struct {
	Origin  string `json:"origin"`
	Version string `json:"version"`

	// Layers is how many layers compose a launch, and BoundLayers how many of
	// them a binding ties to a store state.
	Layers      int `json:"layers"`
	BoundLayers int `json:"boundLayers"`

	// PreparedCheckouts is how many of those layers the storage driver serves
	// out of a prepared directory, and DescribedCheckouts how many of those
	// directories a state the store holds says the shape of. A checkout no
	// state describes is one nothing can be compared against, which is not the
	// same as one that failed a comparison.
	PreparedCheckouts  int `json:"preparedCheckouts"`
	DescribedCheckouts int `json:"describedCheckouts"`

	// Disagreements are the layers the store contradicts itself about: what a
	// binding names and what the store now holds, or what a state describes and
	// what the prepared checkout now is. One line each, and a launch is refused
	// while any of them stand.
	Disagreements []string `json:"disagreements,omitempty"`

	// Unmeasured are the layers the store cannot re-derive at all, so nothing
	// disagrees and nothing answers for them either.
	Unmeasured []string `json:"unmeasured,omitempty"`

	// Unreadable is why none of the counts above could be worked out. It is set
	// instead of failing the whole pass, because an audit that gives up on the
	// first application it cannot read says nothing about the rest.
	Unreadable string `json:"unreadable,omitempty"`
}

// UnboundLayers is how many layers of this application no binding names.
func (a ApplicationIntegrity) UnboundLayers() int {
	return a.Layers - a.BoundLayers
}

// UndescribedCheckouts is how many prepared directories no state describes.
func (a ApplicationIntegrity) UndescribedCheckouts() int {
	return a.PreparedCheckouts - a.DescribedCheckouts
}

// FullyAnswered reports whether something in the store speaks for every layer
// and every prepared directory of this application, which is not the same as
// what it says being worth anything.
func (a ApplicationIntegrity) FullyAnswered() bool {
	return a.Unreadable == "" && a.UnboundLayers() == 0 && a.UndescribedCheckouts() == 0
}

// StoreIntegrity is one pass over every installed application.
type StoreIntegrity struct {
	Applications []ApplicationIntegrity `json:"applications"`
}

// UnboundLayers is how many layers across the store no binding names.
func (s StoreIntegrity) UnboundLayers() int {
	total := 0
	for _, app := range s.Applications {
		total += app.UnboundLayers()
	}
	return total
}

// UndescribedCheckouts is how many prepared directories across the store no
// state describes.
func (s StoreIntegrity) UndescribedCheckouts() int {
	total := 0
	for _, app := range s.Applications {
		total += app.UndescribedCheckouts()
	}
	return total
}

// Disagreements is how many layers across the store the store contradicts
// itself about.
func (s StoreIntegrity) Disagreements() int {
	total := 0
	for _, app := range s.Applications {
		total += len(app.Disagreements)
	}
	return total
}

// Unreadable is how many applications nothing could be worked out for.
func (s StoreIntegrity) Unreadable() int {
	total := 0
	for _, app := range s.Applications {
		if app.Unreadable != "" {
			total++
		}
	}
	return total
}

// IntegrityReport reads what the store records about every installed
// application and puts it next to what the store now holds. It writes nothing:
// an application missing its bindings is reported as missing them and is never
// given them, because writing a binding from the store as it stands is a
// separate act with a separate cost and it has to be asked for.
func (c *Cpak) IntegrityReport() (StoreIntegrity, error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return StoreIntegrity{}, err
	}
	recorded := c.integrityRecordsExist()
	report := StoreIntegrity{Applications: make([]ApplicationIntegrity, 0, len(apps))}
	for _, app := range apps {
		report.Applications = append(report.Applications, c.applicationIntegrity(app, recorded))
	}
	return report, nil
}

// integrityRecordsExist reports whether the store holds the directory the
// records live in. It is asked before anything opens a ledger, because opening
// one creates that directory, and the store that has none is exactly the store
// this report exists for: a report must not be the thing that writes to it.
func (c *Cpak) integrityRecordsExist() bool {
	info, err := os.Stat(c.GetInStoreDir("bindings"))
	return err == nil && info.IsDir()
}

// applicationIntegrity answers for one application. A store with no records at
// all is answered without opening a ledger, and the answer is the one the
// ledgers would have given: nothing speaks for anything.
func (c *Cpak) applicationIntegrity(app types.Application, recorded bool) ApplicationIntegrity {
	state := ApplicationIntegrity{Origin: app.Origin, Version: app.Version}
	layers, err := c.composedApplicationLayers(app)
	if err != nil {
		state.Unreadable = err.Error()
		return state
	}
	state.Layers = len(layers)
	prepared, err := c.preparedCheckoutCount(layers)
	if err != nil {
		state.Unreadable = err.Error()
		return state
	}
	state.PreparedCheckouts = prepared
	if !recorded {
		return state
	}
	bound, err := c.boundLayerCount(layers)
	if err != nil {
		state.Unreadable = err.Error()
		return state
	}
	state.BoundLayers = bound
	measurement, err := c.measureLaunch(layers)
	if err != nil {
		state.Unreadable = err.Error()
		return state
	}
	state.DescribedCheckouts = prepared - len(measurement.Unrecorded)
	state.Disagreements = findingReasons(measurement.Disagreements)
	state.Unmeasured = findingReasons(measurement.Unmeasured)
	return state
}

// composedApplicationLayers answers with the layers a launch of an application
// stacks, which is what the gate weighs and therefore what is worth counting.
func (c *Cpak) composedApplicationLayers(app types.Application) ([]string, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	components, err := c.resolveLayerDependenciesFromStore(app, store)
	if err != nil {
		return nil, err
	}
	addons, err := c.resolveEnabledAddonsFromStore(app, store)
	if err != nil {
		return nil, err
	}
	return composedLayers(app, components, addons), nil
}

// boundLayerCount counts the layers a binding ties to a store state.
func (c *Cpak) boundLayerCount(layers []string) (int, error) {
	bindings, err := c.layerBindings()
	if err != nil {
		return 0, err
	}
	bound := 0
	for _, layer := range layers {
		_, found, lookupErr := bindings.Lookup(layer)
		if lookupErr != nil {
			return 0, fmt.Errorf("read the binding of layer %s: %w", layer, lookupErr)
		}
		if found {
			bound++
		}
	}
	return bound, nil
}

// preparedCheckoutCount counts the layers the storage driver serves out of a
// prepared directory. A launch told to mount the repositories through FUSE
// reads no prepared index, so under that backend nothing is served that way and
// the count is zero, which is how the launch measurement treats it too.
func (c *Cpak) preparedCheckoutCount(layers []string) (int, error) {
	backend, err := configuredStorageBackend()
	if err != nil {
		return 0, err
	}
	if backend == storageBackendFUSE {
		return 0, nil
	}
	directories, err := c.launchCheckoutDirectories(layers)
	if err != nil {
		return 0, err
	}
	return len(directories), nil
}
