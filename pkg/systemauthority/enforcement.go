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

	"github.com/godbus/dbus/v5"
)

const (
	ActionSetEnforcement = "it.cpak.system.set-enforcement"

	// ActionSetSignaturePolicy is the other half of the same decision. It is a
	// separate action because refusing to enrol unsigned software and refusing
	// to launch software nothing claims are two different costs, and an
	// administrator who agreed to one has not agreed to the other.
	ActionSetSignaturePolicy = "it.cpak.system.set-signature-policy"

	enforcementSetAction    = "set-enforcement"
	enforcementFileName     = "enforcement"
	signaturePolicyFileName = "signatures"
	settingSizeLimit        = 64
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

// SignaturePolicy is what this host does about a package no publisher signed.
// It is held exactly as the enforcement level is held, in the same directory
// and owned by the same account, for the same reason: an application whose
// provenance nothing states is the application its installer would most like
// to talk the host into taking, so the value that decides it must not be one
// that installer can write.
//
// It decides enrolment and not launch. An application it refuses is left
// unenrolled, which is a state the enforcement level already answers for, so
// the two settings compose instead of each inventing a refusal of its own.
type SignaturePolicy string

const (
	// SignaturesOptional is the default and behaves as cpak always has. A
	// package nobody signed is enrolled, and the record says it was unsigned:
	// the distinction is recorded whether or not this host acts on it.
	SignaturesOptional SignaturePolicy = "optional"

	// SignaturesRequired refuses to enrol an application whose state no
	// identity that may speak for its origin signed. The installation is not
	// undone, because software already on disk and working is not made safer
	// by being half removed; it is left unenrolled and reported as such.
	SignaturesRequired SignaturePolicy = "required"
)

func (p SignaturePolicy) valid() bool {
	switch p {
	case SignaturesOptional, SignaturesRequired:
		return true
	}
	return false
}

// EnforcementStore holds the level and the signature policy. It is a sibling
// of the ledger and is proven the same way the ledger is, because a setting
// anybody could write would let the side a refusal is aimed at decide that
// there is no refusal.
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
	value, found, err := s.setting(enforcementFileName, "enforcement level")
	if err != nil || !found {
		return EnforcementOff, err
	}
	level := EnforcementLevel(value)
	if !level.valid() {
		return EnforcementOff, errors.New("recorded enforcement level is not a level")
	}
	return level, nil
}

func (s EnforcementStore) Set(level EnforcementLevel) error {
	if !level.valid() {
		return errors.New("invalid enforcement level")
	}
	return s.write(enforcementFileName, "the enforcement level", string(level))
}

// SignaturePolicy reads what the administrator set about unsigned packages. A
// policy that is absent reads as optional, which is what a host that was never
// touched has always done. A policy that is present and cannot be trusted is
// an error and not a permission: unlike a launch, an enrolment can be refused
// without taking anything away from a user, so the unreadable case fails on
// the strict side.
func (s EnforcementStore) SignaturePolicy() (SignaturePolicy, error) {
	value, found, err := s.setting(signaturePolicyFileName, "signature policy")
	if err != nil || !found {
		return SignaturesOptional, err
	}
	policy := SignaturePolicy(value)
	if !policy.valid() {
		return SignaturesOptional, errors.New("recorded signature policy is not a policy")
	}
	return policy, nil
}

func (s EnforcementStore) SetSignaturePolicy(policy SignaturePolicy) error {
	if !policy.valid() {
		return errors.New("invalid signature policy")
	}
	return s.write(signaturePolicyFileName, "the signature policy", string(policy))
}

// setting reads one value the owner of the machine wrote. Absent is not an
// error, because a host that never set anything must not answer differently
// from one that has never heard of the setting.
func (s EnforcementStore) setting(name, subject string) (string, bool, error) {
	path, err := s.path(name)
	if err != nil {
		return "", false, err
	}
	if err := validateExistingDirectory(s.Directory, s.OwnerUID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	data, found, err := readTrusted(path, s.OwnerUID, settingSizeLimit, subject)
	if err != nil || !found {
		return "", false, err
	}
	return strings.TrimSpace(string(data)), true, nil
}

func (s EnforcementStore) write(name, subject, value string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := ensureDirectory(s.Directory, s.OwnerUID); err != nil {
		return err
	}
	if err := writeAtomic(path, []byte(value+"\n"), 0644); err != nil {
		return fmt.Errorf("write %s: %w", subject, err)
	}
	return nil
}

// path keeps every setting a sibling of the per-user directories of the
// ledger. A user directory is a number, so no account can ever claim one of
// these names.
func (s EnforcementStore) path(name string) (string, error) {
	if !filepath.IsAbs(s.Directory) {
		return "", errors.New("system authority enforcement path must be absolute")
	}
	return filepath.Join(s.Directory, name), nil
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

// Signatures is what an enrolment asks. It answers with the strict policy when
// the file is there and cannot be trusted, because the alternative is letting
// a broken or replaced file be read as permission to enrol anything.
func Signatures() SignaturePolicy {
	policy, err := DefaultEnforcementStore().SignaturePolicy()
	if err != nil {
		return SignaturesRequired
	}
	return policy
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

// SetSignaturePolicy is the other switch, and it is the owner of the machine's
// decision for the same reason.
//
// It does not walk down to the socket. That transport carries the actions its
// request names and this is not one of them, and it refuses every caller that
// is not root in any case, so a host with no system bus is a host where this
// is set by root and saying so is more use than a transport error.
func SetSignaturePolicy(policy SignaturePolicy) error {
	if !policy.valid() {
		return errors.New("invalid signature policy")
	}
	if os.Geteuid() == 0 {
		return DefaultEnforcementStore().SetSignaturePolicy(policy)
	}
	err := retryPastStale(func() error { return signaturePolicyOverBus(policy) })
	if errors.Is(err, errTransportUnavailable) {
		return ErrNoAuthority
	}
	return err
}

func signaturePolicyOverBus(policy SignaturePolicy) error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	call := connection.Object(BusName, ObjectPath).Call(InterfaceName+".SetSignaturePolicy", 0, string(policy))
	if call.Err == nil {
		return nil
	}
	if unreachableOnBus(call.Err) {
		return errTransportUnavailable
	}
	return fmt.Errorf("set the signature policy: %w", call.Err)
}

// SetSignaturePolicy on the service is declared here, beside the value it
// writes, rather than with the other bus methods: the policy, the transport
// and the question the owner is asked are one thing and nothing else in the
// authority needs to know it exists.
func (s *Service) SetSignaturePolicy(sender dbus.Sender, policy string) *dbus.Error {
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
	wanted := SignaturePolicy(policy)
	if !wanted.valid() {
		return invalidRequest(errors.New("invalid signature policy"))
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, ActionSetSignaturePolicy, map[string]string{
		"signature-policy": policy,
	}); err != nil {
		return denied(err)
	}
	if err := s.Enforcement.SetSignaturePolicy(wanted); err != nil {
		return failed(err)
	}
	return nil
}

func applyEnforcement(store EnforcementStore, message socketRequest) error {
	if message.Action != enforcementSetAction {
		return errors.New("unsupported system authority action")
	}
	return store.Set(EnforcementLevel(message.Level))
}
