/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// launchEnforcement answers with what this host does about a launch no anchor
// was ever recorded for. The level is held beside the ledger and owned by the
// same account, so the side a refusal is aimed at cannot decide there is no
// refusal. It is a variable so that a test can drive the three levels; nothing
// in cpak replaces it.
var launchEnforcement = systemauthority.Enforcement

// launchAnchors is the ledger a launch is checked against. It is a variable so
// that a test can point it at a ledger it owns; nothing in cpak replaces it.
var launchAnchors integrity.Anchors = systemauthority.DefaultAnchorLedger()

var (
	errLaunchUnenrolled   = errors.New("the application is not enrolled for verified launch")
	errLaunchUnbound      = errors.New("a layer of the application is not bound to a store state")
	errLaunchUnrecognised = errors.New("the application does not match the integrity anchor it was enrolled with")
	errLaunchTampered     = errors.New("the store no longer holds what it recorded for a layer of the application")
)

// LaunchVerdict is what comparing a launch against the ledger concluded. The
// zero value is deliberately not a conclusion, so a result nobody filled in is
// never read as a pass.
type LaunchVerdict uint8

const (
	// LaunchUnverifiable means the ledger could not answer for this launch, so
	// it is not known whether anything claims what the application should be.
	LaunchUnverifiable LaunchVerdict = iota + 1

	// LaunchUnenrolled means the ledger answered and holds nothing.
	LaunchUnenrolled

	// LaunchUnbound means the application is enrolled but the store cannot say
	// which state one of its layers became, so the launch cannot be described.
	LaunchUnbound

	LaunchRecognised
	LaunchUnrecognised

	// LaunchTampered means the store and what the store recorded about itself
	// do not agree. It is deliberately not LaunchUnrecognised: that one says
	// the launch is not the one an anchor names, this one says the store
	// contradicts itself, and the second is true whether or not anything was
	// ever enrolled.
	LaunchTampered
)

func (v LaunchVerdict) String() string {
	switch v {
	case LaunchUnverifiable:
		return "unverifiable"
	case LaunchUnenrolled:
		return "not enrolled"
	case LaunchUnbound:
		return "layers not bound"
	case LaunchRecognised:
		return "recognised"
	case LaunchUnrecognised:
		return "not recognised"
	case LaunchTampered:
		return "store contents changed"
	}
	return "not verified"
}

// LaunchIdentity is what a launch was found to be, next to what the ledger says
// it should be. It states, it does not decide: the caller reads the verdict and
// answers for it.
type LaunchIdentity struct {
	Verdict     LaunchVerdict
	UID         uint32
	Origin      string
	PackageRoot string
	PolicyRoot  string
	LaunchRoot  string
	Anchor      integrity.Anchor

	// Reason carries what stopped the ledger from answering, and is set only
	// for LaunchUnverifiable.
	Reason error

	// Disagreements names what the store no longer holds as it recorded it,
	// one line per layer, and is set only for LaunchTampered.
	Disagreements []string

	// Unmeasured names the layers this launch could not re-derive from the
	// store at all. It is filled whatever the verdict, so that a launch
	// nothing contradicted is never mistaken for a launch that was checked.
	Unmeasured []string
}

// verifyLaunch derives what this launch is and compares it with the anchor
// recorded for the account asking for it. A ledger that cannot answer is a
// verdict and not a failure, because the caller needs the same decision it
// needs for an application nobody enrolled.
//
// The derivation is done twice on purpose. The roots are built out of what the
// store recorded, because that is what an anchor was taken over. The
// measurement rebuilds the same values from the store as it stands, because a
// record read back proves only that a record exists.
// verifyLaunch derives what an ordinary application launch is. A session has
// its own policy and its own recorded root, so it goes through verifySessionLaunch.
func (c *Cpak) verifyLaunch(app types.Application, override types.Override, components, addons []types.Application) (LaunchIdentity, error) {
	return c.verifySessionLaunch(app, override, "", components, addons)
}

