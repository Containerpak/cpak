/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// This is the half of publisher signing that touches a registry. What a
// signature means is decided in pkg/signature, which checks a bundle offline
// against the trust root that ships with cpak; what is checked is decided
// here, and it is the one thing that can go wrong without anything looking
// wrong.
//
// The rule the rest of this file exists to enforce: a state is named by what
// was RESOLVED and never by what a manifest ASKED FOR. A manifest that
// declares image_ref source names a tag, a tag is whatever it was last pointed
// at, and a signature over a tag says nothing. The signature is the pin: the
// digest a tag resolved to is inside the signed payload, and a tag that was
// repointed becomes a mismatch instead of a silent substitution. Every
// function below therefore takes the resolved digest as a parameter and
// refuses a value that is not one, so that handing it a reference the manifest
// asked for fails loudly rather than verifying the wrong thing.
//
// The second rule: not signed, does not verify and signed by somebody else are
// three different facts. They are three errors here because a caller has to be
// able to allow the first while refusing the other two, and collapsing them
// would make an unsigned package indistinguishable from a forged one.

// packageSignatureArtifactType is how cpak recognises its own referrers among
// everything else that can hang off an image, and is the same string
// cpak-sign attaches one under. The payload inside is a sigstore bundle, which
// carries the certificate and the inclusion proof and is what lets the check
// happen with no network beyond this fetch.
const packageSignatureArtifactType = "application/vnd.cpak.signature.v1+json"

// maxSignatureBundle bounds one bundle. A certificate, a signature and an
// inclusion proof are nowhere near this, and the limit is what stops a
// registry from deciding how much a verifier reads.
const maxSignatureBundle = 1 << 20

var (
	// ErrPackageUnsigned means the registry holds nothing against the image
	// that was resolved. It is not a failed check, it is the absence of one,
	// and a caller that refuses it is refusing unsigned software on purpose.
	ErrPackageUnsigned = errors.New("no publisher signature is attached to the package")

	// ErrSignatureUnverified means something is attached and does not stand
	// for the state that was resolved: a bundle that does not hold, or one
	// that holds for a different manifest, image or lock than the one here.
	ErrSignatureUnverified = errors.New("the publisher signature does not verify")

	// ErrSignatureForeign means a signature holds and was made by an identity
	// that cannot speak for this origin. It is the one failure that says the
	// artifact is authentic and still not the publisher's.
	ErrSignatureForeign = errors.New("the publisher signature was made by another identity")
)

// verifySignature is the offline check a fetched bundle is put through. It is
// a variable so that a test can drive the answers a caller has to tell apart;
// nothing in cpak replaces it.
var verifySignature = signature.Verify

// PackageState names the part of an installation a publisher could determine
// before it ever reached this machine, which is exactly what a signature
// covers: where the package comes from, the manifest that carries its
// permissions, the image that was resolved, and the lock it was resolved
// through when there was one.
//
// The image digest is a parameter and is never read out of the manifest. The
// manifest holds what was asked for, which for image_ref source is a tag and
// for a pinned reference is a digest that the registry may still answer for
// with a different one; only the digest that came back describes what was
// installed. A value that is not a resolved digest is refused rather than
// hashed, so the mistake this whole file is about cannot be made quietly.
//
// The manifest must already have been through ValidateManifest, because that
// is what fills the defaults in and migrates the fields cpak no longer reads,
// and it is the manifest as it stands afterwards that the publisher hashed. A
// manifest with no version has plainly not been through it and is refused; one
// that was decoded and not validated cannot be told apart from one that was,
// so the requirement is on the caller and this only catches the obvious half.
//
// Generation is left at zero, and a state with a zero generation is one
// signature.Verify refuses before it opens a bundle. That is deliberate and it
// is a gap, not a design: the generation is the publisher's counter, it lives
// inside the signature, and nothing an installing machine holds can supply it.
// Guessing one would put a number cpak invented into the payload cpak then
// checks a signature against, which would verify nothing at all. The counter
// has to travel beside the bundle before a state named here can be verified.
func PackageState(origin string, manifest *types.CpakManifest, imageDigest string, lock *types.ManifestLock) (signature.State, error) {
	origin = normalizePackageOrigin(origin)
	if origin == "" {
		return signature.State{}, errors.New("name the state of a package: the origin is required")
	}
	if manifest == nil {
		return signature.State{}, fmt.Errorf("name the state of %s: the manifest is required", origin)
	}
	if manifest.ManifestVersion == "" {
		return signature.State{}, fmt.Errorf("name the state of %s: the manifest has not been validated", origin)
	}
	if !isResolvedImageDigest(imageDigest) {
		return signature.State{}, fmt.Errorf("name the state of %s: %q is not a resolved image digest", origin, imageDigest)
	}
	applied, err := manifestDigest(manifest)
	if err != nil {
		return signature.State{}, fmt.Errorf("name the state of %s: %w", origin, err)
	}
	state := signature.State{
		ABI:            signature.ABIVersion,
		Origin:         origin,
		ManifestSHA256: applied,
		ImageDigest:    imageDigest,
	}
	if lock == nil {
		return state, nil
	}
	state.LockSHA256, err = manifestLockDigest(lock)
	if err != nil {
		return signature.State{}, fmt.Errorf("name the state of %s: %w", origin, err)
	}
	return state, nil
}

