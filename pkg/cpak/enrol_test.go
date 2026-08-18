/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// enrolmentAuthority stands in for the privileged side. It is the same ledger
// the gate reads, entered through the calls an enrolment makes and through the
// validation the authority applies, so what a test enrols is what a launch of
// it finds and a policy that does not hash to its root is refused here too.
//
// The signature is captured and not written into the ledger, because the
// authority verifies a bundle for real before it records one and nothing here
// can make a real one. What this side owes is the signature it hands over;
// what the authority does with it is proven where the authority lives.
type enrolmentAuthority struct {
	ledger    systemauthority.AnchorLedger
	records   map[string]systemauthority.Enrolment
	recorded  int
	forgotten int
	policy    *types.Override
	signature *systemauthority.SignedState
	signed    int
	refusal   error
}

func useEnrolmentAuthority(t *testing.T) *enrolmentAuthority {
	t.Helper()

	authority := &enrolmentAuthority{ledger: useAnchorLedger(t), records: map[string]systemauthority.Enrolment{}}
	savedRecord, savedForget := recordAnchor, forgetAnchor
	savedRecorded, savedPolicy := recordedAnchor, signaturePolicy
	t.Cleanup(func() {
		recordAnchor = savedRecord
		forgetAnchor = savedForget
		recordedAnchor = savedRecorded
		signaturePolicy = savedPolicy
	})
	recordAnchor = func(anchor integrity.Anchor, policy *types.Override, signed *systemauthority.SignedState) error {
		authority.recorded++
		authority.policy = policy
		authority.signature = signed
		if signed != nil {
			authority.signed++
		}
		if authority.refusal != nil {
			return authority.refusal
		}
		if err := authority.ledger.Record(systemauthority.Enrolment{Anchor: anchor, Policy: policy}); err != nil {
			return err
		}
		authority.records[anchor.Origin] = systemauthority.Enrolment{Anchor: anchor, Policy: policy, Signature: signed}
		return nil
	}
	forgetAnchor = func(uid uint32, origin string) error {
		authority.forgotten++
		if authority.refusal != nil {
			return authority.refusal
		}
		return authority.ledger.Forget(uid, origin)
	}
	// Records are read back from what this authority took, and not from the
	// ledger file: the authority proves a bundle for real before it writes one
	// and nothing on this side can make a bundle that would survive that. The
	// anchor still goes through the real ledger, so the gate still finds what
	// an enrolment recorded.
	recordedAnchor = authority.recordOf
	signaturePolicy = func() systemauthority.SignaturePolicy { return systemauthority.SignaturesOptional }
	return authority
}

func (a *enrolmentAuthority) recordOf(uid uint32, origin string) (systemauthority.Enrolment, bool, error) {
	record, held := a.records[origin]
	if !held || record.UID != uid {
		return systemauthority.Enrolment{}, false, nil
	}
	return record, true, nil
}

// requireSignatures drives the host policy the way useEnforcement drives the
// enforcement level.
func requireSignatures(t *testing.T) {
	t.Helper()

	saved := signaturePolicy
	t.Cleanup(func() { signaturePolicy = saved })
	signaturePolicy = func() systemauthority.SignaturePolicy { return systemauthority.SignaturesRequired }
}

func (a *enrolmentAuthority) holds(t *testing.T, origin string) (integrity.Anchor, bool) {
	t.Helper()

	anchor, held, err := a.ledger.Load(uint32(os.Getuid()), origin)
	if err != nil {
		t.Fatalf("the ledger could not be read back: %v", err)
	}
	return anchor, held
}

// enrolledApplication is the application of the gate tests with its layers
// bound, which is the state an install leaves behind.
func enrolledApplication(t *testing.T, cp *Cpak) types.Application {
	t.Helper()

	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	return verifiedApplication()
}

// This is the whole point of enrolling at install time: the anchor the
// installer writes and the root the gate derives are produced by the same code,
// so the gate recognises what the install just recorded.
func TestEnrolApplicationRecordsTheLaunchTheGateThenRecognises(t *testing.T) {
	cp := newTestCpak(t)
	useEnrolmentAuthority(t)
	app := enrolledApplication(t, cp)

	enrolment := cp.EnrolApplication(app)
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the application to be enrolled", enrolment.Outcome, enrolment.Reason)
	}
	if enrolment.Anchor.Generation != 1 {
		t.Fatalf("got generation %d, want a first enrolment to be generation 1", enrolment.Anchor.Generation)
	}

	identity, err := cp.gateLaunch(app, resolvedOverride(app), nil, nil)
	if err != nil {
		t.Fatalf("the gate refused the launch the install had just enrolled: %v", err)
	}
	if identity.Verdict != LaunchRecognised {
		t.Fatalf("got verdict %s, want the enrolled launch to be recognised", identity.Verdict)
	}
	if identity.LaunchRoot != enrolment.Anchor.LaunchRoot {
		t.Fatal("the gate derived a launch root the enrolment did not record, so the two do not agree by construction")
	}
}

// The anchor carries the effective override and not the one the manifest
// declares, because the effective one is what the gate hashes. The policy
// itself travels with it: without it the authority sees two policy roots it
// cannot order and has to ask the owner about every change.
func TestEnrolApplicationRecordsTheOverrideALaunchResolves(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	app := enrolledApplication(t, cp)
	app.ParsedOverride = types.Override{SocketWayland: true}

	enrolment := cp.EnrolApplication(app)
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the application to be enrolled", enrolment.Outcome, enrolment.Reason)
	}
	policyRoot, err := integrity.PolicyRoot(app.ParsedOverride)
	if err != nil {
		t.Fatal(err)
	}
	if enrolment.Anchor.PolicyRoot != policyRoot {
		t.Fatalf("got policy root %q, want the one the effective override hashes to %q", enrolment.Anchor.PolicyRoot, policyRoot)
	}
	if authority.policy == nil || !reflect.DeepEqual(*authority.policy, app.ParsedOverride) {
		t.Fatalf("got policy %+v, want the enrolment to carry the override its root was taken over", authority.policy)
	}
}

