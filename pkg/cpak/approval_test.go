/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// What these tests hold cpak to is the difference between a signature and an
// approval. A publisher signature is checked against the origin it was
// installed from, so it can only ever agree with the package. An approval is
// checked against a list this machine keeps, so it can disagree, and the three
// ways it can fail have to stay three answers: nothing approved this, what
// approved it does not hold, and what approved it is not who this host trusts
// to approve anything.

const approverOrigin = "github.com/acme/security"

// approverIdentity is what a certificate issued to the CI of an organisation
// names. It is not the publisher's, which is the entire point of it.
func approverIdentity(repo string) signature.Identity {
	return signature.Identity{
		Issuer:  githubActionsIssuer,
		Subject: "https://" + repo + "/.github/workflows/approve.yml@refs/heads/main",
		Repo:    repo,
	}
}

// attachApproval files a counter-signature against an image the way cpak-sign
// approve does: its own artifact type, and the generation of the state it
// covers in the annotation a registry copies onto its referrers index. A
// generation of zero attaches no annotation, which is the referrer of a tool
// that did not write one.
func attachApproval(t *testing.T, registry *signatureRegistry, subject string, generation uint64, bundle []byte) {
	t.Helper()

	registry.attach(t, subject, approvalArtifactType, bundle)
	if generation == 0 {
		return
	}
	attached := registry.referrers[subject]
	attached[len(attached)-1].Annotations = map[string]string{
		signedGenerationAnnotation: strconv.FormatUint(generation, 10),
	}
}

// approvedInstallation is the package of these tests as an install leaves it:
// one application of the test origin, resolved to a digest the test registry
// answers for.
func approvedInstallation(t *testing.T, cp *Cpak, ref oci.Reference, imageDigest string) {
	t.Helper()

	seedApplication(t, cp, types.Application{
		CpakId:      testCpakId("branch", "main"),
		Name:        "demo",
		Version:     "main",
		Branch:      "main",
		Origin:      testOrigin,
		Image:       ref.Name(),
		ImageDigest: imageDigest,
	})
}

// approves is an offline check that accepts one bundle and names who made it.
func approves(bundle string, identity signature.Identity) func([]byte, signature.State) (signature.Verified, error) {
	return func(given []byte, against signature.State) (signature.Verified, error) {
		if string(given) != bundle {
			return signature.Verified{}, errors.New("this bundle does not cover the state")
		}
		return signature.Verified{State: against, Identity: identity}, nil
	}
}

// The whole of what an approval is: a second signature over the exact state
// the publisher signed, made by somebody who is not the publisher, found
// through the installation the digest names and checked against that state
// unchanged.
func TestApprovalsOfReportsWhoCounterSignedTheStateTheInstallationResolved(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	bundle := []byte(`{"bundle":"the one the organisation attached"}`)
	attachApproval(t, registry, resolved, 4, bundle)
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)

	var checkedBundle []byte
	var checkedState signature.State
	useSignatureVerifier(t, func(given []byte, against signature.State) (signature.Verified, error) {
		checkedBundle = given
		checkedState = against
		return signature.Verified{State: against, Identity: approverIdentity(approverOrigin)}, nil
	})

	approvals, err := cp.ApprovalsOf(testOrigin, wantedState(t, resolved, 4))
	if err != nil {
		t.Fatalf("an approval that holds was refused: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("got %d approvals, want the one that is attached", len(approvals))
	}
	if approvals[0].Identity.Repo != approverOrigin {
		t.Fatalf("got approver %+v, want the identity the certificate names", approvals[0].Identity)
	}
	if string(checkedBundle) != string(bundle) {
		t.Fatalf("got bundle %q, want the one the registry served %q", checkedBundle, bundle)
	}
	if checkedState != wantedState(t, resolved, 4) {
		t.Fatalf("got state %+v, want the state the installation resolved %+v", checkedState, wantedState(t, resolved, 4))
	}
	if string(approvals[0].Bundle) != string(bundle) {
		t.Fatalf("got evidence %q, want the bundle the approval was proven from", approvals[0].Bundle)
	}
}

