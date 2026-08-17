/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// Enrolment is what writes the anchor a launch is later recognised by. It runs
// at the one moment cpak knows what an installation is: the instant an install
// or an update finished and its exports are on disk.
//
// What it records is the store as it stands at that instant. That is trust on
// first install: honest on a machine whose owner is trusted, and not
// authenticity. Nothing here checks a publisher signature, so an application
// that was already altered before it was enrolled is enrolled as altered and
// every launch afterwards recognises the alteration. Nothing that calls this
// may describe it as verification of what a publisher shipped.
//
// The roots are derived through the same helpers the gate derives them with,
// and never through a second copy that looks the same, because an anchor the
// gate cannot reproduce would refuse the installation that wrote it.

// recordAnchor and forgetAnchor are the privileged half of an enrolment. They
// are variables so that a test can hold a ledger of its own; the only thing
// cpak ever puts in them is the system authority.
//
// The policy travels with the anchor because an installer is the one caller
// that holds it. Without it the authority sees two policy roots it cannot
// order and has to put every change to the owner of the machine, including the
// ones that only narrow what the application may do.
var (
	recordAnchor = systemauthority.EnrolAnchorWithPolicy
	forgetAnchor = systemauthority.ForgetAnchor
)

// The two things a user can actually do. The first names the backfill and not
// the layer state, because a digest shown to somebody with no way to act on it
// is noise; the second names the setup and not the transport that failed.
const (
	backfillAdvice  = "Run cpak audit --backfill-bindings to record what the store now holds for those layers, then install or update the application again to enrol it."
	authorityAdvice = "Run cpak system setup to install the system authority, then install or update the application again to enrol it."
)

// EnrolmentOutcome is what one attempt to enrol an application concluded. The
// zero value is deliberately not a conclusion, so an outcome nobody filled in
// is never read as a recorded anchor.
type EnrolmentOutcome uint8

const (
	// EnrolmentRecorded means the ledger now answers for this application.
	EnrolmentRecorded EnrolmentOutcome = iota + 1

	// EnrolmentUnchanged means the ledger already held this exact launch, so
	// nothing was asked of the authority and no generation was spent.
	EnrolmentUnchanged

	// EnrolmentUndescribed means the launch could not be derived at all, which
	// is almost always a layer no binding names.
	EnrolmentUndescribed

	// EnrolmentUnrecordable means the launch was derived and the authority did
	// not take it. The installation is complete and unverifiable.
	EnrolmentUnrecordable
)

func (o EnrolmentOutcome) String() string {
	switch o {
	case EnrolmentRecorded:
		return "enrolled"
	case EnrolmentUnchanged:
		return "already enrolled"
	case EnrolmentUndescribed:
		return "not described"
	case EnrolmentUnrecordable:
		return "not recorded"
	}
	return "not attempted"
}

// ApplicationEnrolment is what enrolling one application ended in. It states,
// it does not decide: an install reads it and answers for it.
type ApplicationEnrolment struct {
	Origin  string
	UID     uint32
	Outcome EnrolmentOutcome
	Anchor  integrity.Anchor

	// Reason carries what stopped the enrolment, and is set for every outcome
	// other than a recorded or an unchanged anchor.
	Reason error

	// Advice is the one thing the user can do about it, in the words they
	// would type. It is empty when there is nothing honest to suggest.
	Advice string
}