// A layer nothing binds leaves the launch undescribable. The user has to be
// pointed at the one command that fixes it, and the authority must not be asked
// to record a launch that could not be derived.
func TestEnrolApplicationNamesTheBackfillWhenALayerIsNotBound(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)

	enrolment := cp.EnrolApplication(verifiedApplication())
	if enrolment.Outcome != EnrolmentUndescribed {
		t.Fatalf("got outcome %s, want an application whose layers are not bound to be undescribed", enrolment.Outcome)
	}
	if !errors.Is(enrolment.Reason, errLaunchUnbound) {
		t.Fatalf("got reason %v, want the unbound layer to be named", enrolment.Reason)
	}
	if !strings.Contains(enrolment.Advice, "cpak audit --backfill-bindings") {
		t.Fatalf("got advice %q, want it to name the backfill", enrolment.Advice)
	}
	if authority.recorded != 0 {
		t.Fatalf("the authority was asked %d times to record a launch that could not be derived", authority.recorded)
	}
	if _, held := authority.holds(t, testOrigin); held {
		t.Fatal("the ledger holds an anchor for an application whose layers are not bound")
	}
}

// An enrolment that cannot be recorded must not undo an installation that
// succeeded, and it must not pass in silence either: the outcome says the
// application is unenrolled and names what to run.
func TestEnrolApplicationReportsAnAuthorityItCannotReach(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	authority.refusal = systemauthority.ErrNoAuthority
	app := enrolledApplication(t, cp)

	enrolment := cp.EnrolApplication(app)
	if enrolment.Outcome != EnrolmentUnrecordable {
		t.Fatalf("got outcome %s, want an enrolment the authority did not take", enrolment.Outcome)
	}
	if !errors.Is(enrolment.Reason, systemauthority.ErrNoAuthority) {
		t.Fatalf("got reason %v, want the unreachable authority", enrolment.Reason)
	}
	if !strings.Contains(enrolment.Advice, "cpak system setup") {
		t.Fatalf("got advice %q, want it to name the system setup", enrolment.Advice)
	}
	if _, held := authority.holds(t, app.Origin); held {
		t.Fatal("the ledger holds an anchor the authority refused to take")
	}
}

// A second enrolment of a changed application follows the first, so nothing can
// put an application back to a launch it already left.
func TestEnrolApplicationIncreasesTheGeneration(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	app := enrolledApplication(t, cp)

	if enrolment := cp.EnrolApplication(app); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the first enrolment to be recorded", enrolment.Outcome, enrolment.Reason)
	}
	updated := app
	updated.Version = "2.0"
	enrolment := cp.EnrolApplication(updated)
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the second enrolment to be recorded", enrolment.Outcome, enrolment.Reason)
	}
	if enrolment.Anchor.Generation != 2 {
		t.Fatalf("got generation %d, want the enrolment that followed to be generation 2", enrolment.Anchor.Generation)
	}
	recorded, held := authority.holds(t, app.Origin)
	if !held || recorded.LaunchRoot != enrolment.Anchor.LaunchRoot {
		t.Fatal("the ledger does not hold the launch that was enrolled last")
	}
}

// Re-enrolling a launch the ledger already holds asks nothing of the authority,
// so an update that changed nothing never prompts and never spends a generation.
func TestEnrolApplicationLeavesALaunchTheLedgerAlreadyHolds(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	app := enrolledApplication(t, cp)

	first := cp.EnrolApplication(app)
	if first.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the first enrolment to be recorded", first.Outcome, first.Reason)
	}
	second := cp.EnrolApplication(app)
	if second.Outcome != EnrolmentUnchanged {
		t.Fatalf("got outcome %s, want an unchanged launch to be left alone", second.Outcome)
	}
	if authority.recorded != 1 {
		t.Fatalf("the authority was asked %d times, want the unchanged launch not to reach it", authority.recorded)
	}
	if second.Anchor.Generation != first.Anchor.Generation {
		t.Fatalf("got generation %d, want the unchanged launch to keep generation %d", second.Anchor.Generation, first.Anchor.Generation)
	}
}

// An update writes the anchor of what the application became. Without it the
// gate would refuse the very launch the update produced.
func TestUpdateEnrolsTheApplicationItProduced(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	seedApplication(t, cp, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{verifiedBaseLayer},
		ParsedBinaries: []string{"/usr/bin/demo"},
		Config:         "{}",
	})

	stub := &updateStub{
		manifest:    newTestManifest(),
		layers:      []string{verifiedBaseLayer, verifiedTopLayer},
		config:      "{}",
		imageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	results, err := cp.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("the update returned an error: %v", err)
	}
	if len(results) != 1 || results[0].Status != types.UpdateStatusUpdated {
		t.Fatalf("got %+v, want one updated application", results)
	}
	if authority.recorded != 1 {
		t.Fatalf("the authority was asked %d times, want the update to enrol what it produced once", authority.recorded)
	}

	updated := storedApplications(t, cp)
	if len(updated) != 1 {
		t.Fatalf("got %d stored applications, want the one that was updated", len(updated))
	}
	identity, err := cp.gateLaunch(updated[0], resolvedOverride(updated[0]), nil, nil)
	if err != nil {
		t.Fatalf("the gate refused the launch the update had just enrolled: %v", err)
	}
	if identity.Verdict != LaunchRecognised {
		t.Fatalf("got verdict %s, want the updated launch to be recognised", identity.Verdict)
	}
}

// The decision this round takes: an authority that cannot be reached leaves the
// installation in place and reports it. An update that produced working
// software must not be turned into a failure by a helper that is down.
func TestUpdateSucceedsWhenTheApplicationCannotBeEnrolled(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	authority.refusal = systemauthority.ErrNoAuthority
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	seedApplication(t, cp, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{verifiedBaseLayer},
		ParsedBinaries: []string{"/usr/bin/demo"},
		Config:         "{}",
	})

	stub := &updateStub{
		manifest:    newTestManifest(),
		layers:      []string{verifiedBaseLayer, verifiedTopLayer},
		config:      "{}",
		imageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	results, err := cp.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("the update returned an error: %v", err)
	}
	if len(results) != 1 || results[0].Status != types.UpdateStatusUpdated {
		t.Fatalf("got %+v, want the update to stand although nothing recorded it", results)
	}
	if authority.recorded != 1 {
		t.Fatalf("the authority was asked %d times, want the update to have tried once", authority.recorded)
	}
	stored := storedApplications(t, cp)
	if len(stored) != 1 || stored[0].ImageDigest != "sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("got %+v, want the updated application to be the one in the store", stored)
	}
}

