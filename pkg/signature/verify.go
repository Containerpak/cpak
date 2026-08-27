/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package signature

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	sigstorebundle "github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// digestAlgorithm is the one hash a bundle may commit to a state with. It is
// the name sigstore gives SHA-256 in a bundle, and it is fixed because the
// canonical encoding is hashed exactly one way.
const digestAlgorithm = "SHA2_256"

var (
	// ErrUntrusted reports a bundle that did not verify against the trust root
	// cpak ships with. It covers every reason at once on purpose: a chain that
	// does not reach Fulcio, a certificate nobody logged, a signature no
	// transparency log holds and a signature that simply does not check are the
	// same answer to the caller.
	ErrUntrusted = errors.New("signature: bundle does not verify against the bundled trust root")

	// ErrStateMismatch reports a bundle whose signature is over something other
	// than the state it was checked against. It is kept apart from ErrUntrusted
	// because it is the ordinary outcome of a repointed tag or a rewritten
	// manifest, which is a publisher problem and not an attack on the format.
	ErrStateMismatch = errors.New("signature: bundle does not cover this state")

	// ErrNotKeyless reports a bundle that is not signed by a certificate.
	// Keyless is the whole design: a bare key carries no identity, so there
	// would be nothing in it to put next to an origin.
	ErrNotKeyless = errors.New("signature: bundle is not signed by a certificate")

	// ErrIdentityMismatch reports a valid signature made by an identity that
	// cannot publish the origin in the signed state.
	ErrIdentityMismatch = errors.New("signature: signer cannot publish this origin")
)

type IdentityMismatchError struct {
	Identity Identity
	Origin   string
}

func (e *IdentityMismatchError) Error() string {
	return fmt.Sprintf("%s: %q cannot publish %q", ErrIdentityMismatch, e.Identity.Repo, e.Origin)
}

func (e *IdentityMismatchError) Unwrap() error {
	return ErrIdentityMismatch
}

// Verified is a bundle that checked out: the state it covers, and the identity
// the certificate names.
type Verified struct {
	State    State    `json:"state"`
	Identity Identity `json:"identity"`
}

// VerifyPublisher checks a bundle offline against the bundled trust root,
// confirms it covers exactly the state given and refuses a signer that cannot
// publish the state's origin.
//
// Offline is a requirement and not an optimisation. A sigstore bundle already
// carries the certificate and the log proofs, so nothing here reaches the
// network: an outage at sigstore must not stop an installation, the bundle
// travels beside an image that is being downloaded anyway, and the same check
// has to work afterwards on a machine that has no internet at all.
func VerifyPublisher(bundleJSON []byte, state State) (Verified, error) {
	material, err := bundledTrustRoot()
	if err != nil {
		return Verified{}, err
	}
	return verifyPublisherWith(material, verificationOptions(), bundleJSON, state)
}

// Verify checks a publisher signature. It is kept as the compatible name for
// callers built against earlier releases.
func Verify(bundleJSON []byte, state State) (Verified, error) {
	return VerifyPublisher(bundleJSON, state)
}

func verifyPublisherWith(material root.TrustedMaterial, options []verify.VerifierOption, bundleJSON []byte, state State) (Verified, error) {
	verified, err := verifyWith(material, options, bundleJSON, state)
	if err != nil {
		return Verified{}, err
	}
	if !verified.Identity.MatchesOrigin(state.Origin) {
		return Verified{}, &IdentityMismatchError{Identity: verified.Identity, Origin: state.Origin}
	}
	return verified, nil
}

// VerifyApproval checks a bundle over a state and returns the identity for the
// host approval policy to decide. Approval identities are independent of the
// package origin by design.
func VerifyApproval(bundleJSON []byte, state State) (Verified, error) {
	material, err := bundledTrustRoot()
	if err != nil {
		return Verified{}, err
	}
	return verifyWith(material, verificationOptions(), bundleJSON, state)
}

// VerifyArtifact checks a keyless bundle over the exact artifact bytes and
// returns the repository identity from its verified certificate.
func VerifyArtifact(bundleJSON, artifact []byte) (Identity, error) {
	material, err := bundledTrustRoot()
	if err != nil {
		return Identity{}, err
	}
	return verifyArtifactWith(material, verificationOptions(), bundleJSON, artifact)
}

// verificationOptions is the posture a package signature is held to. Each one
// is a separate thing an attacker would have to hold: a certificate that chains
// to Fulcio, that certificate published in certificate transparency, and the
// signature itself in the transparency log with a timestamp that falls inside
// the certificate's few minutes of life.
func verificationOptions() []verify.VerifierOption {
	return []verify.VerifierOption{
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	}
}

// verifyWith is the whole check, with the trust root and the posture handed in
// so that a test can hold both. The exported entry points decide them.
func verifyWith(material root.TrustedMaterial, options []verify.VerifierOption, bundleJSON []byte, state State) (Verified, error) {
	// The state is hashed first, so a bundle is never opened on behalf of a
	// state that could not have meant anything in the first place.
	digest, err := state.digest()
	if err != nil {
		return Verified{}, err
	}
	identity, err := verifyDigest(material, options, bundleJSON, digest[:])
	if err != nil {
		return Verified{}, err
	}
	return Verified{State: state, Identity: identity}, nil
}

func verifyArtifactWith(material root.TrustedMaterial, options []verify.VerifierOption, bundleJSON, artifact []byte) (Identity, error) {
	digest := sha256.Sum256(artifact)
	return verifyDigest(material, options, bundleJSON, digest[:])
}

func verifyDigest(material root.TrustedMaterial, options []verify.VerifierOption, bundleJSON, digest []byte) (Identity, error) {
	signed := new(sigstorebundle.Bundle)
	if err := signed.UnmarshalJSON(bundleJSON); err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrUntrusted, err)
	}
	if err := coversState(signed, digest); err != nil {
		return Identity{}, err
	}
	verifier, err := verify.NewVerifier(material, options...)
	if err != nil {
		return Identity{}, fmt.Errorf("signature: build the verifier: %w", err)
	}
	// WithoutIdentitiesUnsafe is the honest option here and not a shortcut. The
	// identity is not a policy this function holds, it is a result it reports,
	// and handing sigstore an expected identity would move the decision out of
	// MatchesOrigin, where the origin lives, into a string built here.
	//
	// The digest is passed as well as compared above, because coversState only
	// reads what the bundle says about itself. This is the leg that proves the
	// signature was made over those bytes.
	result, err := verifier.Verify(signed, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digest),
		verify.WithoutIdentitiesUnsafe(),
	))
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrUntrusted, err)
	}
	if result.Signature == nil || result.Signature.Certificate == nil {
		return Identity{}, ErrNotKeyless
	}
	return identityOf(*result.Signature.Certificate), nil
}

// coversState reads what the bundle claims to be a signature over and puts it
// next to the state. It proves nothing on its own, and it exists so that the
// ordinary case of a bundle for a different state is reported as that instead
// of as an untrusted signature.
func coversState(signed *sigstorebundle.Bundle, digest []byte) error {
	content, err := signed.SignatureContent()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUntrusted, err)
	}
	message := content.MessageSignatureContent()
	if message == nil {
		return fmt.Errorf("%w: it is not a signature over a message", ErrStateMismatch)
	}
	if message.DigestAlgorithm() != digestAlgorithm {
		return fmt.Errorf("%w: it commits to a %s digest", ErrStateMismatch, message.DigestAlgorithm())
	}
	if !bytes.Equal(message.Digest(), digest) {
		return ErrStateMismatch
	}
	return nil
}
