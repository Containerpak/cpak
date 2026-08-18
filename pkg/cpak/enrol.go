/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// Enrolment is what writes the anchor a launch is later recognised by. It runs
// at the one moment cpak knows what an installation is: the instant an install
// or an update finished and its exports are on disk.
//
// What it records is the store as it stands at that instant, and, when the
// publisher signed the state that was installed, who signed it. Those are two
// different claims and they are kept apart everywhere. The anchor says the
// application has not changed since it was enrolled, which is worth nothing
// about what the publisher shipped. The signature says the package came from
// the CI of the repository it is published under, and nothing at all about
// whether the software is safe.
//
// An application nobody signed is still enrolled and the record says it was
// unsigned, unless the owner of the machine set the host policy to required,
// in which case it is not enrolled at all. That distinction is the whole point
// of recording it.
//
// The roots are derived through the same helpers the gate derives them with,
// and never through a second copy that looks the same, because an anchor the
// gate cannot reproduce would refuse the installation that wrote it.

// recordAnchor, forgetAnchor and recordedAnchor are the ledger as an install
// sees it. They are variables so that a test can hold a ledger of its own; the
// only thing cpak ever puts in them is the system authority.
//
// The policy and the signature travel with the anchor because an installer is
// the one caller that holds them. Without the policy the authority sees two
// policy roots it cannot order and has to put every change to the owner of the
// machine; without the bundle it would have to believe what the installer says
// about who published the software, which is exactly what a signature exists
// to stop.
var (
	recordAnchor    = systemauthority.EnrolAnchorWithSignature
	forgetAnchor    = systemauthority.ForgetAnchor
	recordedAnchor  = systemauthority.RecordedAnchor
	signaturePolicy = systemauthority.Signatures
)

// The three things a user can actually do. The first names the backfill and not
// the layer state, because a digest shown to somebody with no way to act on it
// is noise; the second names the setup and not the transport that failed; the
// third names the host policy that refused, because the software is installed
// and working and only this host's rule is stopping it from being enrolled.
const (
	backfillAdvice  = "Run cpak audit --backfill-bindings to record what the store now holds for those layers, then install or update the application again to enrol it."
	authorityAdvice = "Run cpak system setup to install the system authority, then install or update the application again to enrol it."
	unsignedAdvice  = "This host enrols only packages a publisher signed. Ask the publisher to sign its releases, or run cpak system set-signatures optional to take unsigned packages again."
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

	// EnrolmentUnsigned means this host enrols only signed packages and
	// nothing that may speak for this origin signed this one.
	EnrolmentUnsigned
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
	case EnrolmentUnsigned:
		return "not signed"
	}
	return "not attempted"
}

// PublishedPackage is what an install and an update hold about the package they
// have just put on disk and an installed record does not carry: the manifest as
// cpak applied it, and the lock it was resolved through when there was one.
//
// Without them no signed state can be named at all. The manifest hash is half
// of what a publisher signs, and it is the manifest as validated that was
// signed, which is a value nothing in the store keeps: the record holds the
// manifest under a different hash, taken for a different purpose. A caller
// that cannot supply this is not asked to guess.
type PublishedPackage struct {
	Manifest *types.CpakManifest
	Lock     *types.ManifestLock
}

// signedLock is the lock that belongs in a signed state, which is the lock the
// package being installed is the root of and never one it merely arrived with.
//
// A publisher signs its own manifest beside its own lock, and cpak-sign refuses
// a lock rooted at another manifest for exactly that reason. An installation
// resolved through a lock also installs the dependencies that lock pins, and
// naming the same lock in their states would describe something no publisher of
// theirs ever signed: a signature that stands would be reported as one that
// does not.
func signedLock(origin string, lock *types.ManifestLock) *types.ManifestLock {
	if lock == nil {
		return nil
	}
	if normalizePackageOrigin(lock.Root.Origin) != normalizePackageOrigin(origin) {
		return nil
	}
	return lock
}