func TestRemoveForgetsTheAnchorOfTheApplicationItRemoved(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	app := enrolledApplication(t, cp)
	app.CpakId = testCpakId("branch", "main")
	app.Version = "main"
	seedApplication(t, cp, app)

	if enrolment := cp.EnrolApplication(app); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the application to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}
	if err := cp.Remove(testOrigin, "main", "", ""); err != nil {
		t.Fatalf("the removal failed: %v", err)
	}
	if authority.forgotten != 1 {
		t.Fatalf("the authority was asked %d times to forget the anchor, want once", authority.forgotten)
	}
	if _, held := authority.holds(t, testOrigin); held {
		t.Fatal("the ledger still answers for an application that is not installed")
	}
}

// An anchor is filed under the origin alone, so removing one of two branches
// leaves the ledger naming an installation that may be gone. The one that
// remains takes its place.
func TestRemoveEnrolsTheInstallationThatRemains(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	removed := enrolledApplication(t, cp)
	removed.CpakId = testCpakId("branch", "main")
	removed.Version = "main"
	seedApplication(t, cp, removed)

	kept := removed
	kept.CpakId = testCpakId("branch", "stable")
	kept.Version = "stable"
	kept.Branch = "stable"
	kept.ParsedLayers = []string{verifiedBaseLayer}
	seedApplication(t, cp, kept)

	if enrolment := cp.EnrolApplication(removed); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the application to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}
	if err := cp.Remove(testOrigin, "main", "", ""); err != nil {
		t.Fatalf("the removal failed: %v", err)
	}

	anchor, held := authority.holds(t, testOrigin)
	if !held {
		t.Fatal("the origin still has an installation and the ledger holds nothing for it")
	}
	expected := cp.EnrolApplication(kept)
	if expected.Outcome != EnrolmentUnchanged {
		t.Fatalf("got outcome %s, want the ledger to already hold the installation that remains", expected.Outcome)
	}
	if anchor.LaunchRoot != expected.Anchor.LaunchRoot {
		t.Fatal("the anchor left behind is not the launch of the installation that remains")
	}
}

// Everything below is the half of an enrolment that was trust on first install
// until now: who published what was installed. A signature is fetched from the
// registry the image came from, checked offline, and handed to the authority as
// the bundle it is, never as a claim that it checked out.

// publisherIdentity is what a certificate issued to the CI of a repository
// names. The subject is the workflow and not the repository, which is why
// nothing here compares subjects.
func publisherIdentity(repo string) signature.Identity {
	return signature.Identity{
		Issuer:  githubActionsIssuer,
		Subject: "https://" + repo + "/.github/workflows/release.yml@refs/heads/main",
		Repo:    repo,
	}
}

// attachSigned files a bundle against an image the way cpak-sign does: the
// publisher's counter travels in the annotation a registry copies onto the
// descriptor of its referrers index, because it is the one field of a signed
// state that cannot be derived from the package.
func attachSigned(t *testing.T, registry *signatureRegistry, subject string, generation uint64, bundle []byte) {
	t.Helper()

	registry.attach(t, subject, packageSignatureArtifactType, bundle)
	attached := registry.referrers[subject]
	attached[len(attached)-1].Annotations = map[string]string{
		signedGenerationAnnotation: strconv.FormatUint(generation, 10),
	}
}

// installedFromRegistry is the application of the gate tests as an install
// leaves it: layers bound, and resolved to a digest a registry answers for.
func installedFromRegistry(t *testing.T, cp *Cpak, registry *signatureRegistry, imageDigest string) types.Application {
	t.Helper()

	ref := registry.start(t)
	app := enrolledApplication(t, cp)
	app.Image = ref.ContextName() + ":main"
	app.ImageDigest = imageDigest
	seedApplication(t, cp, app)
	return app
}

// publishedTestPackage is what an install holds and an installed record does
// not: the manifest as cpak applied it, which is half of what was signed.
func publishedTestPackage(t *testing.T) PublishedPackage {
	t.Helper()

	return PublishedPackage{Manifest: validatedTestManifest(t)}
}

func wantedState(t *testing.T, imageDigest string, generation uint64) signature.State {
	t.Helper()

	state, err := PackageState(testOrigin, validatedTestManifest(t), imageDigest, nil)
	if err != nil {
		t.Fatalf("the state of the test package could not be named: %v", err)
	}
	state.Generation = generation
	return state
}

// The whole of what this round adds: the enrolment carries the bundle, and the
// state it was checked against is the state that was installed, manifest hash
// and all. A signature over the image alone would let somebody swap the
// manifest and widen the sandbox with a signature that still verified.
func TestEnrolPublishedApplicationRecordsWhoSignedTheInstallation(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	bundle := []byte(`{"bundle":"the one the publisher attached"}`)
	attachSigned(t, registry, digest, 4, bundle)
	app := installedFromRegistry(t, cp, registry, digest)

	var checked []signature.State
	useSignatureVerifier(t, func(offered []byte, state signature.State) (signature.Verified, error) {
		checked = append(checked, state)
		if string(offered) != string(bundle) {
			return signature.Verified{}, errors.New("that is not the bundle the registry serves")
		}
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})

	enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want a signed application to be enrolled", enrolment.Outcome, enrolment.Reason)
	}
	if !enrolment.Signature.Verified || enrolment.Signature.Identity.Repo != testOrigin {
		t.Fatalf("got signature %+v, want it verified and made by %s", enrolment.Signature, testOrigin)
	}
	if authority.signature == nil {
		t.Fatal("the authority was handed no signature for a package the registry holds one for")
	}
	if string(authority.signature.Bundle) != string(bundle) {
		t.Fatalf("the authority was handed %q, want the bundle the registry serves", authority.signature.Bundle)
	}
	want := wantedState(t, digest, 4)
	if authority.signature.State != want {
		t.Fatalf("the authority was handed state %+v, want the state that was installed %+v", authority.signature.State, want)
	}
	if len(checked) == 0 || checked[0] != want {
		t.Fatalf("the bundle was checked against %+v, want the state that was installed %+v", checked, want)
	}
}

