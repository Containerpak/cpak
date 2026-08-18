/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package trustpolicy is what an administrator decides about the software a
// host may run, written down apart from the software so that nothing a package
// says can change it.
//
// It exists because a publisher signature on its own is an end in itself. It
// proves a package came from the repository it says it came from, and every
// package satisfies that, including one an attacker publishes from a repository
// they own. Nothing in it says which publishers an organisation is willing to
// run, nothing but the publisher asserts anything about a release, and nothing
// can be taken back once a release turns out badly. This package is the other
// party: who may publish, which origins may be installed at all, who may
// counter-sign that this organisation checked this exact state, and what has
// since been revoked.
//
// It decides and it never reads. There is no file, no socket and no default
// path in here, because where a policy is held is the whole of its authority
// and that belongs beside the anchor ledger, owned by the account a launch
// cannot write as. A Policy handed to this package is one somebody else has
// already proven they may act on.
//
// A Policy nobody filled in is the host nobody manages, and it allows
// everything. That is not a convenience. Every installation that exists today
// has no policy, so a default that refused anything would be a broken release
// and not a strict one.
package trustpolicy

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ABIVersion changes when the meaning of a policy changes, so a policy written
// under one set of rules is never enforced under another. It is neither the
// signature ABI nor the integrity ABI: the three describe different things and
// move for different reasons.
const ABIVersion = 1

// reasonLimit caps how much administrator prose travels with a revocation. The
// reason is printed where an application refused to start, so it has to fit in
// a sentence a person reads, and a policy is not a place to keep a document.
const reasonLimit = 200

// ErrInvalidPolicy reports a policy that cannot mean anything. It is a sentinel
// because whoever loads a policy has to tell a policy this build could not
// understand from a policy that refuses something: the first is an
// administrator's mistake and has to be reported to them, the second is the
// control working.
var ErrInvalidPolicy = errors.New("trustpolicy: policy is not well formed")

// Signer is an identity an administrator approves. Empty fields mean "any".
type Signer struct {
	Issuer string `json:"issuer,omitempty"`
	Repo   string `json:"repo,omitempty"`
}

// Policy is what an administrator decides. It is read where the launching
// account cannot write it and is never derived from a package.
type Policy struct {
	ABI int `json:"abi"`

	// RequirePublisher and RequireApproval are separate on purpose: the first
	// says a package must be signed by someone approved, the second says this
	// organisation must have counter-signed this exact state.
	RequirePublisher bool `json:"require_publisher"`
	RequireApproval  bool `json:"require_approval"`

	ApprovedSigners []Signer     `json:"approved_signers,omitempty"`
	ApprovedOrigins []string     `json:"approved_origins,omitempty"`
	ApprovalSigners []Signer     `json:"approval_signers,omitempty"`
	Revoked         []Revocation `json:"revoked,omitempty"`
}