// EnrolmentSignature is what was found out about who published an
// installation. Signed, unsigned and a signature that does not stand are three
// answers and none of them is folded into another.
type EnrolmentSignature struct {
	// Verified is set only when a bundle checked out against the trust root
	// cpak ships with and the identity in its certificate may speak for the
	// origin the package is installed from.
	Verified bool
	Identity signature.Identity
	State    signature.State

	// Reason is why there is no verified signature, and is set for every case
	// other than a verified one. It wraps ErrPackageUnsigned when the registry
	// simply holds nothing.
	Reason error
}

// Unsigned reports whether the registry holds no signature at all, as opposed
// to holding one that does not stand.
func (s EnrolmentSignature) Unsigned() bool {
	return !s.Verified && errors.Is(s.Reason, ErrPackageUnsigned)
}

// ApplicationEnrolment is what enrolling one application ended in. It states,
// it does not decide: an install reads it and answers for it.
type ApplicationEnrolment struct {
	Origin    string
	UID       uint32
	Outcome   EnrolmentOutcome
	Anchor    integrity.Anchor
	Signature EnrolmentSignature

	// Reason carries what stopped the enrolment, and is set for every outcome
	// other than a recorded or an unchanged anchor.
	Reason error

	// Advice is the one thing the user can do about it, in the words they
	// would type. It is empty when there is nothing honest to suggest.
	Advice string
}

// EnrolApplication records what a launch of an installed application is, for a
// caller that holds nothing but the installed record.
//
// It cannot name a signed state, because the manifest a publisher signed is not
// something the store keeps, so it carries forward the signature the ledger
// already holds for this application when that signature is still about this
// image. Dropping it instead would turn a signed application into an unsigned
// one the first time somebody changed an override or enabled an addon, and on a
// host that takes only signed packages that would unenrol working software.
func (c *Cpak) EnrolApplication(app types.Application) ApplicationEnrolment {
	return c.enrolApplication(app, PublishedPackage{}, c.carriedSignature(app))
}

// EnrolPublishedApplication is what an install and an update call. It is the
// one moment cpak holds everything a signed state is made of, so it is the one
// moment the signature can be checked at all.
//
// It never fails an install. An application that could not be enrolled is one
// the gate finds unenrolled, which is a state the gate already answers for and
// an administrator already decides about; failing the install instead would
// put that decision in a second place, the one place policy cannot reach, and
// would turn an unreachable helper into a failed installation of software that
// is already on disk and working. What it must not be is quiet, so every
// outcome other than a recorded anchor is reported to the user as it happens.
func (c *Cpak) EnrolPublishedApplication(app types.Application, published PublishedPackage) ApplicationEnrolment {
	return c.enrolApplication(app, published, nil)
}

