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

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// refuseUnenrolledLaunch is the single switch that decides what happens to an
// application no anchor was ever recorded for. It is false because nothing
// writes an anchor yet: every installation that exists today is unenrolled, and
// refusing them would refuse every launch on every host. Turning it to true is
// the whole of the change once enrolment lands, and from that moment an
// application the ledger does not answer for does not start.
const refuseUnenrolledLaunch = false

// launchAnchors is the ledger a launch is checked against. It is a variable so
// that a test can point it at a ledger it owns; nothing in cpak replaces it.
var launchAnchors integrity.Anchors = systemauthority.DefaultAnchorLedger()

var (
	errLaunchUnenrolled   = errors.New("the application is not enrolled for verified launch")
	errLaunchUnbound      = errors.New("a layer of the application is not bound to a store state")
	errLaunchUnrecognised = errors.New("the application does not match the integrity anchor it was enrolled with")
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
}

// verifyLaunch derives what this launch is and compares it with the anchor
// recorded for the account asking for it. A ledger that cannot answer is a
// verdict and not a failure, because the caller needs the same decision it
// needs for an application nobody enrolled.
func (c *Cpak) verifyLaunch(app types.Application, override types.Override, components, addons []types.Application) (LaunchIdentity, error) {
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

	anchor, enrolled, err := launchAnchors.Load(identity.UID, app.Origin)
	if err != nil {
		identity.Verdict = LaunchUnverifiable
		identity.Reason = err
		return identity, nil
	}
	identity.Anchor = anchor

	// Being enrolled comes first: what the layers of an application are cannot
	// be a disagreement until something claims what they should be.
	switch {
	case !enrolled:
		identity.Verdict = LaunchUnenrolled
	case unbound:
		identity.Verdict = LaunchUnbound
	case identity.LaunchRoot == anchor.LaunchRoot:
		identity.Verdict = LaunchRecognised
	default:
		identity.Verdict = LaunchUnrecognised
	}
	return identity, nil
}

// gateLaunch is the last word before anything of an application is mounted. It
// turns the verdict into an answer, which is the one thing verifyLaunch
// deliberately does not do.
func (c *Cpak) gateLaunch(app types.Application, override types.Override, components, addons []types.Application) (LaunchIdentity, error) {
	identity, err := c.verifyLaunch(app, override, components, addons)
	if err != nil {
		return identity, err
	}
	switch identity.Verdict {
	case LaunchRecognised:
		return identity, nil
	case LaunchUnenrolled, LaunchUnverifiable:
		if refuseUnenrolledLaunch {
			return identity, fmt.Errorf("%w: %s is %s", errLaunchUnenrolled, app.Origin, identity.Verdict)
		}
		if identity.Reason != nil {
			logger.Printf("Warning: the integrity anchor of %s could not be read: %v", app.Origin, identity.Reason)
			return identity, nil
		}
		// Nothing enrols yet, so this is every application there is: saying it
		// at every launch would only teach the warning away.
		if isVerbose {
			logger.Printf("The launch of %s is %s", app.Origin, identity.Verdict)
		}
		return identity, nil
	case LaunchUnbound:
		return identity, fmt.Errorf("%w: %s", errLaunchUnbound, app.Origin)
	case LaunchUnrecognised:
		return identity, fmt.Errorf("%w: %s", errLaunchUnrecognised, app.Origin)
	default:
		return identity, fmt.Errorf("the launch of %s could not be verified", app.Origin)
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