// Revocation withdraws trust from what was already approved. A revocation with
// no generation withdraws every generation of that origin.
type Revocation struct {
	Origin     string `json:"origin"`
	Generation uint64 `json:"generation,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Decision is the answer, and it always carries why.
//
// Allowed means one thing in every decision here: what was asked about may go
// ahead. It never means "yes, that is so", which matters most at IsRevoked,
// where Allowed false is the withdrawal and not the absence of one. Reason is
// filled in whether the answer is yes or no, because an administrator working
// out why an application started is doing the same work as one working out why
// it did not.
type Decision struct {
	Allowed bool
	Reason  string
}

// Empty reports whether an administrator has decided anything at all. An empty
// policy must allow everything, so an unmanaged host behaves as it does today.
//
// It answers about the decisions and deliberately not about the ABI, which says
// how to read them rather than what they are. It is also not a licence to skip
// Validate: a policy from a later cpak can look empty here precisely because
// this build cannot see what it decided, which is why the decisions refuse one.
func (p Policy) Empty() bool {
	return !p.RequirePublisher &&
		!p.RequireApproval &&
		len(p.ApprovedSigners) == 0 &&
		len(p.ApprovedOrigins) == 0 &&
		len(p.ApprovalSigners) == 0 &&
		len(p.Revoked) == 0
}

// Validate refuses a policy that cannot mean anything, so that an administrator
// hears about it while they are writing it rather than through an application
// that will not start.
//
// The zero Policy is valid and is the absence of a policy. Anything that
// decides something has to name the ABI it was written for: a policy from a
// later cpak, whose fields this build cannot see, would otherwise read as an
// empty one, and a control that switches itself off when it is not understood
// is not a control.
func (p Policy) Validate() error {
	if p.ABI == 0 && p.Empty() {
		return nil
	}
	if p.ABI != ABIVersion {
		return fmt.Errorf("%w: unsupported policy abi %d", ErrInvalidPolicy, p.ABI)
	}
	// A require flag with nothing that could satisfy it refuses everything for
	// ever, which nobody sets on purpose and which looks, at a launch, exactly
	// like the control working.
	if p.RequirePublisher && len(p.ApprovedSigners) == 0 {
		return fmt.Errorf("%w: require_publisher is set and approved_signers is empty, so nothing could ever be published", ErrInvalidPolicy)
	}
	if p.RequireApproval && len(p.ApprovalSigners) == 0 {
		return fmt.Errorf("%w: require_approval is set and approval_signers is empty, so nothing could ever be approved", ErrInvalidPolicy)
	}
	if err := validateSigners("approved_signers", p.ApprovedSigners, false); err != nil {
		return err
	}
	// An approval identity that names no repository is every identity that
	// issuer will ever certify, which gives away the whole of the second
	// opinion. Leaving the list out already means nobody has to counter-sign,
	// so an entry that matches everybody is a mistake and never a decision.
	if err := validateSigners("approval_signers", p.ApprovalSigners, true); err != nil {
		return err
	}
	for _, origin := range p.ApprovedOrigins {
		if err := validateOrigin("approved_origins", origin); err != nil {
			return err
		}
	}
	for _, revocation := range p.Revoked {
		if err := validateOrigin("revoked", revocation.Origin); err != nil {
			return err
		}
		if err := validateReason(revocation.Reason); err != nil {
			return err
		}
	}
	return nil
}

func validateSigners(field string, signers []Signer, repoRequired bool) error {
	for _, signer := range signers {
		if signer.Issuer == "" && signer.Repo == "" {
			return fmt.Errorf("%w: a %s entry names nobody, and leaving the list out already means anybody", ErrInvalidPolicy, field)
		}
		if signer.Issuer != "" && !validIssuer(signer.Issuer) {
			return fmt.Errorf("%w: %s issuer %q is not an https oidc issuer", ErrInvalidPolicy, field, signer.Issuer)
		}
		if signer.Repo == "" {
			if repoRequired {
				return fmt.Errorf("%w: a %s entry names no repository, so every identity that issuer certifies could counter-sign", ErrInvalidPolicy, field)
			}
			continue
		}
		if err := validateOrigin(field, signer.Repo); err != nil {
			return err
		}
	}
	return nil
}

// validateOrigin holds an origin to the shape it is compared in and not merely
// to one that folds onto it. What an administrator reads in the policy has to
// be the string the decisions use, or the file says one thing and the host does
// another.
func validateOrigin(field, origin string) error {
	canonical, ok := canonicalOrigin(origin)
	if !ok {
		return fmt.Errorf("%w: %s entry %q is not a host/owner/repository origin", ErrInvalidPolicy, field, origin)
	}
	if canonical != origin {
		return fmt.Errorf("%w: %s entry %q is not written the way it is compared, which is %q", ErrInvalidPolicy, field, origin, canonical)
	}
	return nil
}

func validateReason(reason string) error {
	if len(reason) > reasonLimit {
		return fmt.Errorf("%w: a revocation reason is printed at a refused launch, so it may be at most %d characters and this one is %d", ErrInvalidPolicy, reasonLimit, len(reason))
	}
	if strings.ContainsFunc(reason, unicode.IsControl) {
		return fmt.Errorf("%w: a revocation reason is printed at a refused launch and holds a control character: %q", ErrInvalidPolicy, reason)
	}
	return nil
}