func (c *Cpak) enrolApplication(app types.Application, published PublishedPackage, carried *systemauthority.SignedState) ApplicationEnrolment {
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
		ImageDigest: app.ImageDigest,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  integrity.LaunchRoot(packageRoot, policyRoot),
	}
	// The manifest is hashed here rather than read out of the signed state, so
	// that the authority compares two values derived independently instead of
	// comparing one with itself.
	if published.Manifest != nil {
		configured, digestErr := manifestDigest(published.Manifest)
		if digestErr != nil {
			return undescribedEnrolment(enrolment, fmt.Errorf("hash the manifest of %s: %w", app.Origin, digestErr), "")
		}
		anchor.ManifestDigest = configured
	}
	if err := anchor.ValidateDigests(); err != nil {
		return undescribedEnrolment(enrolment, err, "")
	}
	if held {
		if recorded.LaunchRoot == anchor.LaunchRoot {
			enrolment.Outcome = EnrolmentUnchanged
			enrolment.Anchor = recorded
			enrolment.Signature = c.heldSignature(app.Origin, enrolment.UID)
			return enrolment
		}
		anchor.Generation = recorded.Generation + 1
	}
	enrolment.Anchor = anchor

	// The signature is resolved after the launch is described and before the
	// authority is asked. An application nothing can describe is not enrolled
	// whatever a publisher said about it, and asking a registry about it first
	// would be a network round trip spent on an answer nobody can use.
	signed, found := c.packageSignature(app, published, carried)
	enrolment.Signature = found
	if signed == nil && signaturePolicy() == systemauthority.SignaturesRequired {
		return unsignedEnrolment(enrolment, fmt.Errorf("%w: %w", systemauthority.ErrSignatureRequired, found.Reason))
	}
	reportSignature(app.Origin, enrolment.UID, enrolment.Signature)

	if err = recordAnchor(anchor, &policy, signed); err != nil {
		if errors.Is(err, systemauthority.ErrSignatureRequired) {
			return unsignedEnrolment(enrolment, err)
		}
		return unrecordableEnrolment(enrolment, err)
	}
	enrolment.Outcome = EnrolmentRecorded
	if isVerbose {
		logger.Printf("%s is enrolled for verified launch at generation %d", app.Origin, anchor.Generation)
	}
	return enrolment
}

// packageSignature answers with the signature this enrolment is recorded
// under, or with why there is none.
//
// A caller that holds the published package asks the registry. A caller that
// does not carries forward what the ledger already proved, so that re-enrolling
// an application nobody changed is not a downgrade.
func (c *Cpak) packageSignature(app types.Application, published PublishedPackage, carried *systemauthority.SignedState) (*systemauthority.SignedState, EnrolmentSignature) {
	if published.Manifest == nil {
		if carried == nil {
			return nil, EnrolmentSignature{Reason: fmt.Errorf("name the signed state of %s: %w", app.Origin, ErrPackageUnsigned)}
		}
		return carried, describeSignature(app.Origin, carried)
	}
	signed, err := c.verifiedPackageSignature(app, published)
	if err != nil {
		return nil, EnrolmentSignature{Reason: err}
	}
	return signed, describeSignature(app.Origin, signed)
}

// carriedSignature is the signature the ledger holds for this application, kept
// only while it is still about the image this installation resolved to. What a
// publisher signed is the manifest and the image, and neither an override nor
// an addon changes either of those; a different image is a different package
// and its signature is not this one's.
func (c *Cpak) carriedSignature(app types.Application) *systemauthority.SignedState {
	recorded, held, err := recordedAnchor(uint32(os.Getuid()), app.Origin)
	if err != nil || !held || recorded.Signature == nil {
		return nil
	}
	if recorded.Signature.State.ImageDigest != app.ImageDigest {
		return nil
	}
	// It is proven again before it is offered. A signature that no longer
	// stands would be refused by the authority, and refusing the whole
	// enrolment over it would take an application down because a trust root
	// moved on: it is dropped here instead, and the application goes back to
	// being what it now provably is, which is unsigned.
	if !describeSignature(app.Origin, recorded.Signature).Verified {
		return nil
	}
	return recorded.Signature
}

// heldSignature reports what the ledger holds for an application that was
// already enrolled, proven again here rather than assumed from the fact that
// the authority once took it.
func (c *Cpak) heldSignature(origin string, uid uint32) EnrolmentSignature {
	recorded, held, err := recordedAnchor(uid, origin)
	if err != nil {
		return EnrolmentSignature{Reason: err}
	}
	if !held || recorded.Signature == nil {
		return EnrolmentSignature{Reason: ErrPackageUnsigned}
	}
	return describeSignature(origin, recorded.Signature)
}

