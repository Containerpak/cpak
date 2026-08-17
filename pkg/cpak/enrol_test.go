/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// enrolmentAuthority stands in for the privileged side. It is the same ledger
// the gate reads, entered through the calls an enrolment makes and through the
// validation the authority applies, so what a test enrols is what a launch of
// it finds and a policy that does not hash to its root is refused here too.
type enrolmentAuthority struct {
	ledger    systemauthority.AnchorLedger
	recorded  int
	forgotten int
	policy    *types.Override
	refusal   error
}

func useEnrolmentAuthority(t *testing.T) *enrolmentAuthority {
	t.Helper()

	authority := &enrolmentAuthority{ledger: useAnchorLedger(t)}
	savedRecord, savedForget := recordAnchor, forgetAnchor
	t.Cleanup(func() {
		recordAnchor = savedRecord
		forgetAnchor = savedForget
	})
	recordAnchor = func(anchor integrity.Anchor, policy *types.Override) error {
		authority.recorded++
		authority.policy = policy
		if authority.refusal != nil {
			return authority.refusal
		}
		return authority.ledger.Record(systemauthority.Enrolment{Anchor: anchor, Policy: policy})
	}
	forgetAnchor = func(uid uint32, origin string) error {
		authority.forgotten++
		if authority.refusal != nil {
			return authority.refusal
		}
		return authority.ledger.Forget(uid, origin)
	}
	return authority
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
		imageDigest: "sha256:new",
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
		imageDigest: "sha256:new",
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
	if len(stored) != 1 || stored[0].ImageDigest != "sha256:new" {
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