func (c *Cpak) verifySessionLaunch(app types.Application, override types.Override, sessionID string, components, addons []types.Application) (LaunchIdentity, error) {
	identity := LaunchIdentity{UID: uint32(os.Getuid()), Origin: app.Origin}

	packageRoot, err := c.launchPackageRoot(app, components, addons)
	unbound := errors.Is(err, errLaunchUnbound)
	if err != nil && !unbound {
		return identity, err
	}
	policyRoot, err := integrity.PolicyRoot(override)
	if err != nil {
		return identity, fmt.Errorf("derive the policy root of %s: %w", app.Origin, err)
	}
	identity.PolicyRoot = policyRoot
	if !unbound {
		identity.PackageRoot = packageRoot
		identity.LaunchRoot = integrity.LaunchRoot(packageRoot, policyRoot)
	}

	measurement, err := c.measureLaunch(composedLayers(app, components, addons))
	if err != nil {
		return identity, err
	}
	identity.Unmeasured = findingReasons(measurement.Unmeasured)

	// A store that contradicts itself is not a question for the ledger, so it
	// is answered before the ledger is asked. Being unenrolled means nobody
	// said what the launch should be; it does not mean the store may disagree
	// with what it wrote down about itself.
	if len(measurement.Disagreements) > 0 {
		identity.Verdict = LaunchTampered
		identity.Disagreements = findingReasons(measurement.Disagreements)
		return identity, nil
	}

	anchor, enrolled, err := launchAnchors.Load(identity.UID, app.Origin)
	if err != nil {
		identity.Verdict = LaunchUnverifiable
		identity.Reason = err
		return identity, nil
	}
	identity.Anchor = anchor

	// Being enrolled comes first: what the layers of an application are cannot
	// be a disagreement until something claims what they should be.
	// A session is recognised by its own root, because it is started with its
	// own policy. An anchor written before sessions were recorded holds none,
	// and that is an unenrolled launch rather than a wrong one: nothing ever
	// claimed what this session should be, so nothing can say it is not it.
	recorded := anchor.LaunchRoot
	if sessionID != "" {
		known, held := anchor.SessionRoots[sessionID]
		if !held {
			identity.Verdict = LaunchUnenrolled
			return identity, nil
		}
		recorded = known
	}

	switch {
	case !enrolled:
		identity.Verdict = LaunchUnenrolled
	case unbound || len(measurement.Unrecorded) > 0:
		identity.Verdict = LaunchUnbound
	case identity.LaunchRoot == recorded:
		identity.Verdict = LaunchRecognised
	default:
		identity.Verdict = LaunchUnrecognised
	}
	return identity, nil
}

// gateLaunch is the last word before anything of an application is mounted. It
// turns the verdict into an answer, which is the one thing verifyLaunch
// deliberately does not do, and says on the way what the answer let through.
func (c *Cpak) gateLaunch(app types.Application, override types.Override, components, addons []types.Application) (LaunchIdentity, error) {
	return c.gateSessionLaunch(app, override, "", components, addons)
}

func (c *Cpak) gateSessionLaunch(app types.Application, override types.Override, sessionID string, components, addons []types.Application) (LaunchIdentity, error) {
	identity, err := c.verifySessionLaunch(app, override, sessionID, components, addons)
	if err != nil {
		return identity, err
	}
	level := launchEnforcement()
	announceMeasurementGap(level, app.Origin, identity.Unmeasured)
	refusal := launchRefusal(level, app.Origin, identity)
	if refusal == nil {
		announceUnclaimedLaunch(level, app.Origin, identity)
	}
	return identity, refusal
}

// launchRefusal is the answer itself, and it prints nothing, so that a report
// can ask what a launch would be told without a line claiming a launch
// happened.
//
// Only one verdict is put to the enforcement level. A store that contradicts
// itself, an enrolled application whose layers nothing binds and a launch that
// is not the one an anchor names are refused at every level: enforcement
// decides what happens to what is unknown, never to what is known to be wrong.
func launchRefusal(level systemauthority.EnforcementLevel, origin string, identity LaunchIdentity) error {
	switch identity.Verdict {
	case LaunchRecognised:
		return nil
	case LaunchTampered:
		return fmt.Errorf("%w: %s: %s", errLaunchTampered, origin, strings.Join(identity.Disagreements, "; "))
	case LaunchUnbound:
		return fmt.Errorf("%w: %s", errLaunchUnbound, origin)
	case LaunchUnrecognised:
		return fmt.Errorf("%w: %s", errLaunchUnrecognised, origin)
	case LaunchUnenrolled, LaunchUnverifiable:
		return unclaimedLaunchRefusal(level, origin, identity)
	default:
		return fmt.Errorf("the launch of %s could not be verified", origin)
	}
}