// verifiedPackageSignature asks the registry what is attached to the image this
// installation resolved to, and verifies it offline against the state cpak
// itself named from the manifest it applied.
//
// The generation comes from the referrer that carries the bundle, because it is
// the publisher's counter and nothing on this machine holds it. Reading it from
// an annotation is safe for exactly one reason: it goes straight into the state
// the signature then has to cover, so a wrong value produces a state no bundle
// covers, which is a refusal and never an acceptance.
func (c *Cpak) verifiedPackageSignature(app types.Application, published PublishedPackage) (*systemauthority.SignedState, error) {
	state, err := PackageState(app.Origin, published.Manifest, app.ImageDigest, published.Lock)
	if err != nil {
		return nil, err
	}
	ref, err := c.installedImageReference(app.Origin, app.ImageDigest)
	if err != nil {
		return nil, err
	}
	attached, err := c.attachedSignatures(ref, app.Origin, app.ImageDigest)
	if err != nil {
		return nil, err
	}
	if len(attached) == 0 {
		return nil, fmt.Errorf("verify the signature of %s: %w", app.Origin, ErrPackageUnsigned)
	}
	var foreign string
	var madeByAnother bool
	var refusal error
	for _, candidate := range attached {
		state.Generation = candidate.generation
		verified, verifyErr := verifySignature(candidate.bundle, state)
		if verifyErr != nil {
			if refusal == nil {
				refusal = verifyErr
			}
			continue
		}
		if verified.Identity.MatchesOrigin(app.Origin) {
			return &systemauthority.SignedState{State: state, Bundle: candidate.bundle}, nil
		}
		if !madeByAnother {
			madeByAnother = true
			foreign = verified.Identity.Repo
		}
	}
	// A signature that holds and was made by somebody else outranks one that
	// does not hold: it is the more specific fact, and it is the one that says
	// somebody who is not the publisher signed this image.
	if madeByAnother {
		return nil, fmt.Errorf("verify the signature of %s: %w: %s", app.Origin, ErrSignatureForeign, whoseSignature(foreign))
	}
	if refusal == nil {
		return nil, fmt.Errorf("verify the signature of %s: %w", app.Origin, ErrSignatureUnverified)
	}
	return nil, fmt.Errorf("verify the signature of %s: %w: %w", app.Origin, ErrSignatureUnverified, refusal)
}

// whoseSignature names the identity a foreign signature was made by. A
// certificate that names no repository at all is a different sentence from one
// that names the wrong repository, and a report that printed an empty string
// for the first would read as though nobody had looked.
func whoseSignature(repo string) string {
	if repo == "" {
		return "its certificate names no repository"
	}
	return fmt.Sprintf("it was made for %q", repo)
}

// attachedSignature is one bundle a registry holds against an image, with the
// generation the referrer that carries it declares.
type attachedSignature struct {
	generation uint64
	bundle     []byte
}

// attachedSignatures reads them all. The listing is done here and not through
// the entry point that verifies a complete state, because that state cannot be
// completed until the generation has been read off these descriptors.
func (c *Cpak) attachedSignatures(ref oci.Reference, origin, imageDigest string) ([]attachedSignature, error) {
	client := &oci.Client{Credentials: registryauth.Provider{Origin: origin, Path: c.Options.RegistryAuthPath}}
	referrers, err := client.Referrers(c.Ctx, ref, imageDigest, packageSignatureArtifactType)
	if err != nil {
		return nil, fmt.Errorf("list the signatures of %s@%s: %w", ref.ContextName(), imageDigest, err)
	}
	attached := make([]attachedSignature, 0, len(referrers))
	for _, referrer := range referrers {
		generation, named := signedGeneration(referrer)
		if !named {
			continue
		}
		bundle, payloadErr := client.ReferrerPayload(c.Ctx, ref, referrer, maxSignatureBundle)
		if payloadErr != nil {
			return nil, fmt.Errorf("read the signature %s of %s: %w", referrer.Digest, ref.ContextName(), payloadErr)
		}
		attached = append(attached, attachedSignature{generation: generation, bundle: bundle})
	}
	// The newest state a publisher attached is the one an installation should
	// be recorded under, so a re-signed package does not keep answering with
	// the bundle it replaced.
	sort.SliceStable(attached, func(first, second int) bool {
		return attached[first].generation > attached[second].generation
	})
	return attached, nil
}