// EnrolApplication records what a launch of an installed application is, so
// that the gate has something to recognise it by.
//
// It never fails an install. An application that could not be enrolled is one
// the gate finds unenrolled, which is a state the gate already answers for and
// an administrator already decides about; failing the install instead would
// put that decision in a second place, the one place policy cannot reach, and
// would turn an unreachable helper into a failed installation of software that
// is already on disk and working. What it must not be is quiet, so every
// outcome other than a recorded anchor is reported to the user as it happens.
func (c *Cpak) EnrolApplication(app types.Application) ApplicationEnrolment {
	enrolment := ApplicationEnrolment{Origin: app.Origin, UID: uint32(os.Getuid())}
	components, addons, err := c.launchComposition(app)
	if err != nil {
		return undescribedEnrolment(enrolment, err, "")
	}
	packageRoot, err := c.launchPackageRoot(app, components, addons)
	if err != nil {
		// Every way this fails is a layer the bindings ledger cannot answer
		// for, so the user is pointed at the ledger and not at the hash.
		return undescribedEnrolment(enrolment, err, backfillAdvice)
	}
	policy := resolvedOverride(app)
	policyRoot, err := integrity.PolicyRoot(policy)
	if err != nil {
		return undescribedEnrolment(enrolment, fmt.Errorf("derive the policy root of %s: %w", app.Origin, err), "")
	}

	recorded, held, err := launchAnchors.Load(enrolment.UID, app.Origin)
	if err != nil {
		// A ledger that cannot be read cannot say which generation this
		// enrolment follows, and an anchor written under a guessed one is
		// either refused as a downgrade or accepted as one.
		return unrecordableEnrolment(enrolment, err)
	}
	anchor := integrity.Anchor{
		ABI:         integrity.ABIVersion,
		UID:         enrolment.UID,
		Origin:      app.Origin,
		Generation:  1,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  integrity.LaunchRoot(packageRoot, policyRoot),
	}
	if held {
		if recorded.LaunchRoot == anchor.LaunchRoot {
			enrolment.Outcome = EnrolmentUnchanged
			enrolment.Anchor = recorded
			return enrolment
		}
		anchor.Generation = recorded.Generation + 1
	}
	enrolment.Anchor = anchor
	if err = recordAnchor(anchor, &policy); err != nil {
		return unrecordableEnrolment(enrolment, err)
	}
	enrolment.Outcome = EnrolmentRecorded
	if isVerbose {
		logger.Printf("%s is enrolled for verified launch at generation %d", app.Origin, anchor.Generation)
	}
	return enrolment
}

// forgetEnrolment drops the anchor of an application that has just been
// removed. An anchor is filed under the origin alone, so an origin that still
// has one installation left is enrolled again from the one that remains, and
// an origin that has several is left with none: nothing in a single anchor can
// name which of them it is about, and picking one would enrol an application
// the user did not ask about.
func (c *Cpak) forgetEnrolment(app types.Application) {
	uid := uint32(os.Getuid())
	if err := forgetAnchor(uid, app.Origin); err != nil {
		logger.Warnf("The integrity anchor of %s was not removed: %v. Until it is, the ledger answers for an application that is not installed.", app.Origin, err)
		return
	}
	remaining, err := c.installationsOf(app.Origin)
	if err != nil {
		logger.Warnf("The remaining installations of %s could not be read, so none of them was enrolled again: %v", app.Origin, err)
		return
	}
	switch len(remaining) {
	case 0:
		return
	case 1:
		c.EnrolApplication(remaining[0])
	default:
		logger.Warnf("%d installations of %s remain and the ledger holds one anchor per origin, so none of them is enrolled for verified launch.", len(remaining), app.Origin)
	}
}

// launchComposition resolves the applications a launch of this one composes,
// the way the gate resolves them: the layer dependencies stacked under it and
// the addons its owner enabled.
func (c *Cpak) launchComposition(app types.Application) (components, addons []types.Application, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()

	components, err = c.resolveLayerDependenciesFromStore(app, store)
	if err != nil {
		return nil, nil, err
	}
	addons, err = c.resolveEnabledAddonsFromStore(app, store)
	if err != nil {
		return nil, nil, err
	}
	return components, addons, nil
}

func (c *Cpak) installationsOf(origin string) ([]types.Application, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	return store.GetApplicationsByOrigin(origin, "", "", "", "")
}

func undescribedEnrolment(enrolment ApplicationEnrolment, err error, advice string) ApplicationEnrolment {
	enrolment.Outcome = EnrolmentUndescribed
	enrolment.Reason = err
	enrolment.Advice = advice
	return reportEnrolment(enrolment)
}

func unrecordableEnrolment(enrolment ApplicationEnrolment, err error) ApplicationEnrolment {
	enrolment.Outcome = EnrolmentUnrecordable
	enrolment.Reason = err
	if errors.Is(err, systemauthority.ErrNoAuthority) {
		enrolment.Advice = authorityAdvice
	}
	return reportEnrolment(enrolment)
}

// report says the one thing the user has to know: the application is installed
// and its launches answer to nothing. It is never gated on verbosity, because
// an installation that cannot be verified is not a detail, and it is the whole
// of what an install pays for not being failed outright.
func reportEnrolment(enrolment ApplicationEnrolment) ApplicationEnrolment {
	message := fmt.Sprintf("%s is installed and not enrolled for verified launch: %v.", enrolment.Origin, enrolment.Reason)
	if enrolment.Advice != "" {
		message += " " + enrolment.Advice
	}
	logger.Warn(message)
	return enrolment
}