// unclaimedLaunchRefusal answers for a launch nothing claims, which is the one
// answer an administrator owns.
func unclaimedLaunchRefusal(level systemauthority.EnforcementLevel, origin string, identity LaunchIdentity) error {
	if level != systemauthority.EnforcementRefuse {
		return nil
	}
	if identity.Reason != nil {
		return fmt.Errorf("%w: %s is %s: %v", errLaunchUnenrolled, origin, identity.Verdict, identity.Reason)
	}
	return fmt.Errorf("%w: %s is %s", errLaunchUnenrolled, origin, identity.Verdict)
}

// announceMeasurementGap names the layers nothing in the store could speak for.
// Off keeps the rule the permissive posture rests on and says it only when it
// is asked; any other level is a host that wanted to be told, and then the line
// goes to the error stream where a piped stdout cannot swallow it.
func announceMeasurementGap(level systemauthority.EnforcementLevel, origin string, unmeasured []string) {
	if len(unmeasured) == 0 {
		return
	}
	gap := strings.Join(unmeasured, "; ")
	if level == systemauthority.EnforcementOff {
		if isVerbose {
			logger.Printf("The launch of %s holds layers the store cannot re-derive: %s", origin, gap)
		}
		return
	}
	logger.Warnf("The launch of %s holds layers the store cannot re-derive: %s", origin, gap)
}

// announceUnclaimedLaunch reports a launch nothing claims that was let through.
// It is never reached at refuse, where the refusal has already said it.
func announceUnclaimedLaunch(level systemauthority.EnforcementLevel, origin string, identity LaunchIdentity) {
	if identity.Verdict != LaunchUnenrolled && identity.Verdict != LaunchUnverifiable {
		return
	}
	if identity.Reason != nil {
		logger.Warnf("The integrity anchor of %s could not be read: %v", origin, identity.Reason)
		return
	}
	if level == systemauthority.EnforcementWarn {
		logger.Warnf("The launch of %s is %s and was allowed: enforcement is warn. At refuse it does not start.", origin, identity.Verdict)
		return
	}
	// Off is the level of a host that enrolled nothing, so this is most of the
	// applications it has: saying it at every launch teaches the warning away.
	if isVerbose {
		logger.Printf("The launch of %s is %s", origin, identity.Verdict)
	}
}

func (c *Cpak) launchPackageRoot(app types.Application, components, addons []types.Application) (string, error) {
	described, err := c.launchPackage(app, components, addons)
	if err != nil {
		return "", err
	}
	root, err := described.Root()
	if err != nil {
		return "", fmt.Errorf("derive the package root of %s: %w", app.Origin, err)
	}
	return root, nil
}

// launchPackage describes the application the way the ledger names it: what is
// installed, with every layer answered for by the state the store produced when
// that layer was pulled. The layers keep the order they are composed in,
// because that is the order they are stacked in and two orders are two
// different root filesystems.
func (c *Cpak) launchPackage(app types.Application, components, addons []types.Application) (integrity.Package, error) {
	bindings, err := c.layerBindings()
	if err != nil {
		return integrity.Package{}, err
	}
	composed := composedLayers(app, components, addons)
	layers := make([]integrity.LayerBinding, 0, len(composed))
	for _, digest := range composed {
		binding, found, lookupErr := bindings.Lookup(digest)
		if lookupErr != nil {
			return integrity.Package{}, fmt.Errorf("read the binding of layer %s: %w", digest, lookupErr)
		}
		if !found {
			return integrity.Package{}, fmt.Errorf("%w: %s", errLaunchUnbound, digest)
		}
		layers = append(layers, binding)
	}
	// ManifestDigest is left empty because nothing carries it yet: the manifest
	// is read at install time and never recorded, so claiming a digest here
	// would be claiming a value this code invented.
	return integrity.Package{
		Origin:         app.Origin,
		Selector:       applicationSelector(app),
		Version:        app.Version,
		ImageDigest:    app.ImageDigest,
		ConfigDigest:   configurationDigest(app.Config),
		Layers:         layers,
		Dependencies:   dependencyIdentities(app.ParsedDependencies),
		Addons:         addonIdentities(addons),
		Binaries:       app.ParsedBinaries,
		DesktopEntries: app.ParsedDesktopEntries,
		Sessions:       sessionIdentities(app.ParsedSessions),
	}, nil
}