// signedGeneration reads the publisher's counter off a referrer. A referrer
// that names none is skipped rather than guessed at: generation zero is not a
// state, and any other number cpak chose would be a number cpak invented and
// then checked a signature against.
func signedGeneration(referrer oci.Descriptor) (uint64, bool) {
	value, named := referrer.Annotations[signedGenerationAnnotation]
	if !named {
		return 0, false
	}
	generation, err := strconv.ParseUint(value, 10, 64)
	if err != nil || generation == 0 {
		return 0, false
	}
	return generation, true
}

// signedGenerationAnnotation is where cpak-sign writes the one field of a
// signed state that cannot be derived from the package: the publisher's own
// counter. A registry copies a referrer manifest's annotations onto the
// descriptor in its referrers index, so it is read from a listing that has
// already happened.
const signedGenerationAnnotation = "dev.cpak.signature.generation"

// describeSignature proves a signature and says what it found, through the
// same offline check the rest of cpak puts a bundle through. It is never
// believed from a record: a record is trustworthy because root wrote it, which
// says nothing about whether the bundle inside it still stands, and a trust
// root that moved on or a file somebody replaced is exactly what a report is
// for.
//
// The authority proves it again for itself before it records anything, and
// that second check is the one that decides. This one only decides what to
// report.
func describeSignature(origin string, signed *systemauthority.SignedState) EnrolmentSignature {
	if signed == nil {
		return EnrolmentSignature{Reason: ErrPackageUnsigned}
	}
	verified, err := verifySignature(signed.Bundle, signed.State)
	if err != nil {
		return EnrolmentSignature{Reason: fmt.Errorf("%w: %w", ErrSignatureUnverified, err)}
	}
	if !verified.Identity.MatchesOrigin(origin) {
		return EnrolmentSignature{Reason: fmt.Errorf("%w: %s", ErrSignatureForeign, whoseSignature(verified.Identity.Repo))}
	}
	return EnrolmentSignature{Verified: true, Identity: verified.Identity, State: verified.State}
}

// reportSignature says what was found out about the publisher of an
// installation that is about to be enrolled. A package nobody signed is a line
// only somebody who asked for detail wants; a signature that is attached and
// does not stand is not, because something is claiming to be the publisher and
// is not.
//
// An origin that was signed and now arrives unsigned is the third case and it
// is never quiet. Nothing failed and nothing is refused, which is what makes it
// worth saying: the publisher stopped signing, or somebody put a package that
// nobody signed where a signed one used to be, and on a host that has not been
// set to require signatures this line is the only place either shows up.
func reportSignature(origin string, uid uint32, found EnrolmentSignature) {
	switch {
	case found.Verified:
		if isVerbose {
			logger.Printf("%s is signed by %s", origin, found.Identity.Repo)
		}
	case found.Unsigned():
		if signedBefore(uid, origin) {
			logger.Warnf("%s was signed when it was last enrolled and this one carries no publisher signature at all. It is enrolled as unsigned.", origin)
			return
		}
		if isVerbose {
			logger.Printf("%s is not signed by anybody, and is enrolled as unsigned", origin)
		}
	case errors.Is(found.Reason, ErrSignatureUnverified), errors.Is(found.Reason, ErrSignatureForeign):
		logger.Warnf("A publisher signature is attached to %s and cpak does not accept it: %v. It is enrolled as unsigned.", origin, found.Reason)
	default:
		// Everything left is cpak failing to find out rather than a package
		// failing a check, which is now reachable on every install: a registry
		// that would not answer must not be reported as a package claiming to
		// be signed by somebody it is not.
		logger.Warnf("Who published %s could not be found out: %v. It is enrolled as unsigned.", origin, found.Reason)
	}
}

