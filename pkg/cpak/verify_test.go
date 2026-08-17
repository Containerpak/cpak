/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
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

// useEnforcement drives the level the gate reads, the way useAnchorLedger
// drives the ledger. Nothing in a test may read the real one: it belongs to the
// host, and a test that depended on it would pass or fail by machine.
func useEnforcement(t *testing.T, level systemauthority.EnforcementLevel) {
	t.Helper()

	saved := launchEnforcement
	t.Cleanup(func() { launchEnforcement = saved })
	launchEnforcement = func() systemauthority.EnforcementLevel { return level }
}

// captureStderr answers with what was written to the error stream while the
// given work ran. It replaces the descriptor rather than the variable, because
// the logger took hold of os.Stderr when its package was initialised and would
// not notice a variable moving under it.
func captureStderr(t *testing.T, during func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("the error stream could not be captured: %v", err)
	}
	defer reader.Close()
	saved, err := unix.Dup(unix.Stderr)
	if err != nil {
		t.Skipf("the error stream cannot be duplicated here: %v", err)
	}
	if err := unix.Dup3(int(writer.Fd()), unix.Stderr, 0); err != nil {
		_ = unix.Close(saved)
		t.Skipf("the error stream cannot be redirected here: %v", err)
	}
	collected := make(chan string, 1)
	go func() {
		written, _ := io.ReadAll(reader)
		collected <- string(written)
	}()
	during()
	if err := unix.Dup3(saved, unix.Stderr, 0); err != nil {
		t.Fatalf("the error stream was not put back: %v", err)
	}
	_ = unix.Close(saved)
	// The reader sees the end of the pipe only once no descriptor is left open
	// on the other side, and the one above has just stopped being one.
	_ = writer.Close()
	return <-collected
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

// The three levels are the whole of the enforcement decision, so each one is
// pinned on its own rather than followed through a branch.
func TestGateLaunchLetsAnUnenrolledLaunchThroughWhileEnforcementIsOff(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementOff)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	if _, err := cp.gateLaunch(verifiedApplication(), types.Override{}, nil, nil); err != nil {
		t.Fatalf("got %v, want an unenrolled launch to start while enforcement is off", err)
	}
}

func TestGateLaunchWarnsAboutAnUnenrolledLaunchWithoutRefusingIt(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementWarn)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	var err error
	reported := captureStderr(t, func() {
		_, err = cp.gateLaunch(verifiedApplication(), types.Override{}, nil, nil)
	})
	if err != nil {
		t.Fatalf("got %v, want warn to refuse nothing", err)
	}
	said := testOrigin + " is " + LaunchUnenrolled.String()
	if !strings.Contains(reported, said) {
		t.Fatalf("got %q on the error stream, want warn to say %q", reported, said)
	}
}

// Off is what every host is on until somebody says otherwise, so it has to stay
// as quiet as it was: a warning at every launch of every application is a
// warning nobody reads by the second day.
func TestGateLaunchSaysNothingAboutAnUnenrolledLaunchWhileEnforcementIsOff(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementOff)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	reported := captureStderr(t, func() {
		if _, err := cp.gateLaunch(verifiedApplication(), types.Override{}, nil, nil); err != nil {
			t.Errorf("got %v, want an unenrolled launch to start while enforcement is off", err)
		}
	})
	if reported != "" {
		t.Fatalf("got %q on the error stream, want off to say nothing about an unenrolled launch", reported)
	}
}

func TestGateLaunchRefusesAnUnenrolledLaunchWhenEnforcementRefuses(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementRefuse)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	identity, err := cp.gateLaunch(verifiedApplication(), types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchUnenrolled) {
		t.Fatalf("got %v, want an unenrolled launch to be refused while enforcement refuses", err)
	}
	if identity.Verdict != LaunchUnenrolled {
		t.Fatalf("got verdict %s, want an application nothing was recorded for", identity.Verdict)
	}
}

// A ledger that will not answer is not the same as a ledger that answered with
// nothing, and enforcement has to cover it: an attacker who can stop the ledger
// being read must not thereby stop it being enforced.
func TestGateLaunchRefusesALedgerThatCannotAnswerWhenEnforcementRefuses(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	app := verifiedApplication()
	app.Origin = "GitHub.com/User/Demo"

	useEnforcement(t, systemauthority.EnforcementOff)
	if _, err := cp.gateLaunch(app, types.Override{}, nil, nil); err != nil {
		t.Fatalf("got %v, want a ledger that could not answer to be allowed while enforcement is off", err)
	}

	useEnforcement(t, systemauthority.EnforcementRefuse)
	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchUnenrolled) {
		t.Fatalf("got %v, want a ledger that could not answer to be refused while enforcement refuses", err)
	}
	if identity.Verdict != LaunchUnverifiable {
		t.Fatalf("got verdict %s, want a ledger that could not answer", identity.Verdict)
	}
}