// applicationSelector names how the installation was pinned. The same origin
// followed on a branch and pinned to a commit are two different installations,
// and an anchor for one must not recognise the other.
func applicationSelector(app types.Application) string {
	source := app.SourceType()
	switch source {
	case "branch":
		return source + ":" + app.Branch
	case "release":
		return source + ":" + app.Release
	case "commit":
		return source + ":" + app.Commit
	}
	return source
}

// configurationDigest names the image configuration the container is built
// from. The stored bytes are hashed as they stand, because they are the bytes
// the container is configured with and not a reading of them.
func configurationDigest(configuration string) string {
	sum := sha256.Sum256([]byte(configuration))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// dependencyIdentities names what the application was wired to. The mode is
// part of the name because the same dependency composed as a layer and run
// nested are two different applications.
func dependencyIdentities(dependencies []types.Dependency) []string {
	if len(dependencies) == 0 {
		return nil
	}
	identities := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		mode := "nested"
		if dependency.IsLayer() {
			mode = "layer"
		}
		identities = append(identities, mode+":"+dependency.Origin+":"+dependency.Id)
	}
	return identities
}

// addonIdentities names the addons that are enabled, which is the set that
// composes this launch and not the set the application supports. What an addon
// is made of is already covered by the layers, which one it is is not.
func addonIdentities(addons []types.Application) []string {
	if len(addons) == 0 {
		return nil
	}
	identities := make([]string, 0, len(addons))
	for _, addon := range addons {
		identities = append(identities, addon.Origin+":"+addon.ImageDigest)
	}
	return identities
}

func sessionIdentities(sessions []types.Session) []string {
	if len(sessions) == 0 {
		return nil
	}
	identities := make([]string, 0, len(sessions))
	for _, session := range sessions {
		identities = append(identities, session.ID)
	}
	return identities
}

// Everything below reads the gate instead of passing through it. A refusal on
// its own says a launch did not start; it does not say what the ledger holds,
// what the store now derives, or which of the two moved. These report both
// sides so that a person can read the difference rather than guess at it, and
// they mount nothing and start nothing.

// AnchorState is what the anchor ledger holds for one installed application,
// next to the launch root a launch of it derives now. It answers the question
// the binding counts cannot: whether anything outside the store the user owns
// claims what this application is allowed to be.
//
// What it does not answer is whether the anchor names what the publisher
// shipped. An anchor records the installation as it stood when it was written,
// which is trust on first install and not authenticity.
type AnchorState struct {
	Origin     string `json:"origin"`
	Enrolled   bool   `json:"enrolled"`
	Generation uint64 `json:"generation,omitempty"`

	// AnchorRoot is the launch root the ledger holds and DerivedRoot the one a
	// launch builds from the store as it stands.
	AnchorRoot  string `json:"anchorLaunchRoot,omitempty"`
	DerivedRoot string `json:"derivedLaunchRoot,omitempty"`

	// Unreadable is why the ledger would not answer, and Underived why no
	// launch root follows from the store. They are kept apart because they are
	// two different failures with two different owners: the first is the
	// authority's side, the second is almost always a layer no binding names.
	Unreadable string `json:"unreadable,omitempty"`
	Underived  string `json:"underived,omitempty"`
}

// Recognised reports whether the launch this application derives now is the one
// the ledger holds.
func (s AnchorState) Recognised() bool {
	return s.Enrolled && s.AnchorRoot != "" && s.AnchorRoot == s.DerivedRoot
}

