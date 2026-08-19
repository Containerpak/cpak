/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/trustpolicy"
)

const (
	// ActionSetTrustPolicy is its own action because it is its own decision.
	// The enforcement level says what happens to software nobody enrolled and
	// the signature policy says whether an unsigned package may be enrolled at
	// all; this one says who may publish for this host and which origins it
	// takes software from, which is the decision that stops a managed machine
	// being one install away from arbitrary software.
	ActionSetTrustPolicy = "it.cpak.system.set-trust-policy"

	// The siblings of this file hold one word each and are named after it.
	// This one holds a document, and the name says so, because an
	// administrator deploying a fleet writes it by hand or by configuration
	// management and has to be able to tell what belongs in it.
	trustPolicyFileName = "trust.json"

	// A signer list, an origin list and a revocation list, for an organisation
	// and not for one package. The cap is what stops whoever wrote the file
	// from deciding how much a reader reads.
	trustPolicySizeLimit = 64 << 10
)

// ErrTrustRefused reports an enrolment the administrator's own policy refuses.
// The caller has to tell it from a failure, because the answer is to change
// the policy or the package and never to try again. Which rule refused is in
// the message: a decision that could not say why would be indistinguishable
// from a machine that is simply broken.
var ErrTrustRefused = errors.New("the host trust policy refuses this enrolment")

// verifyApproval is the counter-signature check, and it is deliberately not
// implemented here. What it has to prove is that an identity other than the
// publisher signed the exact state this enrolment records, which is the origin,
// the image digest, the manifest digest and the publisher generation the anchor
// is bound to, and not merely the origin: an approval that covered an origin
// would approve every release of it, including the ones nobody looked at.
//
// It is nil until the file that owns approvals assigns it, and a host that
// requires an approval refuses while it is nil rather than pretending the
// second party spoke.
var verifyApproval func(Enrolment) (signature.Verified, error)

// TrustStore holds what the administrator decided. It is a sibling of the
// ledger and of the enforcement level and is proven the same way both are: a
// policy the launching account could write would let the side a refusal is
// aimed at decide that there is no refusal, which is the whole reason this
// lives here and not in a package manifest or a home directory.
type TrustStore struct {
	Directory string
	OwnerUID  uint32
}

func DefaultTrustStore() TrustStore {
	return TrustStore{Directory: DefaultAnchorDirectory, OwnerUID: 0}
}

// Policy reads what the administrator decided.
//
// A host where nobody decided anything answers with the empty policy and no
// error, and the empty policy allows everything: every installation that
// exists today has no policy, so a default that refused anything would be a
// broken release rather than a control.
//
// A policy that is there and cannot be trusted answers with the empty policy
// and an error, the way the signature policy already does. The two callers
// then part company on purpose: an enrolment propagates the error and refuses,
// because refusing an enrolment costs a user nothing a reinstall does not give
// back, while a client that only wants to explain itself reads the value and
// gets the same answer an unmanaged host would give.
func (s TrustStore) Policy() (trustpolicy.Policy, error) {
	path, err := s.path()
	if err != nil {
		return trustpolicy.Policy{}, err
	}
	if err := validateExistingDirectory(s.Directory, s.OwnerUID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return trustpolicy.Policy{}, nil
		}
		return trustpolicy.Policy{}, err
	}
	data, found, err := readTrusted(path, s.OwnerUID, trustPolicySizeLimit, "trust policy")
	if err != nil || !found {
		return trustpolicy.Policy{}, err
	}
	return decodeTrustPolicy(string(data))
}

// Clear removes the policy, which returns the host to one where nobody has
// decided anything and everything is allowed. An administrator who cannot undo
// a control will not turn it on.
func (s TrustStore) Clear() error {
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the trust policy: %w", err)
	}
	return nil
}

func (s TrustStore) Set(policy trustpolicy.Policy) error {
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("invalid trust policy: %w", err)
	}
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := ensureDirectory(s.Directory, s.OwnerUID); err != nil {
		return err
	}
	document, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the trust policy: %w", err)
	}
	if err := writeAtomic(path, append(document, '\n'), 0644); err != nil {
		return fmt.Errorf("write the trust policy: %w", err)
	}
	return nil
}