// signedBefore reports whether the ledger holds a signature for this origin,
// asked before the anchor that is about to replace it is written.
//
// The record is what remembers it, so the answer is only ever about the last
// enrolment: once an origin has been recorded as unsigned it is unsigned, and
// the downgrade is said once, at the moment it happens, rather than at every
// enrolment forever after. A ledger that cannot be read says nothing, because a
// warning about a downgrade nobody can show happened is a warning that trains
// the reader to ignore the next one.
func signedBefore(uid uint32, origin string) bool {
	recorded, held, err := recordedAnchor(uid, origin)
	if err != nil || !held {
		return false
	}
	return recorded.Signature != nil
}

// forgetEnrolment drops the anchor of an application that has just been
// removed. An anchor is filed under the origin alone, so an origin that still
// has one installation left is enrolled again from the one that remains, and
// an origin that has several is left with none: nothing in a single anchor can
// name which of them it is about, and picking one would enrol an application
// the user did not ask about.
func (c *Cpak) forgetEnrolment(app types.Application) {
	uid := uint32(os.Getuid())
	// What the ledger holds is read before it is dropped, because the
	// installation that remains is enrolled again from a record that no longer
	// exists by then, and it would otherwise be recorded as unsigned.
	recorded, held, recordErr := recordedAnchor(uid, app.Origin)
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
		var carried *systemauthority.SignedState
		if recordErr == nil && held && recorded.Signature != nil && recorded.Signature.State.ImageDigest == remaining[0].ImageDigest {
			carried = recorded.Signature
		}
		c.enrolApplication(remaining[0], PublishedPackage{}, carried)
	default:
		logger.Warnf("%d installations of %s remain and the ledger holds one anchor per origin, so none of them is enrolled for verified launch.", len(remaining), app.Origin)
	}
}

// RecordedSignature is what the ledger holds about who published one installed
// application, put back through the same offline check the authority made
// before it recorded it.
type RecordedSignature struct {
	Origin   string
	Enrolled bool
	EnrolmentSignature
}

// RecordedSignatures answers for every installed application. It writes
// nothing and it reaches no network: a bundle carries its own certificate and
// its own proofs, which is what lets an administrator read this on a machine
// that has no internet.
func (c *Cpak) RecordedSignatures() ([]RecordedSignature, error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	signatures := make([]RecordedSignature, 0, len(apps))
	for _, app := range apps {
		if seen[app.Origin] {
			continue
		}
		seen[app.Origin] = true
		signatures = append(signatures, c.RecordedSignatureOf(app.Origin))
	}
	return signatures, nil
}

// RecordedSignatureOf answers for one application, and answers with the reason
// rather than an error when the ledger will not say: a report that gave up on
// the first application it could not read would say nothing about the rest.
func (c *Cpak) RecordedSignatureOf(origin string) RecordedSignature {
	found := RecordedSignature{Origin: origin}
	recorded, held, err := recordedAnchor(uint32(os.Getuid()), origin)
	if err != nil {
		found.Reason = err
		return found
	}
	if !held {
		found.Reason = ErrPackageUnsigned
		return found
	}
	found.Enrolled = true
	if recorded.Signature == nil {
		found.Reason = ErrPackageUnsigned
		return found
	}
	found.EnrolmentSignature = describeSignature(origin, recorded.Signature)
	return found
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

// unsignedEnrolment is the host policy answered here as well as by the
// authority. The authority refuses it too, and that is the refusal that
// counts; this one exists so that the user is told what is wrong with their
// package instead of being told that a record could not be written.
func unsignedEnrolment(enrolment ApplicationEnrolment, reason error) ApplicationEnrolment {
	enrolment.Outcome = EnrolmentUnsigned
	enrolment.Reason = reason
	enrolment.Advice = unsignedAdvice
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