func TestGateLaunchStartsARecognisedLaunchWhileEnforcementRefuses(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementRefuse)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	app := verifiedApplication()
	derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)

	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatalf("got %v, want an enrolled launch to start at the strictest level", err)
	}
	if identity.Verdict != LaunchRecognised {
		t.Fatalf("got verdict %s, want the launch that was enrolled to be recognised", identity.Verdict)
	}
}

// This is the line enforcement must never cross. A launch that is not the one
// its anchor names is known to be wrong, and no level of a switch an
// administrator owns turns that into a launch.
func TestGateLaunchRefusesAnUnrecognisedLaunchAtEveryEnforcementLevel(t *testing.T) {
	for _, level := range []systemauthority.EnforcementLevel{
		systemauthority.EnforcementOff,
		systemauthority.EnforcementWarn,
		systemauthority.EnforcementRefuse,
	} {
		cp := newTestCpak(t)
		ledger := useAnchorLedger(t)
		useEnforcement(t, level)
		bindLayer(t, cp, verifiedBaseLayer, "state-base")
		bindLayer(t, cp, verifiedTopLayer, "state-top")

		app := verifiedApplication()
		derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		enrol(t, ledger, derived)

		if _, err := cp.gateLaunch(app, types.Override{FsHost: true}, nil, nil); !errors.Is(err, errLaunchUnrecognised) {
			t.Fatalf("got %v at level %s, want a launch its anchor does not cover to be refused at every level", err, level)
		}
	}
}

// The same line from the other side: a store that contradicts what it wrote
// down about itself is wrong whether or not anything ever claimed the
// application, so no level lets it start.
func TestGateLaunchRefusesATamperedLaunchAtEveryEnforcementLevel(t *testing.T) {
	for _, level := range []systemauthority.EnforcementLevel{
		systemauthority.EnforcementOff,
		systemauthority.EnforcementWarn,
		systemauthority.EnforcementRefuse,
	} {
		cp := newTestCpak(t)
		ledger := useAnchorLedger(t)
		useEnforcement(t, level)
		seedFVSLayerFile(t, cp, measuredLayer, "usr/share/value", []byte("value"))
		if err := cp.recordLayerBinding(measuredLayer); err != nil {
			t.Fatalf("the layer binding was refused: %v", err)
		}
		// Enrolled, so that the refusal after the tamper can only be about the
		// store contradicting itself and never about the level.
		app := measuredApplication(measuredLayer)
		derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		enrol(t, ledger, derived)
		if _, err := cp.gateLaunch(app, types.Override{}, nil, nil); err != nil {
			t.Fatalf("got %v at level %s, want a launch nothing had touched to start", err, level)
		}

		rebuildFVSLayer(t, cp, measuredLayer, []byte("something else entirely"))
		if _, err := cp.gateLaunch(app, types.Override{}, nil, nil); !errors.Is(err, errLaunchTampered) {
			t.Fatalf("got %v at level %s, want a store that contradicts itself to be refused at every level", err, level)
		}
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

// installApplication puts an application in the store the way everything else
// in cpak finds one, so that a report resolves it the way a launch does.
func installApplication(t *testing.T, cp *Cpak, app types.Application) types.Application {
	t.Helper()

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.NewApplication(app); err != nil {
		t.Fatalf("the application could not be put in the store: %v", err)
	}
	return app
}

func anchorStateOf(t *testing.T, cp *Cpak, origin string) AnchorState {
	t.Helper()

	states, err := cp.AnchorStates()
	if err != nil {
		t.Fatalf("what the ledger holds could not be read: %v", err)
	}
	for _, state := range states {
		if state.Origin == origin {
			if state.Unreadable != "" {
				t.Fatalf("the ledger would not answer for %s: %s", origin, state.Unreadable)
			}
			if state.Underived != "" {
				t.Fatalf("no launch root follows from the store for %s: %s", origin, state.Underived)
			}
			return state
		}
	}
	t.Fatalf("%s is installed and was left out of the report", origin)
	return AnchorState{}
}

func TestAnchorStatesDescribesAnApplicationNothingWasRecordedFor(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	app := installApplication(t, cp, verifiedApplication())

	state := anchorStateOf(t, cp, app.Origin)
	if state.Enrolled {
		t.Fatal("an application nothing was ever recorded for is reported as enrolled")
	}
	if state.Recognised() {
		t.Fatal("an application nothing was ever recorded for is reported as recognised")
	}
	// Without this the report would say an application is not enrolled and
	// leave the reader with no value to enrol it against.
	if state.DerivedRoot == "" {
		t.Fatal("the launch root the store derives was left out, so nothing could be compared with an anchor")
	}
}

func TestAnchorStatesRecognisesTheLaunchTheLedgerHolds(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	app := installApplication(t, cp, verifiedApplication())

	derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)

	state := anchorStateOf(t, cp, app.Origin)
	if !state.Enrolled {
		t.Fatal("an application the ledger answers for is reported as unenrolled")
	}
	if state.Generation != 1 {
		t.Fatalf("got generation %d, want the one the anchor was written at", state.Generation)
	}
	if !state.Recognised() {
		t.Fatalf("the store derives %s where the anchor holds %s, and neither moved", state.DerivedRoot, state.AnchorRoot)
	}
}

func TestAnchorStatesReportsAnAnchorTheStoreNoLongerDerives(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	app := installApplication(t, cp, verifiedApplication())

	policyRoot, err := integrity.PolicyRoot(types.Override{})
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := strings.Repeat("c", 64)
	enrol(t, ledger, LaunchIdentity{
		UID:         uint32(os.Getuid()),
		Origin:      app.Origin,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  integrity.LaunchRoot(packageRoot, policyRoot),
	})

	state := anchorStateOf(t, cp, app.Origin)
	if !state.Enrolled {
		t.Fatal("an application the ledger answers for is reported as unenrolled")
	}
	if state.Recognised() {
		t.Fatal("an anchor the store no longer derives is reported as recognised")
	}
	if state.AnchorRoot == "" || state.DerivedRoot == "" {
		t.Fatalf("got anchor root %q and derived root %q, want both so the difference can be read", state.AnchorRoot, state.DerivedRoot)
	}
}

func TestExplainLaunchReportsTheRefusalAnUnenrolledLaunchWouldGet(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementRefuse)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")
	app := installApplication(t, cp, verifiedApplication())

	explanation, err := cp.ExplainLaunch(app.Origin)
	if err != nil {
		t.Fatalf("an installed application could not be explained: %v", err)
	}
	if explanation.Enforcement != systemauthority.EnforcementRefuse {
		t.Fatalf("got enforcement %s, want the level the gate reads", explanation.Enforcement)
	}
	if explanation.Enrolled {
		t.Fatal("an application nothing was recorded for is explained as enrolled")
	}
	if !errors.Is(explanation.Refusal, errLaunchUnenrolled) {
		t.Fatalf("got %v, want the refusal the gate gives while enforcement refuses", explanation.Refusal)
	}

	derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)

	explanation, err = cp.ExplainLaunch(app.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if !explanation.Enrolled || explanation.Refusal != nil {
		t.Fatalf("got enrolled %v and refusal %v, want the enrolled launch to be explained as one that starts", explanation.Enrolled, explanation.Refusal)
	}
	if explanation.Anchor.LaunchRoot != explanation.Identity.LaunchRoot {
		t.Fatalf("the ledger holds %s and the launch derives %s, and the two were reported as agreeing", explanation.Anchor.LaunchRoot, explanation.Identity.LaunchRoot)
	}
}