// path keeps the policy a sibling of the per-user directories of the ledger. A
// user directory is a number, so no account can ever claim this name.
func (s TrustStore) path() (string, error) {
	if !filepath.IsAbs(s.Directory) {
		return "", errors.New("system authority trust policy path must be absolute")
	}
	return filepath.Join(s.Directory, trustPolicyFileName), nil
}

// decodeTrustPolicy refuses a document it does not fully understand. An
// unknown field is a decision this build cannot act on, and a policy obeyed in
// part is not the policy the administrator wrote: an organisation that adds a
// rule a host is too old to know about must be told, not silently obeyed on
// the rules that happen to be recognised.
func decodeTrustPolicy(document string) (trustpolicy.Policy, error) {
	if len(document) > trustPolicySizeLimit {
		return trustpolicy.Policy{}, errors.New("trust policy is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.DisallowUnknownFields()
	policy := trustpolicy.Policy{}
	if err := decoder.Decode(&policy); err != nil {
		return trustpolicy.Policy{}, fmt.Errorf("decode the trust policy: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return trustpolicy.Policy{}, errors.New("trust policy contains multiple JSON values")
	}
	if err := policy.Validate(); err != nil {
		return trustpolicy.Policy{}, fmt.Errorf("invalid trust policy: %w", err)
	}
	return policy, nil
}

// admitTrust is where an administrator's decision stops being advice.
//
// It runs in the authority, on the one call every transport ends at, so no
// client is ever the thing that decides it: the bus, the socket and root all
// arrive at Record, and a caller that skipped the check would be a caller that
// skipped writing the ledger. A client may read the same policy to explain
// itself, and what it reads changes nothing.
//
// An empty policy returns before anything is decided at all. That is not an
// optimisation: it is the guarantee that a host nobody manages records exactly
// what it records today.
func (l AnchorLedger) admitTrust(enrolment Enrolment) error {
	policy, err := TrustStore{Directory: l.Directory, OwnerUID: l.OwnerUID}.Policy()
	if err != nil {
		return fmt.Errorf("read the host trust policy: %w", err)
	}
	if policy.Empty() {
		return nil
	}
	if decision := policy.AllowsOrigin(enrolment.Origin); !decision.Allowed {
		return refusedBy(decision)
	}
	if err := admitPublisher(policy, enrolment); err != nil {
		return err
	}
	if decision := policy.IsRevoked(enrolment.Origin, publisherGeneration(enrolment)); !decision.Allowed {
		return refusedBy(decision)
	}
	return admitApproval(policy, enrolment)
}

// admitPublisher puts the identity that signed to the list the administrator
// wrote, which is the part the publisher signature on its own never answered.
// A package signed by its own repository proves it came from where it says it
// came from, and every package can say that of itself; who may publish for
// this host is a decision only the owner of the host can make.
func admitPublisher(policy trustpolicy.Policy, enrolment Enrolment) error {
	verified, err := enrolment.Signer()
	if errors.Is(err, ErrUnsigned) {
		if policy.RequirePublisher {
			return fmt.Errorf("%w: %s is signed by nobody and this host enrols only what an approved publisher signed",
				ErrTrustRefused, enrolment.Origin)
		}
		return nil
	}
	if err != nil {
		return err
	}
	decision := policy.AllowsPublisher(verified.Identity.Issuer, verified.Identity.Repo, enrolment.Origin)
	if !decision.Allowed {
		return refusedBy(decision)
	}
	return nil
}

// admitApproval is the second party. A publisher signature is the publisher
// asserting itself; an approval is this organisation saying it looked at this
// exact state and counter-signed it, which is the only part of the chain the
// publisher cannot produce on its own behalf.
//
// A build with no approval check refuses instead of passing. Answering that an
// approval holds because nothing looked for one is the failure this whole
// round exists to remove.
func admitApproval(policy trustpolicy.Policy, enrolment Enrolment) error {
	if !policy.RequireApproval {
		return nil
	}
	if verifyApproval == nil {
		return fmt.Errorf("%w: this build cannot check the counter-signature %s requires", ErrTrustRefused, enrolment.Origin)
	}
	verified, err := verifyApproval(enrolment)
	if err != nil {
		return fmt.Errorf("%w: the approval of %s does not stand: %w", ErrTrustRefused, enrolment.Origin, err)
	}
	decision := policy.AllowsApproval(verified.Identity.Issuer, verified.Identity.Repo)
	if !decision.Allowed {
		return refusedBy(decision)
	}
	return nil
}

// publisherGeneration is the number a revocation names. It is the publisher's
// counter and never the anchor's: the anchor's counts installations on this
// machine, so revoking by it would mean something different on every host,
// while the publisher's names one release the same way everywhere.
//
// An enrolment nobody signed names no release, so only a revocation of the
// whole origin can reach it. A host that wants to withdraw single releases has
// to require a publisher first, and that is a decision it makes explicitly.
func publisherGeneration(enrolment Enrolment) uint64 {
	if enrolment.Signature == nil {
		return 0
	}
	return enrolment.Signature.State.Generation
}

// refusedBy carries the reason through and never summarises it. An
// administrator reading a log has to be able to tell which of their own rules
// refused, and a user has to be told something other than that it failed.
func refusedBy(decision trustpolicy.Decision) error {
	return fmt.Errorf("%w: %s", ErrTrustRefused, decision.Reason)
}

// TrustPolicy is what a client reads to explain itself before it asks for
// anything. Reading takes no privilege, exactly as reading the ledger takes
// none, and the answer it gives is not the one that matters: the authority
// asks the same question again when it records, and only its answer decides.
func TrustPolicy() (trustpolicy.Policy, error) {
	return DefaultTrustStore().Policy()
}

// SetTrustPolicy writes what the administrator decided, and it is privileged
// for both directions of the change: naming who may publish protects every
// account on the host, and withdrawing that protection is the same decision
// read backwards.
//
// It does not walk down to the socket, for the reason the signature policy
// does not: that transport carries the actions its request names and this is
// not one of them, and it refuses every caller that is not root in any case,
// so a host with no system bus is a host where this is set by root and saying
// so is more use than a transport error.
func SetTrustPolicy(policy trustpolicy.Policy) error {
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("invalid trust policy: %w", err)
	}
	if os.Geteuid() == 0 {
		return DefaultTrustStore().Set(policy)
	}
	err := retryPastStale(func() error { return trustPolicyOverBus(policy) })
	if errors.Is(err, errTransportUnavailable) {
		return ErrNoAuthority
	}
	return err
}

func trustPolicyOverBus(policy trustpolicy.Policy) error {
	document, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("encode the trust policy: %w", err)
	}
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	call := connection.Object(BusName, ObjectPath).Call(InterfaceName+".SetTrustPolicy", 0, string(document))
	if call.Err == nil {
		return nil
	}
	if unreachableOnBus(call.Err) {
		return errTransportUnavailable
	}
	return fmt.Errorf("set the trust policy: %w", call.Err)
}

// SetTrustPolicy on the service is declared here, beside the value it writes,
// rather than with the other bus methods: the policy, the transport and the
// question the owner is asked are one thing.
//
// The details put to polkit are counts and not the document. What the owner is
// being asked is whether this caller may decide who publishes for the machine,
// and an authentication prompt that scrolled a fleet policy past them would be
// read by nobody.
func (s *Service) SetTrustPolicy(sender dbus.Sender, document string) *dbus.Error {
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
	policy, err := decodeTrustPolicy(document)
	if err != nil {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, ActionSetTrustPolicy, map[string]string{
		"approved-origins": strconv.Itoa(len(policy.ApprovedOrigins)),
		"approved-signers": strconv.Itoa(len(policy.ApprovedSigners)),
		"revocations":      strconv.Itoa(len(policy.Revoked)),
	}); err != nil {
		return denied(err)
	}
	if err := s.Trust.Set(policy); err != nil {
		return failed(err)
	}
	return nil
}
