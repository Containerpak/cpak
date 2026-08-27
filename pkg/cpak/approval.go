/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
	"github.com/mirkobrombin/cpak/pkg/signature"
)

// This is the second party.
//
// A publisher signature answers one question: did this come from the
// repository it says it came from. The repository in the certificate is put
// next to the origin cpak installs from, and every package is published by its
// own repository, so that check can only ever agree with the package. It is
// worth having and it decides nothing, because nobody but the publisher has
// said anything.
//
// An approval is a signature over the SAME signature.State the publisher
// signed, made by an identity that is not the publisher's. It says the one
// thing a publisher cannot say about itself: somebody else looked at this
// exact state and stands behind it. A host that requires an approval is no
// longer taking the package's word for the package.
//
// The state is reused rather than a second format invented for this. A
// counter-signature means "the same bytes", so the bytes have to be the same
// ones, and a second encoding would be a second set of rules to disagree
// about. The bundle is verified through the same offline check, against the
// same trust root, and the only difference is which identity comes back.
//
// It is attached as its own OCI referrer, under its own artifact type. Nothing
// the publisher signed is touched, no artifact is rebuilt, and an approval can
// be made months after a release by an organisation that holds no key of the
// publisher's. Two artifact types and not one, because the two are read by
// different rules and a verifier that could not tell them apart would take an
// approval for a publisher signature.
//
// Nothing here decides whether an approval is required, or whose approval
// counts. Those are the administrator's, they are read from where the
// launching account cannot write them, and this file only asks.

// approvalArtifactType is how cpak recognises a counter-signature among
// everything else that can hang off an image, and is the same string cpak-sign
// attaches one under.
const approvalArtifactType = "application/vnd.cpak.approval.v1+json"

var (
	// ErrPackageUnapproved means the registry holds no approval at all against
	// the image that was resolved. It is the absence of a check and not a
	// failed one, and a host that refuses it is refusing unapproved software
	// on purpose.
	ErrPackageUnapproved = errors.New("no approval is attached to the package")

	// ErrApprovalUnverified means something is attached and does not stand for
	// the state that was resolved: a bundle that does not hold, one made over
	// a different manifest, image or generation, or one nothing on this
	// machine can name the state of.
	ErrApprovalUnverified = errors.New("the approval does not verify")

	// ErrApprovalUnauthorised means an approval holds and was made by an
	// identity no administrator of this host approved of. It is the one
	// failure that says the counter-signature is real and is somebody else's.
	ErrApprovalUnauthorised = errors.New("the approval was made by an identity no administrator approved")

	// ErrApprovalNotIndependent means every approval attached was made by the
	// publisher of the package. They verify like any other, and they are the
	// publisher asserting itself a second time, which is the thing an approval
	// exists to stop being sufficient.
	ErrApprovalNotIndependent = errors.New("the only approvals were made by the publisher of the package")

	// errUnnamedGeneration is the one thing that stops an approval being
	// checked at all: neither the caller nor the referrer names the generation
	// of the state it covers, and any number cpak chose would be a number cpak
	// then checked a signature against.
	errUnnamedGeneration = errors.New("nothing names the generation this approval covers")
)

// Approval is one counter-signature that held: the exact state it covers, the
// identity that made it, and the bundle it was proven from, so that a caller
// recording it hands on the evidence and not its own conclusion.
type Approval struct {
	Identity signature.Identity
	State    signature.State
	Bundle   []byte
}

// Publisher reports whether the approval was made by an identity that may
// speak for the origin itself.
//
// Such an approval verifies like any other and is worth nothing: it is the
// publisher asserting itself a second time, which is exactly what an approval
// exists to stop being sufficient. ApprovalsOf still reports it, because a
// report of what is attached that hid some of it would be a worse report;
// ApprovedState will not act on it, and cpak-sign refuses to make one.
func (a Approval) Publisher() bool {
	return a.Identity.MatchesOrigin(a.State.Origin)
}

