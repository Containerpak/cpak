/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// The ceiling answers a question no signature can: not who published an
// application, but how much it is allowed to do here. It is the widest policy
// this host permits, it beats the manifest and the user override alike, and it
// is deliberately independent of who signed anything. A publisher an
// administrator approved is still held to it, which is the whole point: an
// approval granted once should not be a permanent grant of everything the
// publisher later decides to ask for.
const ceilingFileName = "ceiling"

// ActionSetCeiling is separate from setting the enforcement level because
// widening what every application on the host may do is not the same decision
// as choosing what happens to one it cannot recognise.
const ActionSetCeiling = "it.cpak.system.set-ceiling"

const ceilingSizeLimit = 1 << 20

// Ceiling is the recorded maximum. Present says whether an administrator has
// decided anything at all: a host with no ceiling is unmanaged and every
// application keeps the policy it already had.
type Ceiling struct {
	Present bool
	Policy  types.Override
}

// CeilingStore reads the ceiling from beside the ledger, where the account that
// launches an application cannot write it.
type CeilingStore struct {
	Directory string
	OwnerUID  uint32
}

func DefaultCeilingStore() CeilingStore {
	return CeilingStore{Directory: DefaultAnchorDirectory, OwnerUID: 0}
}

// Load answers the empty ceiling for a host nobody has configured, and for one
// whose file cannot be vouched for. A file that is missing, not owned by root
// or writable by anyone else decides nothing: it must not be able to widen what
// applications may do, and it must not be able to stop them running either.
func (c CeilingStore) Load() (Ceiling, error) {
	data, found, err := readTrusted(c.path(), c.OwnerUID, ceilingSizeLimit, "cpak ceiling")
	if err != nil || !found {
		return Ceiling{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	policy := types.NewOverride()
	if err := decoder.Decode(&policy); err != nil {
		return Ceiling{}, fmt.Errorf("read the cpak ceiling: %w", err)
	}
	if err := types.ValidateFilesystemPermissions(policy.Filesystem); err != nil {
		return Ceiling{}, fmt.Errorf("read the cpak ceiling: %w", err)
	}
	return Ceiling{Present: true, Policy: policy}, nil
}

// Store writes the ceiling. It is privileged, and the caller reaches it through
// the authority rather than by writing the file, so the polkit action is what
// decides whether it may change.
func (c CeilingStore) Store(policy types.Override) error {
	if err := types.ValidateFilesystemPermissions(policy.Filesystem); err != nil {
		return fmt.Errorf("write the cpak ceiling: %w", err)
	}
	if err := ensureDirectory(c.Directory, c.OwnerUID); err != nil {
		return fmt.Errorf("write the cpak ceiling: %w", err)
	}
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return fmt.Errorf("write the cpak ceiling: %w", err)
	}
	return writeAtomic(c.path(), append(data, '\n'), 0644)
}

// Clear removes the ceiling, which returns the host to unmanaged.
func (c CeilingStore) Clear() error {
	if err := os.Remove(c.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the cpak ceiling: %w", err)
	}
	return nil
}

func (c CeilingStore) path() string {
	return filepath.Join(c.Directory, ceilingFileName)
}

// HostCeiling is what the rest of cpak reads. It is a variable so a test can
// answer for a host it did not have to build.
var HostCeiling = func() Ceiling {
	ceiling, err := DefaultCeilingStore().Load()
	if err != nil {
		return Ceiling{}
	}
	return ceiling
}
