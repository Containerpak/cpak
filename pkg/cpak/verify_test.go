/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	verifiedBaseLayer  = "4444444444444444444444444444444444444444444444444444444444444444"
	verifiedTopLayer   = "5555555555555555555555555555555555555555555555555555555555555555"
	verifiedAddonLayer = "6666666666666666666666666666666666666666666666666666666666666666"
)

// useAnchorLedger points the gate at a ledger the test owns, the way the
// authority tests swap the install prefixes.
func useAnchorLedger(t *testing.T) systemauthority.AnchorLedger {
	t.Helper()

	ledger := systemauthority.AnchorLedger{
		Directory: filepath.Join(t.TempDir(), "integrity"),
		OwnerUID:  uint32(os.Getuid()),
	}
	saved := launchAnchors
	t.Cleanup(func() { launchAnchors = saved })
	launchAnchors = ledger
	return ledger
}

func bindLayer(t *testing.T, cp *Cpak, digest, state string) {
	t.Helper()

	bindings, err := cp.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := integrity.LayerBinding{OCIDigest: digest, StateID: state, StateRoot: "root-" + state}
	if err := bindings.Bind(binding); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
}

func verifiedApplication() types.Application {
	return types.Application{
		CpakId:         "verified",
		Name:           "demo",
		Version:        "1.0",
		Branch:         "main",
		Origin:         testOrigin,
		ImageDigest:    "sha256:image",
		Config:         `{"config":{"Env":["PATH=/usr/bin"]}}`,
		ParsedLayers:   []string{verifiedBaseLayer, verifiedTopLayer},
		ParsedBinaries: []string{"/usr/bin/demo"},
	}
}

// enrol records the launch the gate has just derived, which is what an install
// will do once enrolment exists.
func enrol(t *testing.T, ledger systemauthority.AnchorLedger, identity LaunchIdentity) {
	t.Helper()

	anchor := integrity.Anchor{
		ABI:         integrity.ABIVersion,
		UID:         identity.UID,
		Origin:      identity.Origin,
		Generation:  1,
		PackageRoot: identity.PackageRoot,
		PolicyRoot:  identity.PolicyRoot,
		LaunchRoot:  identity.LaunchRoot,
	}
	if err := ledger.Store(anchor); err != nil {
		t.Fatalf("the anchor was refused: %v", err)
	}
}

func TestVerifyLaunchDerivesTheLaunchRootFromItsTwoHalves(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	override := types.Override{SocketWayland: true}
	identity, err := cp.verifyLaunch(verifiedApplication(), override, nil, nil)
	if err != nil {
		t.Fatalf("a launch whose layers are all bound could not be derived: %v", err)
	}
	if identity.PackageRoot == "" {
		t.Fatal("the package root is empty although every layer is bound")
	}
	policyRoot, err := integrity.PolicyRoot(override)
	if err != nil {
		t.Fatal(err)
	}
	if identity.PolicyRoot != policyRoot {
		t.Fatalf("got policy root %q, want the one the override hashes to %q", identity.PolicyRoot, policyRoot)
	}
	if identity.LaunchRoot != integrity.LaunchRoot(identity.PackageRoot, identity.PolicyRoot) {
		t.Fatal("the launch root does not follow from the package and policy roots it was made of")
	}
	if identity.UID != uint32(os.Getuid()) {
		t.Fatalf("got uid %d, want the account asking for the launch %d", identity.UID, os.Getuid())
	}
}