// Nothing approved this and something approved it badly are two different
// facts about a package. A host that requires approvals answers the first with
// "nobody has looked at this yet" and the second with "what looked at it does
// not stand", and it can only do that if they never collapse into one error.
func TestApprovalsOfSeparatesNothingAttachedFromAnApprovalThatDoesNotHold(t *testing.T) {
	resolved := contentDigest([]byte("the image that was resolved"))

	bare := newSignatureCpak(t)
	empty := newSignatureRegistry()
	emptyRef := empty.start(t)
	approvedInstallation(t, bare, emptyRef, resolved)
	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		t.Fatal("the offline check ran for a package nothing is attached to")
		return signature.Verified{}, nil
	})
	_, err := bare.ApprovalsOf(testOrigin, wantedState(t, resolved, 4))
	if !errors.Is(err, ErrPackageUnapproved) {
		t.Fatalf("got %v, want an unapproved package to be reported as %v", err, ErrPackageUnapproved)
	}
	if errors.Is(err, ErrApprovalUnverified) || errors.Is(err, ErrApprovalUnauthorised) {
		t.Fatalf("got %v, want an unapproved package not to be read as a failed check", err)
	}

	held := newSignatureCpak(t)
	registry := newSignatureRegistry()
	attachApproval(t, registry, resolved, 4, []byte("an approval of something else"))
	ref := registry.start(t)
	approvedInstallation(t, held, ref, resolved)
	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("the certificate does not cover this state")
	})
	_, err = held.ApprovalsOf(testOrigin, wantedState(t, resolved, 4))
	if !errors.Is(err, ErrApprovalUnverified) {
		t.Fatalf("got %v, want an approval that does not hold to be reported as %v", err, ErrApprovalUnverified)
	}
	if errors.Is(err, ErrPackageUnapproved) {
		t.Fatalf("got %v, want a failed check not to be read as an unapproved package", err)
	}
	if !strings.Contains(err.Error(), "the certificate does not cover this state") {
		t.Fatalf("got %v, want the reason the offline check gave to survive", err)
	}
}

// The publisher's own signature hangs off the same image. A registry is
// allowed to ignore the artifactType filter, so what comes back unfiltered is
// filtered again here, and a publisher signature must never be counted as an
// approval: that would make every signed package approved by itself.
func TestApprovalsOfDoesNotReadThePublisherSignatureAsAnApproval(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, resolved, 4, []byte(`{"bundle":"the publisher's"}`))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)

	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		t.Fatal("a publisher signature was handed to the check as an approval")
		return signature.Verified{}, nil
	})

	approvals, err := cp.ApprovalsOf(testOrigin, wantedState(t, resolved, 4))
	if !errors.Is(err, ErrPackageUnapproved) {
		t.Fatalf("got %d approvals %v, want a signed and unapproved package to be reported as %v", len(approvals), err, ErrPackageUnapproved)
	}
}

// A host may require an approval without requiring a publisher signature, and
// then nothing on the machine holds the publisher's counter. The referrer
// supplies it, which is safe for one reason only: it goes straight into the
// state the approval then has to cover, so a wrong value is a refusal.
func TestApprovalsOfTakesTheGenerationFromTheReferrerWhenNothingElseNamesIt(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 7, []byte("an approval of generation seven"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)

	var checked signature.State
	useSignatureVerifier(t, func(_ []byte, against signature.State) (signature.Verified, error) {
		checked = against
		return signature.Verified{State: against, Identity: approverIdentity(approverOrigin)}, nil
	})

	approvals, err := cp.ApprovalsOf(testOrigin, wantedState(t, resolved, 0))
	if err != nil {
		t.Fatalf("an approval whose referrer names the generation was refused: %v", err)
	}
	if checked.Generation != 7 {
		t.Fatalf("got generation %d, want the one the referrer names", checked.Generation)
	}
	if approvals[0].State.Generation != 7 {
		t.Fatalf("got approved state %+v, want the generation the approval was checked against", approvals[0].State)
	}
}

// When the caller already holds the state, the registry may not move it. The
// annotation is a hint for a caller that has nothing, and a registry that
// answered with another generation for a state the caller named would have
// the approval of a different release reported as the approval of this one.
func TestApprovalsOfKeepsTheGenerationTheCallerAlreadyNamed(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 9, []byte("an approval the registry annotated"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)

	var checked signature.State
	useSignatureVerifier(t, func(_ []byte, against signature.State) (signature.Verified, error) {
		checked = against
		return signature.Verified{State: against, Identity: approverIdentity(approverOrigin)}, nil
	})

	if _, err := cp.ApprovalsOf(testOrigin, wantedState(t, resolved, 4)); err != nil {
		t.Fatalf("the approval attached to the resolved image was refused: %v", err)
	}
	if checked.Generation != 4 {
		t.Fatalf("got generation %d, want the one the caller named and not the one the registry annotated", checked.Generation)
	}
}

// Neither side names the generation, so there is no state to check anything
// against. Inventing one would put a number cpak chose inside the payload cpak
// then verifies a signature over, which proves nothing at all.
func TestApprovalsOfRefusesAnApprovalNothingNamesTheGenerationOf(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 0, []byte("an approval with no generation"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)

	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		t.Fatal("a state with no generation was handed to the offline check")
		return signature.Verified{}, nil
	})

	_, err := cp.ApprovalsOf(testOrigin, wantedState(t, resolved, 0))
	if !errors.Is(err, ErrApprovalUnverified) {
		t.Fatalf("got %v, want an approval nothing names the state of to be reported as %v", err, ErrApprovalUnverified)
	}
	if !errors.Is(err, errUnnamedGeneration) {
		t.Fatalf("got %v, want the refusal to say that nothing names the generation", err)
	}
}

