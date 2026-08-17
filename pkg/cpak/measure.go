/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	storageindex "github.com/containerpak/storage/pkg/index"
	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"golang.org/x/sys/unix"
)

// A record is a claim, and a launch that reads one back has learnt only what
// the store once said about itself. Everything here re-derives those values
// from the store as it stands now and puts the two answers side by side, so
// that a layer changed under a record nobody touched is a finding instead of a
// launch.
//
// The two halves cover the two things a launch can end up mounting. The state
// root covers the repository a layer lives in, derived exactly the way the
// pull that recorded it derived it. The checkout half covers the prepared
// directory the overlay actually stacks, and it is derived too: the shape that
// directory must have comes out of the bound state, which is inside the
// binding, which is inside the package root an anchor is taken over. Neither
// half asks a file the launching account owns what the answer should be.
//
// What this is not: the state root is a digest of the entry list of a state,
// and the block identifiers in that list are content addresses. It therefore
// answers for the commit and not for the bytes the blocks hold, and the same
// separation is stated on measureCheckoutTree for the prepared side. Reading
// every byte of either is seconds, and a launch cannot be asked to pay that;
// CheckPreparedCheckoutContents is where a caller pays it on purpose.

// errLayerNotStored means a layer is not held as a repository, so there is no
// state to re-derive and nothing the record can be compared with.
var errLayerNotStored = errors.New("the layer is not held as a store repository")

// layerFinding names one layer a measurement had something to say about, and
// says what.
type layerFinding struct {
	Layer  string
	Detail string
}

func (f layerFinding) String() string {
	return f.Layer + ": " + f.Detail
}

// launchMeasurement is what re-deriving a launch from the store concluded. The
// three lists are kept apart because they need three different answers, and
// collapsing them would turn a store that cannot be read into a store that
// disagrees with itself.
type launchMeasurement struct {
	// Disagreements are the layers the store no longer holds what is recorded
	// for. Whatever else agrees, one of the two answers is not what the store
	// produced, and that is true of an enrolled launch and an unenrolled one
	// alike.
	Disagreements []layerFinding

	// Unrecorded are the layers served out of a prepared checkout that no
	// state describes, so nothing derivable says what that checkout should
	// hold. Nothing disagrees, and nothing answers for them either, which is
	// the unbound case and not the tampered one.
	Unrecorded []string

	// Unmeasured are the layers the store cannot re-derive at all, with the
	// reason. A layer held no repository is a layer served from somewhere this
	// cannot see, and saying so is the honest answer.
	Unmeasured []layerFinding
}

// measureLaunch re-derives, from the store itself, everything the store has
// recorded about the layers of a launch.
func (c *Cpak) measureLaunch(layers []string) (launchMeasurement, error) {
	var measurement launchMeasurement
	if err := c.measureLaunchStates(&measurement, layers); err != nil {
		return launchMeasurement{}, err
	}
	if err := c.measureLaunchCheckouts(&measurement, layers); err != nil {
		return launchMeasurement{}, err
	}
	return measurement, nil
}

// measureLaunchStates compares every layer binding with the state the store
// now holds for the layer it names. A layer with no binding is skipped: there
// is nothing to disagree with, and what an unbound layer costs a launch is the
// verdict's decision and not this one's.
func (c *Cpak) measureLaunchStates(measurement *launchMeasurement, layers []string) error {
	bindings, err := c.layerBindings()
	if err != nil {
		return err
	}
	for _, layer := range layers {
		recorded, found, lookupErr := bindings.Lookup(layer)
		if lookupErr != nil {
			return fmt.Errorf("read the binding of layer %s: %w", layer, lookupErr)
		}
		if !found {
			continue
		}
		derived, deriveErr := c.deriveLayerState(layer)
		if errors.Is(deriveErr, errLayerNotStored) {
			measurement.Unmeasured = append(measurement.Unmeasured, layerFinding{Layer: layer, Detail: deriveErr.Error()})
			continue
		}
		if deriveErr != nil {
			return deriveErr
		}
		detail, agrees := compareLayerState(recorded, derived)
		if agrees {
			continue
		}
		measurement.Disagreements = append(measurement.Disagreements, layerFinding{Layer: layer, Detail: detail})
	}
	return nil
}