// FetchPackageSignature returns the signature bundle a registry holds against
// one resolved image, and false when it holds none.
//
// An image nothing is attached to is not an error. Signing arrives into a
// world of packages that are not signed, and a fetch that failed on every one
// of them would decide a policy that belongs to whoever runs the machine.
func (c *Cpak) FetchPackageSignature(ref oci.Reference, imageDigest string) ([]byte, bool, error) {
	bundles, err := c.packageSignatures(ref, imageDigest, "")
	if err != nil || len(bundles) == 0 {
		return nil, false, err
	}
	return bundles[0], true, nil
}

// VerifyPackageState checks what a registry holds against the state that was
// resolved and confirms the identity that signed it may speak for the origin.
//
// The repository is taken from the installation the state describes, found by
// the digest and not by the origin: one origin can be installed several times
// from several branches, each resolved its own image, and answering about
// another one would verify a package nobody asked about. An installer that has
// not stored anything yet holds the reference itself and goes through
// verifyPackageState.
func (c *Cpak) VerifyPackageState(origin string, state signature.State) (signature.Verified, error) {
	ref, err := c.installedImageReference(origin, state.ImageDigest)
	if err != nil {
		return signature.Verified{}, err
	}
	return c.verifyPackageState(ref, origin, state)
}

func (c *Cpak) verifyPackageState(ref oci.Reference, origin string, state signature.State) (signature.Verified, error) {
	origin = normalizePackageOrigin(origin)
	if origin == "" {
		return signature.Verified{}, errors.New("verify a package signature: the origin is required")
	}
	if state.Origin != origin {
		return signature.Verified{}, fmt.Errorf("verify the signature of %s: the state names %q", origin, state.Origin)
	}
	if err := state.Validate(); err != nil {
		return signature.Verified{}, fmt.Errorf("verify the signature of %s: %w", origin, err)
	}
	bundles, err := c.packageSignatures(ref, state.ImageDigest, origin)
	if err != nil {
		return signature.Verified{}, err
	}
	if len(bundles) == 0 {
		return signature.Verified{}, fmt.Errorf("verify the signature of %s: %w", origin, ErrPackageUnsigned)
	}

	// Every bundle attached to the image is put to the same question, because
	// a publisher that re-signs a state leaves the old bundle beside the new
	// one and only verification can tell which is which. A foreign signature
	// outranks one that does not hold: it is the more specific fact, and it is
	// the one that says somebody who is not the publisher signed this image.
	var foreign string
	var unverified error
	for _, bundle := range bundles {
		verified, verifyErr := verifySignature(bundle, state)
		if verifyErr != nil {
			if unverified == nil {
				unverified = verifyErr
			}
			continue
		}
		if verified.Identity.MatchesOrigin(origin) {
			return verified, nil
		}
		if foreign == "" {
			foreign = verified.Identity.Repo
		}
	}
	if foreign != "" {
		return signature.Verified{}, fmt.Errorf("verify the signature of %s: %w: it was made for %q", origin, ErrSignatureForeign, foreign)
	}
	if unverified != nil {
		return signature.Verified{}, fmt.Errorf("verify the signature of %s: %w: %w", origin, ErrSignatureUnverified, unverified)
	}
	return signature.Verified{}, fmt.Errorf("verify the signature of %s: %w", origin, ErrSignatureUnverified)
}