// A store that contradicts itself is answered before the ledger is asked, so
// the verdict carries no anchor. The report has to read the ledger for itself,
// otherwise the one moment a person most needs to see what was recorded is the
// moment it disappears from the report.
func TestExplainLaunchShowsTheAnchorOfAnApplicationTheStoreContradicts(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementOff)
	seedFVSLayerFile(t, cp, measuredLayer, "usr/share/value", []byte("value"))
	if err := cp.recordLayerBinding(measuredLayer); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
	app := installApplication(t, cp, measuredApplication(measuredLayer))

	derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)
	rebuildFVSLayer(t, cp, measuredLayer, []byte("something else entirely"))

	explanation, err := cp.ExplainLaunch(app.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.Identity.Verdict != LaunchTampered {
		t.Fatalf("got verdict %s, want the store to be reported as contradicting itself", explanation.Identity.Verdict)
	}
	if !explanation.Enrolled || explanation.Anchor.LaunchRoot == "" {
		t.Fatal("the anchor of an enrolled application vanished from the report as soon as the store contradicted itself")
	}
	if !errors.Is(explanation.Refusal, errLaunchTampered) {
		t.Fatalf("got %v, want the refusal a tampered launch gets while enforcement is off", explanation.Refusal)
	}
}

// A report must not be the thing that starts recording. Opening the bindings
// ledger creates it, and a store that holds none is exactly the store this
// report exists for.
func TestAnchorStatesLeavesAStoreThatRecordsNothingAlone(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	app := installApplication(t, cp, verifiedApplication())

	states, err := cp.AnchorStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Origin != app.Origin {
		t.Fatalf("got %d states, want the one application that is installed", len(states))
	}
	if states[0].Underived == "" {
		t.Fatal("a store that records nothing was reported as deriving a launch root")
	}
	if _, err := os.Stat(cp.GetInStoreDir("bindings")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want the report to leave a store that records nothing without a ledger", err)
	}
}