// The digest names what was downloaded, the binding names what the store made
// of it. Identity has to cover the second, otherwise a rewritten store state
// launches under the name of the layer it replaced.
func TestVerifyLaunchFollowsTheStateALayerIsBoundTo(t *testing.T) {
	rootFor := func(state string) string {
		cp := newTestCpak(t)
		useAnchorLedger(t)
		bindLayer(t, cp, verifiedBaseLayer, state)
		bindLayer(t, cp, verifiedTopLayer, "state-top")
		identity, err := cp.verifyLaunch(verifiedApplication(), types.Override{}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return identity.PackageRoot
	}

	if rootFor("state-base") == rootFor("state-other") {
		t.Fatal("two different store states produced the same package root")
	}
}

func TestVerifyLaunchSeparatesIdentityFromPolicy(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	app := verifiedApplication()
	narrow, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := cp.verifyLaunch(app, types.Override{FsHostHome: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if narrow.PackageRoot != wide.PackageRoot {
		t.Fatal("narrowing what an application may do changed what it is")
	}
	if narrow.PolicyRoot == wide.PolicyRoot {
		t.Fatal("two different policies produced the same policy root")
	}
	if narrow.LaunchRoot == wide.LaunchRoot {
		t.Fatal("two different policies produced the same launch root")
	}
}

// An addon composes the launch even when it brings no layer of its own, so
// enabling one has to change what the launch is.
func TestVerifyLaunchCoversTheEnabledAddons(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	bindLayer(t, cp, verifiedAddonLayer, "state-addon")

	app := verifiedApplication()
	plain, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	quiet := types.Application{Origin: "github.com/user/addon", ImageDigest: "sha256:addon"}
	withAddon, err := cp.verifyLaunch(app, types.Override{}, nil, []types.Application{quiet})
	if err != nil {
		t.Fatal(err)
	}
	if plain.PackageRoot == withAddon.PackageRoot {
		t.Fatal("an enabled addon left the package root unchanged")
	}
	layered := quiet
	layered.ParsedLayers = []string{verifiedAddonLayer}
	withLayer, err := cp.verifyLaunch(app, types.Override{}, nil, []types.Application{layered})
	if err != nil {
		t.Fatal(err)
	}
	if withLayer.PackageRoot == withAddon.PackageRoot {
		t.Fatal("the layer an addon stacks left the package root unchanged")
	}
}

func TestVerifyLaunchReportsAnApplicationTheLedgerHoldsNothingFor(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	identity, err := cp.verifyLaunch(verifiedApplication(), types.Override{}, nil, nil)
	if err != nil {
		t.Fatalf("an empty ledger was reported as a failure: %v", err)
	}
	if identity.Verdict != LaunchUnenrolled {
		t.Fatalf("got verdict %s, want an application nothing was recorded for", identity.Verdict)
	}
	if identity.LaunchRoot == "" {
		t.Fatal("an unenrolled launch was not described, so nothing could ever be enrolled from it")
	}
}

func TestVerifyLaunchRecognisesAnEnrolledLaunch(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	app := verifiedApplication()
	override := types.Override{SocketWayland: true}
	derived, err := cp.verifyLaunch(app, override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)

	identity, err := cp.gateLaunch(app, override, nil, nil)
	if err != nil {
		t.Fatalf("the launch that was enrolled was refused: %v", err)
	}
	if identity.Verdict != LaunchRecognised {
		t.Fatalf("got verdict %s, want the launch that was enrolled to be recognised", identity.Verdict)
	}
}

func TestGateLaunchRefusesAPolicyTheAnchorDoesNotCover(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	app := verifiedApplication()
	derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)

	identity, err := cp.gateLaunch(app, types.Override{FsHost: true}, nil, nil)
	if !errors.Is(err, errLaunchUnrecognised) {
		t.Fatalf("got %v, want a launch that does not match its anchor to be refused", err)
	}
	if identity.Verdict != LaunchUnrecognised {
		t.Fatalf("got verdict %s, want a launch that does not match its anchor", identity.Verdict)
	}
}

// An enrolled application whose layers the store cannot answer for is not the
// application that was enrolled, whatever else agrees.
func TestGateLaunchRefusesAnEnrolledLaunchWhoseLayersAreNotBound(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)

	app := verifiedApplication()
	policyRoot, err := integrity.PolicyRoot(types.Override{})
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := strings.Repeat("a", 64)
	enrol(t, ledger, LaunchIdentity{
		UID:         uint32(os.Getuid()),
		Origin:      app.Origin,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  integrity.LaunchRoot(packageRoot, policyRoot),
	})

	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchUnbound) {
		t.Fatalf("got %v, want an enrolled launch with an unbound layer to be refused", err)
	}
	if identity.Verdict != LaunchUnbound {
		t.Fatalf("got verdict %s, want a launch whose layers are not bound", identity.Verdict)
	}
}

// A ledger that cannot name the origin has no anchor for it and can never have
// one, so it needs the same answer an application nobody enrolled needs.
func TestVerifyLaunchReportsALedgerThatCannotAnswer(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	app := verifiedApplication()
	app.Origin = "GitHub.com/User/Demo"
	identity, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatalf("a ledger that cannot answer was reported as a failure: %v", err)
	}
	if identity.Verdict != LaunchUnverifiable {
		t.Fatalf("got verdict %s, want a ledger that could not answer", identity.Verdict)
	}
	if identity.Reason == nil {
		t.Fatal("the ledger refused to answer without saying why")
	}
}

// The switch is what the whole permissive posture rests on, so the test follows
// it instead of restating it: the day it turns to true, this pins the refusal.
func TestGateLaunchFollowsTheUnenrolledSwitch(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	_, err := cp.gateLaunch(verifiedApplication(), types.Override{}, nil, nil)
	if refuseUnenrolledLaunch {
		if !errors.Is(err, errLaunchUnenrolled) {
			t.Fatalf("got %v, want an unenrolled launch to be refused while the switch is on", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("got %v, want an unenrolled launch to be allowed while the switch is off", err)
	}
}

func TestContainerPolicyHashFollowsTheLaunchRoot(t *testing.T) {
	override := types.Override{SocketWayland: true}
	previous, err := containerPolicyHash(override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rootless, err := containerLaunchPolicyHash(containerRuntimePolicyVersion, "", override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rootless != previous {
		t.Fatal("a launch that carries no root changed the key its container is reused by")
	}
	first, err := containerLaunchPolicyHash(containerRuntimePolicyVersion, strings.Repeat("a", 64), override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == previous {
		t.Fatal("the launch root did not reach the key a container is reused by")
	}
	second, err := containerLaunchPolicyHash(containerRuntimePolicyVersion, strings.Repeat("b", 64), override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("two different launch roots produced the same reuse key")
	}
}