// ApprovalAuthority answers whether an identity may counter-sign for this
// host. It is a question and never a decision made here: the list of
// approved signers lives where the launching account cannot write it.
//
// A nil authority is a host on which no administrator has decided anything, so
// nothing is refused for want of an approver.
type ApprovalAuthority func(issuer, repo string) bool

// ApprovalsOf reports every approval attached to the image an installation
// resolved that holds against the state it named, in the order the registry
// lists them.
//
// The repository is taken from the installation the digest names and not from
// the origin, the same way a publisher signature is found: one origin can be
// installed several times from several branches, and answering about another
// of them would report an approval of a package nobody asked about.
func (c *Cpak) ApprovalsOf(origin string, state signature.State) ([]Approval, error) {
	ref, err := c.installedImageReference(origin, state.ImageDigest)
	if err != nil {
		return nil, err
	}
	return c.approvalsOf(ref, origin, state)
}

// ApprovedState is the approval a host that requires one may act on: it holds
// against the state, it was not made by the publisher, and the identity that
// made it is one the administrator approved of.
//
// The failures stay apart because a host has to answer them differently.
// Nothing attached is a package this organisation has never looked at, and it
// is what every package in the world is until somebody approves it. Something
// attached that does not hold is an approval that is stale, forged, or for
// another release. An approval by an identity nobody approved is a real
// counter-signature made by the wrong organisation, which is the one case
// where the artifact is authentic and is still not evidence for this host.
// Only the publisher's own is the package approving itself, which is where
// this round started.
func (c *Cpak) ApprovedState(origin string, state signature.State, allowed ApprovalAuthority) (Approval, error) {
	approvals, err := c.ApprovalsOf(origin, state)
	if err != nil {
		return Approval{}, err
	}
	return authorisedApproval(normalizePackageOrigin(origin), approvals, allowed)
}

func (c *Cpak) approvalsOf(ref oci.Reference, origin string, state signature.State) ([]Approval, error) {
	origin = normalizePackageOrigin(origin)
	if origin == "" {
		return nil, errors.New("read the approvals of a package: the origin is required")
	}
	if state.Origin != origin {
		return nil, fmt.Errorf("read the approvals of %s: the state names %q", origin, state.Origin)
	}
	if !isResolvedImageDigest(state.ImageDigest) {
		return nil, fmt.Errorf("read the approvals of %s: %q is not a resolved image digest", origin, state.ImageDigest)
	}
	attached, err := c.attachedApprovals(ref, origin, state.ImageDigest)
	if err != nil {
		return nil, err
	}
	if len(attached) == 0 {
		return nil, fmt.Errorf("read the approvals of %s: %w", origin, ErrPackageUnapproved)
	}

	// Every attached approval is put to the same question. Several are the
	// ordinary case and not a conflict: an organisation that re-approves a
	// release leaves the earlier one beside the new one, and a package can be
	// approved by more than one party.
	approvals := make([]Approval, 0, len(attached))
	var refusal error
	for _, candidate := range attached {
		covered, nameErr := coveredState(state, candidate.generation)
		if nameErr != nil {
			refusal = firstReason(refusal, nameErr)
			continue
		}
		verified, verifyErr := verifyApprovalSignature(candidate.bundle, covered)
		if verifyErr != nil {
			refusal = firstReason(refusal, verifyErr)
			continue
		}
		approvals = append(approvals, Approval{Identity: verified.Identity, State: covered, Bundle: candidate.bundle})
	}
	if len(approvals) > 0 {
		return approvals, nil
	}
	// Every candidate ended in an approval or in a reason, so a run that found
	// no approval is holding one.
	return nil, fmt.Errorf("read the approvals of %s: %w: %w", origin, ErrApprovalUnverified, refusal)
}

