/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ActionSetEnforcement = "it.cpak.system.set-enforcement"

	enforcementSetAction = "set-enforcement"
	enforcementFileName  = "enforcement"
	enforcementSizeLimit = 64
)

// EnforcementLevel is what happens to a launch the ledger does not answer for.
// It lives next to the ledger and is owned by the same account, and it is read
// from nowhere else: not from the environment, not from anything under a home
// directory. It decides whether a refusal happens, so the account a refusal
// binds must not be an account that can write it.
type EnforcementLevel string

const (
	// EnforcementOff is the default and the reason it is safe to ship this at
	// all: until an administrator sets a level, nothing changes for anybody.
	EnforcementOff EnforcementLevel = "off"

	// EnforcementWarn says at every launch what refuse would have refused,
	// which is how a host finds out what is not enrolled before it starts
	// losing applications.
	EnforcementWarn EnforcementLevel = "warn"

	// EnforcementRefuse is the point of all of it: an application the ledger
	// does not answer for does not start.
	EnforcementRefuse EnforcementLevel = "refuse"
)

func (l EnforcementLevel) valid() bool {
	switch l {
	case EnforcementOff, EnforcementWarn, EnforcementRefuse:
		return true
	}
	return false
}

// EnforcementStore holds the level. It is a sibling of the ledger and is proven
// the same way the ledger is, because a level anybody could write would let the
// side a refusal is aimed at decide that there is no refusal.
type EnforcementStore struct {
	Directory string
	OwnerUID  uint32
}

func DefaultEnforcementStore() EnforcementStore {
	return EnforcementStore{Directory: DefaultAnchorDirectory, OwnerUID: 0}
}

// Level reads what the administrator set. A level that is absent, unreadable or
// not trusted is not an instruction to refuse anything, so it reads as off: a
// host where nothing was ever set behaves as it always did, and a file nobody
// can vouch for is never allowed to turn refusals on or off. Whoever can write
// in this directory already owns the ledger next to it.
func (s EnforcementStore) Level() (EnforcementLevel, error) {
	path, err := s.path()
	if err != nil {
		return EnforcementOff, err
	}
	if err := validateExistingDirectory(s.Directory, s.OwnerUID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return EnforcementOff, nil
		}
		return EnforcementOff, err
	}
	data, found, err := readTrusted(path, s.OwnerUID, enforcementSizeLimit, "enforcement level")
	if err != nil || !found {
		return EnforcementOff, err
	}
	level := EnforcementLevel(strings.TrimSpace(string(data)))
	if !level.valid() {
		return EnforcementOff, errors.New("recorded enforcement level is not a level")
	}
	return level, nil
}

func (s EnforcementStore) Set(level EnforcementLevel) error {
	if !level.valid() {
		return errors.New("invalid enforcement level")
	}
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := ensureDirectory(s.Directory, s.OwnerUID); err != nil {
		return err
	}
	if err := writeAtomic(path, []byte(string(level)+"\n"), 0644); err != nil {
		return fmt.Errorf("write the enforcement level: %w", err)
	}
	return nil
}

// path keeps the level a sibling of the per-user directories of the ledger. A
// user directory is a number, so no account can ever claim this name.
func (s EnforcementStore) path() (string, error) {
	if !filepath.IsAbs(s.Directory) {
		return "", errors.New("system authority enforcement path must be absolute")
	}
	return filepath.Join(s.Directory, enforcementFileName), nil
}

// Enforcement is what a launch asks. It answers with a level and never with a
// failure, because a launch that cannot read the level has not been told to
// refuse anything.
func Enforcement() EnforcementLevel {
	level, err := DefaultEnforcementStore().Level()
	if err != nil {
		return EnforcementOff
	}
	return level
}

// SetEnforcement is the switch. It is privileged and it walks the transports
// the way an enrolment does, because turning refusals on for every account on
// the host is the owner of the machine's decision and nobody else's.
func SetEnforcement(level EnforcementLevel) error {
	if !level.valid() {
		return errors.New("invalid enforcement level")
	}
	return dispatchIntegrity(socketRequest{Action: enforcementSetAction, Level: string(level)})
}

func applyEnforcement(store EnforcementStore, message socketRequest) error {
	if message.Action != enforcementSetAction {
		return errors.New("unsupported system authority action")
	}
	return store.Set(EnforcementLevel(message.Level))
}