// AnchorStates says, for every installed application, what the ledger holds and
// what a launch derives. It writes nothing.
//
// It deliberately does not re-derive the store the way a launch does: whether
// the store still holds what it recorded is the audit's own subject, reported
// beside this one, and measuring it twice would buy a second walk of every
// prepared checkout to say the same thing.
func (c *Cpak) AnchorStates() ([]AnchorState, error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return nil, err
	}
	// A store that holds no bindings is answered without opening the ledger
	// that would create them. Opening one creates the directory, and a store
	// with nothing recorded is exactly the store this report exists for: it
	// must not be the thing that starts recording.
	recorded := c.integrityRecordsExist()
	uid := uint32(os.Getuid())
	states := make([]AnchorState, 0, len(apps))
	for _, app := range apps {
		states = append(states, c.anchorState(uid, app, recorded))
	}
	return states, nil
}

// anchorState answers for one application. What stopped it is recorded on the
// state instead of returned, because a pass that gives up on the first
// application it cannot read says nothing about the rest, and because a ledger
// that would not answer still leaves a launch root worth showing.
func (c *Cpak) anchorState(uid uint32, app types.Application, recorded bool) AnchorState {
	state := AnchorState{Origin: app.Origin}
	anchor, enrolled, err := launchAnchors.Load(uid, app.Origin)
	if err != nil {
		state.Unreadable = err.Error()
	} else {
		state.Enrolled = enrolled
		state.Generation = anchor.Generation
		state.AnchorRoot = anchor.LaunchRoot
	}
	if !recorded {
		state.Underived = "no layer in this store is bound to a state, so no launch root follows from it"
		return state
	}
	root, err := c.installedLaunchRoot(app)
	if err != nil {
		state.Underived = err.Error()
		return state
	}
	state.DerivedRoot = root
	return state
}

// installedLaunchRoot derives the launch root of an installed application the
// way a launch of it would: the same composed layers and the same effective
// override, so that what comes out can be put beside an anchor and mean the
// same thing.
func (c *Cpak) installedLaunchRoot(app types.Application) (string, error) {
	components, addons, err := c.launchComposition(app)
	if err != nil {
		return "", err
	}
	packageRoot, err := c.launchPackageRoot(app, components, addons)
	if err != nil {
		return "", err
	}
	policyRoot, err := integrity.PolicyRoot(resolvedOverride(app))
	if err != nil {
		return "", fmt.Errorf("derive the policy root of %s: %w", app.Origin, err)
	}
	return integrity.LaunchRoot(packageRoot, policyRoot), nil
}

// LaunchExplanation is one application put beside the anchor recorded for it,
// with the answer the gate gives right now and what that answer was made of.
type LaunchExplanation struct {
	Origin      string
	Version     string
	Enforcement systemauthority.EnforcementLevel

	// Enrolled and Anchor are what the ledger holds, read on their own rather
	// than taken from the verdict: a store that contradicts itself is answered
	// before the ledger is asked, and a report that showed nothing there would
	// be calling an enrolled application unenrolled.
	Enrolled bool
	Anchor   integrity.Anchor

	// AnchorReason is why the ledger would not answer at all.
	AnchorReason error

	// Identity is what a launch of this application derives from the store as
	// it stands.
	Identity LaunchIdentity

	// Refusal is why this launch would not start, and is nil when it would.
	Refusal error
}

// ExplainLaunch derives a launch of an installed application exactly as a
// launch does and puts it beside the anchor the ledger holds. The refusal it
// reports is the one the gate would give, produced by the same function, so
// that a report cannot drift into describing a gate that no longer exists.
func (c *Cpak) ExplainLaunch(origin string) (LaunchExplanation, error) {
	app, err := c.getStoredApplication(origin, "", "", "", "")
	if err != nil {
		return LaunchExplanation{}, err
	}
	components, addons, err := c.launchComposition(app)
	if err != nil {
		return LaunchExplanation{}, err
	}
	identity, err := c.verifyLaunch(app, resolvedOverride(app), components, addons)
	if err != nil {
		return LaunchExplanation{}, err
	}
	level := launchEnforcement()
	explanation := LaunchExplanation{
		Origin:      app.Origin,
		Version:     app.Version,
		Enforcement: level,
		Identity:    identity,
		Refusal:     launchRefusal(level, app.Origin, identity),
	}
	explanation.Anchor, explanation.Enrolled, explanation.AnchorReason = launchAnchors.Load(identity.UID, app.Origin)
	return explanation, nil
}
