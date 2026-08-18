/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/integrity"
)

// layerBindings opens the ledger that remembers which store state a layer
// digest produced. It sits beside the store and not inside the fvs root,
// because the fvs root is what the storage daemon is given to read and a claim
// about a layer is not the daemon's to see.
func (c *Cpak) layerBindings() (*integrity.DirectoryBindings, error) {
	return integrity.NewDirectoryBindings(c.GetInStoreDir("bindings"))
}

// recordLayerBinding ties a layer digest to the state now published for it.
// The published repository is read back instead of trusting the answer the
// writer gave, because a layer that loses the publish race is served by the
// state that won it.
func (c *Cpak) recordLayerBinding(digest string) error {
	repository := c.fvsLayerPath(digest)
	states, err := fvsrepo.States(repository)
	if err != nil {
		return fmt.Errorf("read layer states for %s: %w", digest, err)
	}
	if len(states) == 0 {
		return fmt.Errorf("layer %s holds no state", digest)
	}
	root, err := layerStateRoot(repository, states[0].ID)
	if err != nil {
		return err
	}
	bindings, err := c.layerBindings()
	if err != nil {
		return err
	}
	return bindings.Bind(integrity.LayerBinding{
		OCIDigest: digest,
		StateID:   states[0].ID,
		StateRoot: root,
	})
}

// layerStateRoot digests the state a layer repository holds. fvs2 keeps its own
// tree hash unexported, so the root is rebuilt from the entry list the
// repository does export, which is the strongest statement available: the block
// identifiers in it are content addresses. The whole entry is hashed rather
// than a chosen subset, so a field the store starts recording is covered
// without this having to learn about it first.
func layerStateRoot(repository, state string) (string, error) {
	entries, err := fvsrepo.StateFiles(repository, state)
	if err != nil {
		return "", fmt.Errorf("read layer state %s: %w", state, err)
	}
	ordered := append([]fvsrepo.FileEntry(nil), entries...)
	sort.Slice(ordered, func(first, second int) bool { return ordered[first].Path < ordered[second].Path })
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return "", fmt.Errorf("encode layer state %s: %w", state, err)
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "cpak.layer-state.v%d\n", integrity.ABIVersion)
	hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil)), nil
}