// The negative of the one above. Nothing about the fetch changes, only the
// answer the offline check gives, and the installation is enrolled as unsigned
// instead of being enrolled as signed by nobody in particular.
func TestEnrolPublishedApplicationEnrolsAsUnsignedWhenTheBundleDoesNotStand(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, digest, 1, []byte("a bundle that does not stand"))
	app := installedFromRegistry(t, cp, registry, digest)
	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("no transparency log holds this")
	})

	enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the installation to be enrolled anyway", enrolment.Outcome, enrolment.Reason)
	}
	if enrolment.Signature.Verified {
		t.Fatal("a bundle the offline check refused was reported as a signature")
	}
	if !errors.Is(enrolment.Signature.Reason, ErrSignatureUnverified) {
		t.Fatalf("got reason %v, want the refusal to say the signature does not verify", enrolment.Signature.Reason)
	}
	if enrolment.Signature.Unsigned() {
		t.Fatal("a bundle that does not stand was reported as no bundle at all")
	}
	if authority.signature != nil {
		t.Fatal("a signature that does not stand was handed to the authority")
	}
}

// A signature that checks out and was made by somebody else is the one failure
// that says the artifact is authentic and still not the publisher's. The
// identity comparison here is the real one, so this is also what pins that a
// lookalike repository and the wrong issuer speak for nothing.
func TestEnrolPublishedApplicationRefusesASignatureFromAnotherIdentity(t *testing.T) {
	for name, identity := range map[string]signature.Identity{
		"another repository":  publisherIdentity("github.com/attacker/demo"),
		"a lookalike owner":   publisherIdentity("github.com/user-inc/demo"),
		"a name it prefixes":  publisherIdentity("github.com/user/demo-evil"),
		"another issuer":      {Issuer: "https://accounts.google.com", Repo: testOrigin},
		"a nameless identity": {},
	} {
		cp := newSignatureCpak(t)
		authority := useEnrolmentAuthority(t)
		registry := newSignatureRegistry()
		digest := contentDigest([]byte("the image that was signed"))
		attachSigned(t, registry, digest, 1, []byte("a bundle somebody else made"))
		app := installedFromRegistry(t, cp, registry, digest)
		useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
			return signature.Verified{State: state, Identity: identity}, nil
		})

		enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
		if enrolment.Signature.Verified {
			t.Fatalf("%s was accepted as the publisher of %s", name, testOrigin)
		}
		if !errors.Is(enrolment.Signature.Reason, ErrSignatureForeign) {
			t.Fatalf("%s was refused as %v, want it named as another identity", name, enrolment.Signature.Reason)
		}
		if authority.signature != nil {
			t.Fatalf("the signature of %s was handed to the authority", name)
		}
	}
}

// The generation is the publisher's counter and nothing on this machine holds
// it, so it is read off the referrer. A referrer that names none is skipped:
// any number cpak chose would be a number cpak invented and then checked a
// signature against.
func TestEnrolPublishedApplicationTakesTheGenerationFromTheReferrer(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	older := []byte(`{"bundle":"generation two"}`)
	newer := []byte(`{"bundle":"generation five"}`)
	attachSigned(t, registry, digest, 2, older)
	attachSigned(t, registry, digest, 5, newer)
	app := installedFromRegistry(t, cp, registry, digest)

	var checked []signature.State
	useSignatureVerifier(t, func(offered []byte, state signature.State) (signature.Verified, error) {
		checked = append(checked, state)
		if string(offered) != string(newer) || state.Generation != 5 {
			return signature.Verified{}, errors.New("this bundle does not cover that state")
		}
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})

	enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	if !enrolment.Signature.Verified {
		t.Fatalf("the signature was not accepted: %v", enrolment.Signature.Reason)
	}
	if enrolment.Signature.State.Generation != 5 {
		t.Fatalf("got generation %d, want the one the referrer names", enrolment.Signature.State.Generation)
	}
	if len(checked) == 0 || checked[0].Generation != 5 {
		t.Fatalf("the states offered were %+v, want the newest one first", checked)
	}
}

func TestEnrolPublishedApplicationIgnoresAReferrerThatNamesNoGeneration(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	registry.attach(t, digest, packageSignatureArtifactType, []byte("a bundle nobody counted"))
	app := installedFromRegistry(t, cp, registry, digest)

	asked := 0
	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		asked++
		return signature.Verified{}, errors.New("nothing should have been offered")
	})

	enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	if asked != 0 {
		t.Fatalf("the offline check was asked %d times about a referrer that names no generation", asked)
	}
	if !enrolment.Signature.Unsigned() {
		t.Fatalf("got signature %+v, want a referrer nobody counted to read as unsigned", enrolment.Signature)
	}
	if authority.signature != nil {
		t.Fatal("a referrer that names no generation was handed to the authority")
	}
}

// Signing arrives into a world of packages nobody signed. On a host that has
// not been told otherwise, one of them installs and enrols exactly as before,
// and the record says what it is.
func TestEnrolPublishedApplicationEnrolsAnUnsignedPackage(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("an image nobody signed"))
	app := installedFromRegistry(t, cp, registry, digest)

	enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want an unsigned package to be enrolled", enrolment.Outcome, enrolment.Reason)
	}
	if !enrolment.Signature.Unsigned() {
		t.Fatalf("got signature %+v, want it reported as unsigned", enrolment.Signature)
	}
	if authority.signature != nil {
		t.Fatal("the authority was handed a signature for a package nobody signed")
	}
	if _, held := authority.holds(t, app.Origin); !held {
		t.Fatal("an unsigned package was not enrolled on a host that takes unsigned packages")
	}
}