// deriveLayerState answers with the binding a pull would record for a layer
// now. It is recordLayerBinding with the writing taken out: the same
// repository, the same choice of state and the same digest of it, so that what
// it answers can be put next to what was filed and mean the same thing.
func (c *Cpak) deriveLayerState(digest string) (integrity.LayerBinding, error) {
	repository := c.fvsLayerPath(digest)
	states, err := fvsrepo.States(repository)
	if layerNotStored(err) {
		return integrity.LayerBinding{}, fmt.Errorf("%w: %s", errLayerNotStored, digest)
	}
	if err != nil {
		return integrity.LayerBinding{}, fmt.Errorf("read layer states for %s: %w", digest, err)
	}
	if len(states) == 0 {
		return integrity.LayerBinding{}, fmt.Errorf("%w: %s", errLayerNotStored, digest)
	}
	root, err := layerStateRoot(repository, states[0].ID)
	if err != nil {
		return integrity.LayerBinding{}, err
	}
	return integrity.LayerBinding{OCIDigest: digest, StateID: states[0].ID, StateRoot: root}, nil
}

// layerNotStored reports whether reading a repository failed because there is
// no repository, which is not the same as a repository that would not answer.
func layerNotStored(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fvsrepo.ErrNotInitialized) || errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err)
}

// compareLayerState puts a record next to what the store now holds. The digest
// is not compared: the record is filed under it, so the two are the same layer
// by construction. The state comes first because a repository serving another
// state is a different finding from a state that digests differently, and the
// two are worth telling apart in the message.
func compareLayerState(recorded, derived integrity.LayerBinding) (string, bool) {
	if recorded.StateID != derived.StateID {
		return fmt.Sprintf("the store serves state %s where %s was recorded", derived.StateID, recorded.StateID), false
	}
	if recorded.StateRoot != derived.StateRoot {
		return fmt.Sprintf("state %s digests to %s where %s was recorded", recorded.StateID, derived.StateRoot, recorded.StateRoot), false
	}
	return "", true
}

// measureLaunchCheckouts answers for the tree the overlay actually stacks. A
// layer the prepared index does not name is not served from a checkout, and a
// launch told to mount the repositories through FUSE reads no index at all, so
// in both cases there is nothing here to measure.
func (c *Cpak) measureLaunchCheckouts(measurement *launchMeasurement, layers []string) error {
	backend, err := configuredStorageBackend()
	if err != nil {
		return err
	}
	if backend == storageBackendFUSE {
		return nil
	}
	directories, err := c.launchCheckoutDirectories(layers)
	if err != nil {
		return err
	}
	for _, layer := range layers {
		directory, prepared := directories[layer]
		if !prepared {
			continue
		}
		found, matches, verifyErr := c.verifyPreparedCheckout(layer, directory)
		if verifyErr != nil {
			return fmt.Errorf("measure the prepared checkout of layer %s: %w", layer, verifyErr)
		}
		if !found {
			// Both lists, because the two say different things: the first
			// decides the verdict, the second is what the user is told about
			// a launch nothing contradicted only because nothing could speak.
			measurement.Unrecorded = append(measurement.Unrecorded, layer)
			measurement.Unmeasured = append(measurement.Unmeasured, layerFinding{
				Layer:  layer,
				Detail: "no state the store holds describes its prepared checkout",
			})
			continue
		}
		if matches {
			continue
		}
		measurement.Disagreements = append(measurement.Disagreements, layerFinding{
			Layer:  layer,
			Detail: "the prepared checkout " + directory + " is not the shape the state it was made from describes",
		})
	}
	return nil
}