// coveredState is the exact state one attached approval is checked against.
//
// The generation is the publisher's counter and no installing machine holds
// it. A caller that already knows it is asking about that state and nothing a
// registry serves may move it; a caller that does not takes it from the
// referrer, where it is a hint and never evidence, because it goes straight
// into the state the approval then has to cover and a wrong value produces a
// refusal rather than an acceptance.
func coveredState(state signature.State, declared uint64) (signature.State, error) {
	if state.Generation != 0 {
		return state, nil
	}
	if declared == 0 {
		return signature.State{}, errUnnamedGeneration
	}
	state.Generation = declared
	return state, nil
}

// attachedApprovals reads every approval a registry holds against one resolved
// image, with the generation its referrer declares.
//
// A referrer that names no generation is kept rather than skipped, because a
// caller that already holds the publisher's state does not need one, and only
// the caller knows that.
func (c *Cpak) attachedApprovals(ref oci.Reference, origin, imageDigest string) ([]attachedSignature, error) {
	client := &oci.Client{Credentials: registryauth.Provider{Origin: origin, Path: c.Options.RegistryAuthPath}}
	referrers, err := client.Referrers(c.Ctx, ref, imageDigest, approvalArtifactType)
	if err != nil {
		return nil, fmt.Errorf("list the approvals of %s@%s: %w", ref.ContextName(), imageDigest, err)
	}
	attached := make([]attachedSignature, 0, len(referrers))
	for _, referrer := range referrers {
		generation, _ := signedGeneration(referrer)
		bundle, payloadErr := client.ReferrerPayload(c.Ctx, ref, referrer, maxSignatureBundle)
		if payloadErr != nil {
			return nil, fmt.Errorf("read the approval %s of %s: %w", referrer.Digest, ref.ContextName(), payloadErr)
		}
		attached = append(attached, attachedSignature{generation: generation, bundle: bundle})
	}
	return attached, nil
}

// authorisedApproval picks the approval this host may act on.
//
// The publisher's own counter-signatures are dropped before anything else is
// asked, and dropped rather than refused outright, because an image can carry
// one beside a real one: answering with the publisher's because a registry
// listed it first would turn a package a second party did approve into a
// package approved by nobody.
//
// A nil authority is a host where no administrator has decided anything, which
// must behave as every host behaved before there was a policy at all: nothing
// is refused for want of a name on a list.
func authorisedApproval(origin string, approvals []Approval, allowed ApprovalAuthority) (Approval, error) {
	independent := make([]Approval, 0, len(approvals))
	for _, approval := range approvals {
		if approval.Publisher() {
			continue
		}
		independent = append(independent, approval)
	}
	if len(independent) == 0 {
		return Approval{}, fmt.Errorf("read the approvals of %s: %w", origin, ErrApprovalNotIndependent)
	}
	for _, approval := range independent {
		if allowed == nil || allowed(approval.Identity.Issuer, approval.Identity.Repo) {
			return approval, nil
		}
	}
	return Approval{}, fmt.Errorf("read the approvals of %s: %w: %s", origin, ErrApprovalUnauthorised, whoApproved(independent))
}

// whoApproved names who did approve, so that an administrator reading a
// refusal can see whose word is on the package and decide whether to approve
// of it, instead of being told only that nobody they named did.
func whoApproved(approvals []Approval) string {
	named := make([]string, 0, len(approvals))
	for _, approval := range approvals {
		named = append(named, whoseApproval(approval.Identity.Repo))
	}
	return "it was approved by " + strings.Join(named, ", ")
}

// whoseApproval names one approving identity. A certificate that names no
// repository at all is a different sentence from one that names the wrong
// repository, and a report that printed an empty string for the first would
// read as though nobody had looked.
func whoseApproval(repo string) string {
	if repo == "" {
		return "an identity whose certificate names no repository"
	}
	return fmt.Sprintf("%q", repo)
}

// firstReason keeps the first thing that went wrong. A later candidate that
// failed for another reason is not more informative, and replacing the reason
// as the loop went on would report whichever approval the registry happened to
// list last.
func firstReason(kept, reason error) error {
	if kept != nil {
		return kept
	}
	return reason
}