// The host policy, and the whole reason it exists: on a host that takes only
// signed packages an unsigned one is not enrolled at all, and nothing is asked
// of the authority about it.
func TestRequiredSignaturesRefuseToEnrolAnUnsignedPackage(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	requireSignatures(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("an image nobody signed"))
	app := installedFromRegistry(t, cp, registry, digest)

	enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	if enrolment.Outcome != EnrolmentUnsigned {
		t.Fatalf("got outcome %s (%v), want an unsigned package to be refused", enrolment.Outcome, enrolment.Reason)
	}
	if !errors.Is(enrolment.Reason, systemauthority.ErrSignatureRequired) {
		t.Fatalf("got reason %v, want the host policy to be named", enrolment.Reason)
	}
	if !strings.Contains(enrolment.Advice, "cpak system set-signatures optional") {
		t.Fatalf("got advice %q, want it to name the way back", enrolment.Advice)
	}
	if authority.recorded != 0 {
		t.Fatalf("the authority was asked %d times to record an unsigned enrolment", authority.recorded)
	}
	if _, held := authority.holds(t, app.Origin); held {
		t.Fatal("the ledger answers for an application this host refused to enrol")
	}
}

// The same host, the same command, a package the publisher signed.
func TestRequiredSignaturesEnrolASignedPackage(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	requireSignatures(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, digest, 1, []byte(`{"bundle":"signed"}`))
	app := installedFromRegistry(t, cp, registry, digest)
	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})

	enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want a signed package to be enrolled on a host that requires one", enrolment.Outcome, enrolment.Reason)
	}
	if authority.signed != 1 {
		t.Fatalf("the authority was handed %d signatures, want the one that was verified", authority.signed)
	}
}

// A caller that holds nothing but the installed record cannot name a signed
// state, so it carries forward what was already proven. Without this, changing
// an override would turn a signed application into an unsigned one, and on a
// host that requires signatures that would unenrol working software.
func TestEnrolApplicationCarriesForwardTheSignatureTheLedgerHolds(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	bundle := []byte(`{"bundle":"signed"}`)
	attachSigned(t, registry, digest, 3, bundle)
	app := installedFromRegistry(t, cp, registry, digest)
	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})
	if enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the signed installation to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}

	narrowed := app
	narrowed.ParsedOverride = types.Override{SocketWayland: true}
	if enrolment := cp.EnrolApplication(narrowed); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the changed override to be enrolled", enrolment.Outcome, enrolment.Reason)
	}
	if authority.signature == nil || string(authority.signature.Bundle) != string(bundle) {
		t.Fatalf("the authority was handed %+v, want the signature the ledger already holds", authority.signature)
	}
	if authority.signature.State.Generation != 3 {
		t.Fatalf("got generation %d, want the one the publisher signed", authority.signature.State.Generation)
	}
}

// It is carried forward only while it is still about this image. A signature
// covers the manifest and the image, so an installation resolved to another
// image is not the one that was signed and nothing may say it is.
func TestEnrolApplicationDropsASignatureThatIsAboutAnotherImage(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, digest, 1, []byte(`{"bundle":"signed"}`))
	app := installedFromRegistry(t, cp, registry, digest)
	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})
	if enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the signed installation to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}

	moved := app
	moved.ImageDigest = contentDigest([]byte("another image entirely"))
	moved.ParsedOverride = types.Override{SocketWayland: true}
	if enrolment := cp.EnrolApplication(moved); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the moved installation to be enrolled", enrolment.Outcome, enrolment.Reason)
	}
	if authority.signature != nil {
		t.Fatal("a signature about another image was carried forward")
	}
}

// A signature the offline check no longer accepts is not carried forward
// either. Offering it would have the authority refuse the whole enrolment, and
// an application must not be unenrolled because a trust root moved on.
func TestEnrolApplicationDropsASignatureThatNoLongerStands(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, digest, 1, []byte(`{"bundle":"signed"}`))
	app := installedFromRegistry(t, cp, registry, digest)
	standing := true
	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		if !standing {
			return signature.Verified{}, errors.New("this certificate chains to nothing this host trusts")
		}
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})
	if enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the signed installation to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}

	standing = false
	narrowed := app
	narrowed.ParsedOverride = types.Override{SocketWayland: true}
	if enrolment := cp.EnrolApplication(narrowed); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the application to stay enrolled", enrolment.Outcome, enrolment.Reason)
	}
	if authority.signature != nil {
		t.Fatal("a signature that no longer stands was offered to the authority")
	}
}

// What cpak audit and cpak system explain read. It proves the recorded bundle
// again rather than believing the record, which is the only way a report can
// tell a signature that still stands from one that does not.
func TestRecordedSignaturesReportWhatTheLedgerHolds(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, digest, 2, []byte(`{"bundle":"signed"}`))
	app := installedFromRegistry(t, cp, registry, digest)
	standing := true
	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		if !standing {
			return signature.Verified{}, errors.New("this certificate chains to nothing this host trusts")
		}
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})
	if enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the signed installation to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}
	if authority.signed != 1 {
		t.Fatalf("the authority was handed %d signatures, want one", authority.signed)
	}

	reported, err := cp.RecordedSignatures()
	if err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0].Origin != testOrigin {
		t.Fatalf("got %+v, want one report about %s", reported, testOrigin)
	}
	if !reported[0].Enrolled || !reported[0].Verified || reported[0].Identity.Repo != testOrigin {
		t.Fatalf("got %+v, want the recorded signature reported as standing", reported[0])
	}

	// The same record, read on a host the bundle no longer verifies on. It is
	// neither signed nor unsigned, and a report that folded it into either
	// would be the report saying something the ledger does not.
	standing = false
	failing := cp.RecordedSignatureOf(testOrigin)
	if failing.Verified || failing.Unsigned() {
		t.Fatalf("got %+v, want a recorded signature that no longer stands to be reported as neither", failing)
	}
	if !errors.Is(failing.Reason, ErrSignatureUnverified) {
		t.Fatalf("got reason %v, want it to say the signature does not verify", failing.Reason)
	}
}

