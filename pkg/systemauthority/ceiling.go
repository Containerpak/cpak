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
//
// Named is the other half of the answer, and leaving it out was a bug worth
// naming. A ceiling meets a policy by intersection, so a permission the
// administrator never wrote met whatever the zero value happened to be: a file
// saying only that the session bus is closed also closed the ssh agent, emptied
// every filesystem permission and dropped every environment variable, for every
// application on the host. A ceiling constrains what it mentions and nothing
// else, and Named is what it mentioned.
//
// A nil Named means every permission, so a ceiling assembled in code rather
// than read from a file holds all of them. The empty map is the other case and
// a real one: a file that names nothing constrains nothing.
type Ceiling struct {
	Present bool
	Policy  types.Override
	Named   map[string]bool
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
	policy, named, err := parseCeiling(data)
	if err != nil {
		return Ceiling{}, fmt.Errorf("read the cpak ceiling: %w", err)
	}
	return Ceiling{Present: true, Policy: policy, Named: named}, nil
}

// parseCeiling reads the file twice on purpose. Once for the values, and once
// for the keys that are actually in it, which is what decides how far the
// ceiling reaches: the decoded struct cannot tell a permission set to false
// apart from one nobody wrote.
func parseCeiling(data []byte) (types.Override, map[string]bool, error) {
	var policy types.Override
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return types.Override{}, nil, err
	}
	if err := types.ValidateFilesystemPermissions(policy.Filesystem); err != nil {
		return types.Override{}, nil, err
	}
	if err := types.ValidateDBusPolicy(policy.SessionBus); err != nil {
		return types.Override{}, nil, err
	}
	var written map[string]json.RawMessage
	if err := json.Unmarshal(data, &written); err != nil {
		return types.Override{}, nil, err
	}
	named := make(map[string]bool, len(written))
	for key := range written {
		named[key] = true
	}
	return policy, named, nil
}

// Store writes the ceiling. It is privileged, and the caller reaches it through
// the authority rather than by writing the file, so the polkit action is what
// decides whether it may change.
//
// It takes the administrator's bytes rather than a decoded policy because the
// keys they left out are part of what the file says. Writing back a marshalled
// struct would name every permission and turn a file about one of them into a
// ceiling over all of them.
func (c CeilingStore) Store(data []byte) error {
	if _, _, err := parseCeiling(data); err != nil {
		return fmt.Errorf("write the cpak ceiling: %w", err)
	}
	if err := ensureDirectory(c.Directory, c.OwnerUID); err != nil {
		return fmt.Errorf("write the cpak ceiling: %w", err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		data = append(bytes.Clone(data), '\n')
	}
	return writeAtomic(c.path(), data, 0644)
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

// ValidateCeiling reports whether a file could be a ceiling, so a caller can
// refuse a bad one before asking anybody to authenticate. The parse is the same
// one the store performs, because a file that passes here and fails there would
// be the worst of both.
func ValidateCeiling(data []byte) error {
	_, _, err := parseCeiling(data)
	return err
}