// More than one organisation may approve the same release, and an
// organisation that re-approves one leaves the earlier approval attached. Both
// are reported, because which of them a host acts on is decided by the list it
// keeps and not by the order a registry happens to serve them in.
func TestApprovalsOfReportsEveryApprovalThatHolds(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 4, []byte("the security team"))
	attachApproval(t, registry, resolved, 4, []byte("the platform team"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)

	useSignatureVerifier(t, func(given []byte, against signature.State) (signature.Verified, error) {
		return signature.Verified{State: against, Identity: approverIdentity("github.com/acme/" + strings.ReplaceAll(string(given), " ", "-"))}, nil
	})

	approvals, err := cp.ApprovalsOf(testOrigin, wantedState(t, resolved, 4))
	if err != nil {
		t.Fatalf("approvals that hold were refused: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("got %d approvals, want both of the ones attached", len(approvals))
	}
	if approvals[0].Identity.Repo == approvals[1].Identity.Repo {
		t.Fatalf("got %q twice, want each approval reported under the identity that made it", approvals[0].Identity.Repo)
	}
}

// The point of the round. A signature that verifies is not enough, because the
// publisher signs its own packages and always will. What decides is a list
// this machine keeps, and an approval by anybody who is not on it is refused
// even though it is a real, valid counter-signature.
func TestApprovedStateRefusesAnApprovalNoAdministratorApprovedOf(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 4, []byte("an approval by a stranger"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)
	useSignatureVerifier(t, func(_ []byte, against signature.State) (signature.Verified, error) {
		return signature.Verified{State: against, Identity: approverIdentity("github.com/stranger/approvals")}, nil
	})

	var asked [][2]string
	_, err := cp.ApprovedState(testOrigin, wantedState(t, resolved, 4), func(issuer, repo string) bool {
		asked = append(asked, [2]string{issuer, repo})
		return repo == approverOrigin
	})
	if !errors.Is(err, ErrApprovalUnauthorised) {
		t.Fatalf("got %v, want an approval by an identity nobody approved to be reported as %v", err, ErrApprovalUnauthorised)
	}
	if errors.Is(err, ErrPackageUnapproved) || errors.Is(err, ErrApprovalUnverified) {
		t.Fatalf("got %v, want an unauthorised approval not to be read as missing or broken", err)
	}
	if len(asked) != 1 || asked[0] != [2]string{githubActionsIssuer, "github.com/stranger/approvals"} {
		t.Fatalf("got %v, want the identity in the certificate to be the one put to the host", asked)
	}
	// An administrator who is told only that nobody they named approved this
	// cannot act. The name of whoever did is what turns the refusal into a
	// decision they can make.
	if !strings.Contains(err.Error(), "github.com/stranger/approvals") {
		t.Fatalf("got %v, want the refusal to name who did approve", err)
	}
}

// An unmanaged host is every host that exists today. No administrator has
// named an approver on it, and it must not start refusing counter-signatures
// because a policy file it does not have says nothing about them.
func TestApprovedStateOnAHostWithNoApproverListTakesEveryApprovalThatHolds(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 4, []byte("an approval by anybody at all"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)
	useSignatureVerifier(t, func(_ []byte, against signature.State) (signature.Verified, error) {
		return signature.Verified{State: against, Identity: approverIdentity("github.com/nobody/asked")}, nil
	})

	approval, err := cp.ApprovedState(testOrigin, wantedState(t, resolved, 4), nil)
	if err != nil {
		t.Fatalf("a host that approved nobody refused an approval that holds: %v", err)
	}
	if approval.Identity.Repo != "github.com/nobody/asked" {
		t.Fatalf("got approver %+v, want the identity that made the approval", approval.Identity)
	}
}

// The list decides which approval is acted on, not the registry. An image
// carrying an approval by somebody unknown beside one by the organisation must
// answer with the organisation's.
func TestApprovedStatePicksTheApprovalTheAdministratorApprovedOf(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 4, []byte("a stranger"))
	attachApproval(t, registry, resolved, 4, []byte("the organisation"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)
	useSignatureVerifier(t, func(given []byte, against signature.State) (signature.Verified, error) {
		if string(given) == "the organisation" {
			return signature.Verified{State: against, Identity: approverIdentity(approverOrigin)}, nil
		}
		return signature.Verified{State: against, Identity: approverIdentity("github.com/stranger/approvals")}, nil
	})

	approval, err := cp.ApprovedState(testOrigin, wantedState(t, resolved, 4), func(_, repo string) bool {
		return repo == approverOrigin
	})
	if err != nil {
		t.Fatalf("the approval this host approved of was refused: %v", err)
	}
	if approval.Identity.Repo != approverOrigin {
		t.Fatalf("got approver %+v, want the one the host approved of", approval.Identity)
	}
	if string(approval.Bundle) != "the organisation" {
		t.Fatalf("got evidence %q, want the bundle that identity signed", approval.Bundle)
	}
}

// The failure this whole round exists to remove, in its last hiding place. A
// publisher can counter-sign its own release, and that approval verifies
// against the trust root like any other. Acting on it would answer "somebody
// else vouched for this" with the publisher saying so twice.
func TestApprovedStateRefusesThePublishersOwnCounterSignature(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 4, []byte("the publisher approving itself"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)
	useSignatureVerifier(t, func(_ []byte, against signature.State) (signature.Verified, error) {
		return signature.Verified{State: against, Identity: approverIdentity(testOrigin)}, nil
	})

	_, err := cp.ApprovedState(testOrigin, wantedState(t, resolved, 4), nil)
	if !errors.Is(err, ErrApprovalNotIndependent) {
		t.Fatalf("got %v, want the publisher's own counter-signature to be reported as %v", err, ErrApprovalNotIndependent)
	}
	if errors.Is(err, ErrPackageUnapproved) || errors.Is(err, ErrApprovalUnverified) {
		t.Fatalf("got %v, want a self-made approval not to be read as missing or broken", err)
	}

	// It is still reported as attached, because a report that hid it would
	// leave an administrator wondering why an approved-looking package was
	// refused.
	approvals, err := cp.ApprovalsOf(testOrigin, wantedState(t, resolved, 4))
	if err != nil || len(approvals) != 1 {
		t.Fatalf("got %d approvals %v, want the publisher's counter-signature reported as attached", len(approvals), err)
	}
	if !approvals[0].Publisher() {
		t.Fatalf("got approver %+v, want it reported as the publisher's own", approvals[0].Identity)
	}
}

// A publisher that counter-signs its own release must not be able to hide a
// real approval behind it. The registry decides the order the two are listed
// in, so the order must decide nothing.
func TestApprovedStateTakesTheSecondPartyBesideThePublishersOwn(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 4, []byte("the publisher"))
	attachApproval(t, registry, resolved, 4, []byte("the organisation"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)
	useSignatureVerifier(t, func(given []byte, against signature.State) (signature.Verified, error) {
		if string(given) == "the publisher" {
			return signature.Verified{State: against, Identity: approverIdentity(testOrigin)}, nil
		}
		return signature.Verified{State: against, Identity: approverIdentity(approverOrigin)}, nil
	})

	approval, err := cp.ApprovedState(testOrigin, wantedState(t, resolved, 4), nil)
	if err != nil {
		t.Fatalf("a real approval listed after the publisher's own was refused: %v", err)
	}
	if approval.Identity.Repo != approverOrigin {
		t.Fatalf("got approver %+v, want the second party and not the publisher", approval.Identity)
	}
}

// A publisher can attach a counter-signature of its own, and it verifies like
// any other. It is not a second party, so it is reported as what it is rather
// than quietly counted as independent evidence.
func TestApprovalReportsThePublishersOwnCounterSignature(t *testing.T) {
	state := wantedState(t, contentDigest([]byte("the image")), 4)
	own := Approval{Identity: approverIdentity(testOrigin), State: state}
	if !own.Publisher() {
		t.Fatalf("got an approval by %q reported as a second party, want the publisher's own word recognised", own.Identity.Repo)
	}
	other := Approval{Identity: approverIdentity(approverOrigin), State: state}
	if other.Publisher() {
		t.Fatalf("got an approval by %q reported as the publisher's, want a second party recognised", other.Identity.Repo)
	}
}

// The state is what is being asked about. One that names another origin is a
// question about another package, and answering it with this package's
// approvals would report the approval of software nobody asked about.
func TestApprovalsOfRefusesAStateThatNamesAnotherOrigin(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("the image that was approved"))
	attachApproval(t, registry, resolved, 4, []byte("an approval"))
	ref := registry.start(t)
	approvedInstallation(t, cp, ref, resolved)
	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		t.Fatal("a state naming another origin reached the offline check")
		return signature.Verified{}, nil
	})

	elsewhere := wantedState(t, resolved, 4)
	elsewhere.Origin = "github.com/other/demo"
	_, err := cp.ApprovalsOf(testOrigin, elsewhere)
	if err == nil {
		t.Fatal("a state naming another origin was answered with this package's approvals")
	}
	if errors.Is(err, ErrPackageUnapproved) || errors.Is(err, ErrApprovalUnverified) || errors.Is(err, ErrApprovalUnauthorised) {
		t.Fatalf("got %v, want a state about another package not to be read as an answer about an approval", err)
	}
}