// An application nobody signed is reported as unsigned and not as one nothing
// is known about.
func TestRecordedSignaturesReportAnUnsignedApplication(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	app := installedFromRegistry(t, cp, registry, contentDigest([]byte("an image nobody signed")))
	if enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the installation to be enrolled", enrolment.Outcome, enrolment.Reason)
	}

	reported := cp.RecordedSignatureOf(testOrigin)
	if !reported.Enrolled {
		t.Fatal("an application that was just enrolled is reported as not enrolled")
	}
	if !reported.Unsigned() {
		t.Fatalf("got %+v, want it reported as unsigned", reported)
	}
}

// A launch the ledger already holds asks nothing of the authority, and the
// report still says who published it: an install that changed nothing must not
// read as an application nobody knows the provenance of.
func TestAnUnchangedEnrolmentStillReportsWhoSignedIt(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, digest, 6, []byte(`{"bundle":"signed"}`))
	app := installedFromRegistry(t, cp, registry, digest)
	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})
	if enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the signed installation to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}

	second := cp.EnrolApplication(app)
	if second.Outcome != EnrolmentUnchanged {
		t.Fatalf("got outcome %s, want an unchanged launch to be left alone", second.Outcome)
	}
	if authority.recorded != 1 {
		t.Fatalf("the authority was asked %d times, want the unchanged launch not to reach it", authority.recorded)
	}
	if !second.Signature.Verified || second.Signature.State.Generation != 6 {
		t.Fatalf("got signature %+v, want the one the ledger holds", second.Signature)
	}
}

// An anchor is filed under the origin alone, so removing one of two
// installations enrols the one that remains from a record that no longer
// exists by then. The signature has to survive that, or removing a branch
// would quietly turn a signed application into an unsigned one.
func TestRemoveCarriesTheSignatureToTheInstallationThatRemains(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image that was signed"))
	bundle := []byte(`{"bundle":"signed"}`)
	attachSigned(t, registry, digest, 7, bundle)
	ref := registry.start(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	removed := verifiedApplication()
	removed.Image = ref.ContextName() + ":main"
	removed.ImageDigest = digest
	removed.CpakId = testCpakId("branch", "main")
	removed.Version = "main"
	seedApplication(t, cp, removed)

	kept := removed
	kept.CpakId = testCpakId("branch", "stable")
	kept.Version = "stable"
	kept.Branch = "stable"
	kept.ParsedLayers = []string{verifiedBaseLayer}
	seedApplication(t, cp, kept)

	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})
	if enrolment := cp.EnrolPublishedApplication(removed, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the signed installation to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}
	authority.signature = nil

	if err := cp.Remove(testOrigin, "main", "", ""); err != nil {
		t.Fatalf("the removal failed: %v", err)
	}
	if authority.forgotten != 1 {
		t.Fatalf("the authority was asked %d times to forget the anchor, want once", authority.forgotten)
	}
	if authority.signature == nil || string(authority.signature.Bundle) != string(bundle) {
		t.Fatalf("the installation that remains was enrolled with %+v, want the signature the removed one had proven", authority.signature)
	}
}

// Everything above enters enrolment through an entry point. What follows drives
// the two paths a user actually runs, because the check above was written,
// tested through its own entry point, and called by neither: the one thing no
// test asserted was that an install reaches it at all.