// packageSignatures reads every bundle attached to the resolved image.
//
// More than one is not an error and neither is none. A referrer that cannot be
// read is an error, because a registry that answered with an artifact it then
// serves inconsistently is broken and not silent, and reporting that as an
// unsigned package would turn a fault into a downgrade.
//
// The origin is what registry credentials are filed under. It is empty for the
// exported entry point, which therefore reaches only what a registry serves
// anonymously.
func (c *Cpak) packageSignatures(ref oci.Reference, imageDigest, origin string) ([][]byte, error) {
	if !isResolvedImageDigest(imageDigest) {
		return nil, fmt.Errorf("read the signatures of %s: %q is not a resolved image digest", ref.ContextName(), imageDigest)
	}
	client := &oci.Client{}
	if origin != "" {
		client.Credentials = registryauth.Provider{Origin: origin, Path: c.Options.RegistryAuthPath}
	}
	referrers, err := client.Referrers(c.Ctx, ref, imageDigest, packageSignatureArtifactType)
	if err != nil {
		return nil, fmt.Errorf("list the signatures of %s@%s: %w", ref.ContextName(), imageDigest, err)
	}
	bundles := make([][]byte, 0, len(referrers))
	for _, referrer := range referrers {
		bundle, payloadErr := client.ReferrerPayload(c.Ctx, ref, referrer, maxSignatureBundle)
		if payloadErr != nil {
			return nil, fmt.Errorf("read the signature %s of %s: %w", referrer.Digest, ref.ContextName(), payloadErr)
		}
		bundles = append(bundles, bundle)
	}
	return bundles, nil
}

// installedImageReference finds the repository a resolved digest was installed
// from. The digest chooses the installation, so an origin installed more than
// once cannot answer with the wrong one.
func (c *Cpak) installedImageReference(origin, imageDigest string) (oci.Reference, error) {
	origin = normalizePackageOrigin(origin)
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return oci.Reference{}, fmt.Errorf("read the installations of %s: %w", origin, err)
	}
	defer store.Close()

	apps, err := store.GetApplicationsByOrigin(origin, "", "", "", "")
	if err != nil {
		return oci.Reference{}, fmt.Errorf("read the installations of %s: %w", origin, err)
	}
	for _, app := range apps {
		if app.ImageDigest != imageDigest {
			continue
		}
		ref, parseErr := oci.ParseReference(app.Image)
		if parseErr != nil {
			return oci.Reference{}, fmt.Errorf("read the image of %s: %w", origin, parseErr)
		}
		return ref, nil
	}
	return oci.Reference{}, fmt.Errorf("no installation of %s was resolved to %s", origin, imageDigest)
}

// manifestLockDigest names the lock an installation was resolved through, in
// the same shape the manifest beside it is named in: the hash of the JSON cpak
// encodes the value as, so that reformatting the file leaves a signature valid
// while changing what it pins does not.
func manifestLockDigest(lock *types.ManifestLock) (string, error) {
	encoded, err := json.Marshal(lock)
	if err != nil {
		return "", fmt.Errorf("encode the manifest lock: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// normalizePackageOrigin puts an origin in the one form both sides compare:
// the publisher normalises it before signing it, and a state that names it any
// other way is a state no installation of the same package can reproduce.
func normalizePackageOrigin(value string) string {
	origin := strings.ToLower(strings.TrimSpace(value))
	origin = strings.TrimPrefix(origin, "https://")
	origin = strings.TrimSuffix(strings.TrimSuffix(origin, "/"), ".git")
	return origin
}

// isResolvedImageDigest reports whether a value is a digest a registry
// answered with, as opposed to a tag or a reference a manifest declared. The
// shape is the one the registry client accepts, lower case included, so that a
// value this admits is never one the fetch then refuses.
func isResolvedImageDigest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