// launchCheckoutDirectories answers with the directory the prepared index
// resolves each layer to, and leaves out the layers it does not resolve. A
// layer the index cannot resolve is a layer the mount path will not take from
// the index either: it re-prepares instead, and what it prepares is measured
// on the way out.
func (c *Cpak) launchCheckoutDirectories(layers []string) (map[string]string, error) {
	name, err := c.storageDriverName()
	if err != nil {
		return nil, err
	}
	index, err := storageindex.Load(c.storageDriverIndex(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if index.Driver != name {
		return nil, nil
	}
	root := c.storageDriverRoot(name)
	directories := make(map[string]string, len(layers))
	for _, layer := range layers {
		// One layer at a time, because a single layer the index cannot answer
		// for must not hide the ones it can.
		resolved, resolveErr := index.Resolve(root, []string{layer})
		if resolveErr != nil {
			continue
		}
		directories[layer] = resolved[0]
	}
	return directories, nil
}

// CheckoutContentCheck is what reading a prepared checkout against the content
// digests of its state found. Files and Bytes are what was read, so a caller
// can say how much of the tree the answer covers instead of assuming all of it.
type CheckoutContentCheck struct {
	Layer     string   `json:"layer"`
	State     string   `json:"state"`
	Directory string   `json:"directory"`
	Files     int      `json:"files"`
	Bytes     int64    `json:"bytes"`
	Changed   []string `json:"changed"`
	Missing   []string `json:"missing"`
	Unchecked []string `json:"unchecked"`
}

// Sound reports whether every file the state can answer for held what the state
// says it holds. It is deliberately not enough on its own: Unchecked names the
// files the state carries no digest for, and a caller that ignores that list is
// reading silence as agreement.
func (r CheckoutContentCheck) Sound() bool {
	return len(r.Changed) == 0 && len(r.Missing) == 0
}

// CheckPreparedCheckoutContents reads every file of the prepared checkout of a
// layer and compares it with the content digest the state carries for it. This
// is the only thing here that answers for the bytes of a checkout rather than
// for its shape, and it is deliberately not on the launch path: it reads the
// whole tree, which is seconds on a wide layer against the tens of milliseconds
// a metadata walk costs. A caller asks for it when it wants that answer.
//
// It answers for the files the state names and not for the files the checkout
// holds. A path the state never named is a shape finding, which measureLaunch
// already makes, and this does not repeat it.
func (c *Cpak) CheckPreparedCheckoutContents(layer string) (CheckoutContentCheck, error) {
	shape, err := c.boundCheckoutShape(layer)
	if err != nil {
		return CheckoutContentCheck{}, err
	}
	directories, err := c.launchCheckoutDirectories([]string{layer})
	if err != nil {
		return CheckoutContentCheck{}, err
	}
	directory, prepared := directories[layer]
	if !prepared {
		return CheckoutContentCheck{}, fmt.Errorf("layer %s has no prepared checkout to read", layer)
	}
	checkout, resolved, err := c.openPreparedCheckout(directory)
	if err != nil {
		return CheckoutContentCheck{}, err
	}
	defer unix.Close(checkout)
	check := CheckoutContentCheck{Layer: layer, State: shape.state, Directory: resolved}
	// One buffer for the whole tree: a checkout holds tens of thousands of
	// files and a fresh buffer per file is most of what this would allocate.
	buffer := make([]byte, 128*1024)
	for _, name := range sortedCheckoutPaths(shape.contents) {
		expected := shape.contents[name]
		if expected == "" {
			check.Unchecked = append(check.Unchecked, name)
			continue
		}
		size, digest, readErr := digestCheckoutFile(checkout, name, buffer)
		if errors.Is(readErr, fs.ErrNotExist) {
			check.Missing = append(check.Missing, name)
			continue
		}
		if readErr != nil {
			return check, readErr
		}
		check.Files++
		check.Bytes += size
		if !strings.EqualFold(digest, expected) {
			check.Changed = append(check.Changed, name)
		}
	}
	return check, nil
}

func sortedCheckoutPaths(contents map[string]string) []string {
	paths := make([]string, 0, len(contents))
	for name := range contents {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}

// digestCheckoutFile reads one file of a checkout from the descriptor of the
// checkout itself, so a symlink planted at any component of the name cannot
// send the read at a file outside the tree being checked.
func digestCheckoutFile(root int, name string, buffer []byte) (int64, string, error) {
	fd, err := tools.OpenBeneathAt(root, name, unix.O_RDONLY)
	if err != nil {
		return 0, "", err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	digest := sha256.New()
	// The reader is wrapped so that io.CopyBuffer cannot take the ReadFrom or
	// WriteTo shortcut and allocate a buffer of its own instead of this one.
	size, err := io.CopyBuffer(struct{ io.Writer }{digest}, struct{ io.Reader }{file}, buffer)
	if err != nil {
		return 0, "", fmt.Errorf("read %s: %w", name, err)
	}
	return size, "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// findingReasons flattens findings into the lines a caller can print.
func findingReasons(findings []layerFinding) []string {
	if len(findings) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(findings))
	for _, finding := range findings {
		reasons = append(reasons, finding.String())
	}
	return reasons
}