// publishImage puts an image an install can pull into the test registry: a
// manifest with no layers and the config blob it names. Nothing here is about
// layers, only about resolving to the digest a signature is attached to.
func publishImage(t *testing.T, registry *signatureRegistry, tag string) string {
	t.Helper()

	config := []byte(`{"architecture":"amd64","os":"linux","config":{}}`)
	configDigest := contentDigest(config)
	manifest := []byte(fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`,
		configDigest, len(config)))
	digest := contentDigest(manifest)
	registry.blobs[configDigest] = config
	registry.manifests[digest] = manifest
	registry.manifests[tag] = manifest
	return digest
}

// The defect this test exists for. An install that stops asking who published
// what it just put on disk fails here, and it fails for the reason that
// matters: the state the check was made against is the state that was
// installed, named from the manifest the install applied and the digest the
// registry answered with.
func TestInstallVerifiesThePublisherOfWhatItInstalled(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	digest := publishImage(t, registry, "main")
	bundle := []byte(`{"bundle":"the one the publisher attached"}`)
	attachSigned(t, registry, digest, 4, bundle)

	var checked []signature.State
	useSignatureVerifier(t, func(offered []byte, state signature.State) (signature.Verified, error) {
		checked = append(checked, state)
		if string(offered) != string(bundle) {
			return signature.Verified{}, errors.New("that is not the bundle the registry serves")
		}
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})

	manifest := newTestManifest()
	manifest.Image = ref.ContextName() + ":main"
	options := InstallOptions{CreateExports: true, ResolveImageRef: true}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("the install failed: %v", err)
	}

	if len(checked) == 0 {
		t.Fatal("the install enrolled the application without ever asking the verifier, so nothing on the install path checks who published a package")
	}
	want, err := PackageState(testOrigin, manifest, digest, nil)
	if err != nil {
		t.Fatalf("the state of the installed package could not be named: %v", err)
	}
	want.Generation = 4
	if checked[0] != want {
		t.Fatalf("the bundle was checked against %+v, want the state the install resolved %+v", checked[0], want)
	}
	if authority.signature == nil || string(authority.signature.Bundle) != string(bundle) {
		t.Fatalf("the authority was handed %+v, want the bundle the registry serves", authority.signature)
	}
	if authority.signature.State != want {
		t.Fatalf("the authority was handed state %+v, want the state that was installed %+v", authority.signature.State, want)
	}
}

// Signing arrives into a world of packages nobody signed, and the install path
// is where that has to cost nothing: at the policy every host is on, an origin
// the registry holds no signature for installs and enrols exactly as it did
// before any of this existed.
func TestInstallEnrolsAPackageTheRegistryHoldsNoSignatureFor(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	digest := publishImage(t, registry, "main")

	asked := 0
	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		asked++
		return signature.Verified{}, errors.New("nothing is attached to this image")
	})

	manifest := newTestManifest()
	manifest.Image = ref.ContextName() + ":main"
	options := InstallOptions{CreateExports: true, ResolveImageRef: true}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("a package nobody signed did not install: %v", err)
	}

	installed := storedApplications(t, cp)
	if len(installed) != 1 || installed[0].ImageDigest != digest {
		t.Fatalf("got %+v, want the unsigned package installed and resolved to %s", installed, digest)
	}
	if asked != 0 {
		t.Fatalf("the offline check was asked %d times about an image nothing is attached to", asked)
	}
	if authority.recorded != 1 {
		t.Fatalf("the authority was asked %d times to record the installation, want once", authority.recorded)
	}
	if authority.signature != nil {
		t.Fatal("the authority was handed a signature for a package nobody signed")
	}
	if _, held := authority.holds(t, testOrigin); !held {
		t.Fatal("an unsigned package was not enrolled on a host that takes unsigned packages")
	}
}

// The other path that holds a manifest. An update resolves a new image, and it
// is the only moment cpak can find out that the publisher signed this one.
func TestUpdateVerifiesThePublisherOfWhatItInstalled(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	digest := contentDigest([]byte("the image the update resolved"))
	bundle := []byte(`{"bundle":"signed"}`)
	attachSigned(t, registry, digest, 2, bundle)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	seedApplication(t, cp, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{verifiedBaseLayer},
		ParsedBinaries: []string{"/usr/bin/demo"},
		Config:         "{}",
	})

	var checked []signature.State
	useSignatureVerifier(t, func(offered []byte, state signature.State) (signature.Verified, error) {
		checked = append(checked, state)
		if string(offered) != string(bundle) {
			return signature.Verified{}, errors.New("that is not the bundle the registry serves")
		}
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})

	manifest := newTestManifest()
	manifest.Image = ref.ContextName() + ":main"
	stub := &updateStub{
		manifest:    manifest,
		layers:      []string{verifiedBaseLayer, verifiedTopLayer},
		config:      "{}",
		imageDigest: digest,
	}
	results, err := cp.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("the update returned an error: %v", err)
	}
	if len(results) != 1 || results[0].Status != types.UpdateStatusUpdated {
		t.Fatalf("got %+v, want one updated application", results)
	}
	if len(checked) == 0 {
		t.Fatal("the update enrolled what it produced without ever asking the verifier, so nothing on the update path checks who published a package")
	}
	want, err := PackageState(testOrigin, manifest, digest, nil)
	if err != nil {
		t.Fatalf("the state of the updated package could not be named: %v", err)
	}
	want.Generation = 2
	if checked[0] != want {
		t.Fatalf("the bundle was checked against %+v, want the state the update resolved %+v", checked[0], want)
	}
	if authority.signature == nil || string(authority.signature.Bundle) != string(bundle) {
		t.Fatalf("the authority was handed %+v, want the bundle the registry serves", authority.signature)
	}
}

// A publisher that stops signing, or a package nobody signed put where a signed
// one used to be, is the one unsigned case that is never quiet. Nothing failed
// and nothing was refused, which is exactly why this line is the only place it
// shows up on a host that takes unsigned packages.
func TestAnOriginThatWasSignedAndArrivesUnsignedIsReported(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	digest := contentDigest([]byte("the image that was signed"))
	attachSigned(t, registry, digest, 1, []byte(`{"bundle":"signed"}`))
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	signed := verifiedApplication()
	signed.Image = ref.ContextName() + ":main"
	signed.ImageDigest = digest
	signed.CpakId = testCpakId("branch", "main")
	signed.Version = "main"
	seedApplication(t, cp, signed)

	unsigned := signed
	unsigned.CpakId = testCpakId("branch", "stable")
	unsigned.Version = "stable"
	unsigned.Branch = "stable"
	unsigned.ImageDigest = contentDigest([]byte("an image nobody signed"))
	seedApplication(t, cp, unsigned)

	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})
	if enrolment := cp.EnrolPublishedApplication(signed, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the signed installation to be enrolled first", enrolment.Outcome, enrolment.Reason)
	}

	var enrolment ApplicationEnrolment
	reported := captureStderr(t, func() {
		enrolment = cp.EnrolPublishedApplication(unsigned, publishedTestPackage(t))
	})
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the unsigned installation to be enrolled anyway", enrolment.Outcome, enrolment.Reason)
	}
	if !enrolment.Signature.Unsigned() {
		t.Fatalf("got signature %+v, want the installation that arrived to be unsigned", enrolment.Signature)
	}
	if !strings.Contains(reported, testOrigin) || !strings.Contains(reported, "was signed") {
		t.Fatalf("got %q on the error stream, want the downgrade of %s reported without anybody asking for detail", reported, testOrigin)
	}
}

// The same line stays out of the way when nothing was lost. An origin nobody
// ever signed is a verbose detail, or every install of every unsigned package
// warns and the one warning that means something is lost among them.
func TestAnOriginNobodyEverSignedIsNotReportedAsADowngrade(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	app := installedFromRegistry(t, cp, registry, contentDigest([]byte("an image nobody signed")))

	var enrolment ApplicationEnrolment
	reported := captureStderr(t, func() {
		enrolment = cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	})
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the unsigned installation to be enrolled", enrolment.Outcome, enrolment.Reason)
	}
	if reported != "" {
		t.Fatalf("got %q on the error stream, want an origin nobody ever signed to be enrolled quietly", reported)
	}
}

// The lock is half of a signed state and it belongs to one package. An install
// resolved through a lock installs the dependencies that lock pins too, and
// naming their states through it would describe something no publisher of
// theirs ever signed.
func TestSignedLockTravelsOnlyWithThePackageItIsRootedAt(t *testing.T) {
	lock := &types.ManifestLock{
		LockVersion:  types.ManifestLockVersion,
		Root:         types.LockedPackage{Origin: strings.ToUpper(testOrigin)},
		Dependencies: []types.LockedPackage{{Origin: "github.com/user/runtime"}},
	}
	if got := signedLock(testOrigin, lock); got != lock {
		t.Fatalf("got %+v, want the lock the installed package is the root of", got)
	}
	if got := signedLock("github.com/user/runtime", lock); got != nil {
		t.Fatalf("got %+v, want a dependency to carry no lock into its signed state", got)
	}
	if got := signedLock(testOrigin, nil); got != nil {
		t.Fatalf("got %+v, want nothing named when there is no lock", got)
	}
}

// A registry that will not answer is cpak failing to find out, and it used to
// be reported as a package claiming a publisher it does not have. Nothing about
// the installation is refused either way, so the line is all the operator gets
// and it has to say which of the two happened.
func TestARegistryThatWillNotAnswerIsNotReportedAsAFailedSignature(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	app := enrolledApplication(t, cp)
	app.Image = strings.TrimPrefix(server.URL, "http://") + "/example/app:main"
	app.ImageDigest = contentDigest([]byte("an image the registry will not talk about"))
	seedApplication(t, cp, app)

	var enrolment ApplicationEnrolment
	reported := captureStderr(t, func() {
		enrolment = cp.EnrolPublishedApplication(app, publishedTestPackage(t))
	})
	if enrolment.Outcome != EnrolmentRecorded {
		t.Fatalf("got outcome %s (%v), want the installation to be enrolled anyway", enrolment.Outcome, enrolment.Reason)
	}
	if enrolment.Signature.Verified || enrolment.Signature.Unsigned() {
		t.Fatalf("got signature %+v, want a registry that would not answer to be neither signed nor unsigned", enrolment.Signature)
	}
	if !strings.Contains(reported, "could not be found out") {
		t.Fatalf("got %q on the error stream, want it to say who published %s could not be found out", reported, testOrigin)
	}
	if strings.Contains(reported, "is attached to") {
		t.Fatalf("got %q on the error stream, want no claim that a signature is attached to %s", reported, testOrigin)
	}
}

// The other half of the same line. Something is claiming to be the publisher
// and is not, which is a different sentence from a registry that would not
// answer, and it keeps saying so.
func TestABundleThatDoesNotStandIsReportedAsAnAttachedSignature(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image somebody signed"))
	attachSigned(t, registry, digest, 1, []byte("a bundle that does not stand"))
	app := installedFromRegistry(t, cp, registry, digest)
	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("no transparency log holds this")
	})

	reported := captureStderr(t, func() {
		if enrolment := cp.EnrolPublishedApplication(app, publishedTestPackage(t)); enrolment.Outcome != EnrolmentRecorded {
			t.Errorf("got outcome %s (%v), want the installation to be enrolled anyway", enrolment.Outcome, enrolment.Reason)
		}
	})
	if !strings.Contains(reported, "A publisher signature is attached to "+testOrigin) {
		t.Fatalf("got %q on the error stream, want a bundle that does not stand reported as one attached to %s", reported, testOrigin)
	}
}

// The question a host policy exists to answer, asked of the path a user runs.
// Until the install called the verifier, nothing could acquire a signature at
// all, so a host set to required could enrol nothing whatever it installed.
func TestRequiredSignaturesInstallAndEnrolASignedPackage(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	requireSignatures(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	digest := publishImage(t, registry, "main")
	attachSigned(t, registry, digest, 1, []byte(`{"bundle":"signed"}`))
	useSignatureVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: publisherIdentity(testOrigin)}, nil
	})

	manifest := newTestManifest()
	manifest.Image = ref.ContextName() + ":main"
	options := InstallOptions{CreateExports: true, ResolveImageRef: true}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("a signed package did not install on a host that takes only signed packages: %v", err)
	}
	if authority.signed != 1 {
		t.Fatalf("the authority was handed %d signatures, want the one the install verified", authority.signed)
	}
	if _, held := authority.holds(t, testOrigin); !held {
		t.Fatal("a signed package was not enrolled on a host that takes only signed packages")
	}
}

// The same host and an unsigned package: the software installs, because an
// enrolment never fails an install, and the ledger does not answer for it. That
// is the policy working, and it is the state the gate refuses launches from.
func TestRequiredSignaturesLeaveAnUnsignedInstallUnenrolled(t *testing.T) {
	cp := newSignatureCpak(t)
	authority := useEnrolmentAuthority(t)
	requireSignatures(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	publishImage(t, registry, "main")

	manifest := newTestManifest()
	manifest.Image = ref.ContextName() + ":main"
	options := InstallOptions{CreateExports: true, ResolveImageRef: true}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("an unsigned package did not install on a host that takes only signed packages: %v", err)
	}
	if installed := storedApplications(t, cp); len(installed) != 1 {
		t.Fatalf("got %+v, want the installation to stand although it was not enrolled", installed)
	}
	if authority.recorded != 0 {
		t.Fatalf("the authority was asked %d times to record an unsigned enrolment", authority.recorded)
	}
	if _, held := authority.holds(t, testOrigin); held {
		t.Fatal("the ledger answers for an application this host refused to enrol")
	}
}

// The authority refuses a signed enrolment whose anchor does not state the
// image and the manifest it describes, and it validates that client side before
// any transport. An enroller that leaves them out therefore makes every signed
// installation unenrollable, which is what shipped once already because the
// stub authority in this file records an anchor the real one would refuse.
func TestTheAnchorAnEnrolmentBuildsStatesWhatASignatureIsCheckedAgainst(t *testing.T) {
	cp := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	digest := contentDigest([]byte("the image the anchor has to name"))
	app := installedFromRegistry(t, cp, registry, digest)
	published := publishedTestPackage(t)

	if outcome := cp.EnrolPublishedApplication(app, published); outcome.Outcome != EnrolmentRecorded {
		t.Fatalf("the application was not enrolled: %v", outcome.Reason)
	}
	anchor, held := authority.holds(t, app.Origin)
	if !held {
		t.Fatal("the ledger holds nothing for an application it just enrolled")
	}
	if anchor.ImageDigest != app.ImageDigest {
		t.Fatalf("the anchor states image %q, want the one the installation resolved, %q", anchor.ImageDigest, app.ImageDigest)
	}
	configured, err := manifestDigest(published.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.ManifestDigest != configured {
		t.Fatalf("the anchor states manifest %q, want the manifest the installation was configured by, %q", anchor.ManifestDigest, configured)
	}
	// The values have to be the shape a signed state names, or the authority
	// refuses them where they are written rather than where they are compared.
	if err := anchor.ValidateDigests(); err != nil {
		t.Fatalf("the anchor an enrolment built is not one the authority accepts: %v", err)
	}
}
