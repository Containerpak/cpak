/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package signature

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	sigstorebundle "github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const testSubject = "release@acme.example"

// testVerificationOptions is the production posture minus the certificate
// transparency leg. The test certificate authority below cannot mint a signed
// certificate timestamp, so a test that kept that leg would only ever be able
// to prove failure. Everything else is held to exactly what Verify holds a real
// bundle to: a chain to the certificate authority, an entry in the transparency
// log, and a timestamp inside the certificate's life.
//
// TestProductionPostureIsStrictlyStronger pins the difference so that this
// cannot quietly become the posture cpak ships.
func testVerificationOptions() []verify.VerifierOption {
	return []verify.VerifierOption{
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	}
}

// signedBundle signs the canonical encoding of a state with a throwaway
// sigstore and serialises the result as a bundle, which is the shape Verify is
// handed in production.
func signedBundle(t *testing.T, sigstore *ca.VirtualSigstore, state State) []byte {
	t.Helper()
	canonical, err := state.Canonical()
	if err != nil {
		t.Fatalf("the state under test must be signable: %v", err)
	}
	return signedBytes(t, sigstore, canonical)
}

func signedBytes(t *testing.T, sigstore *ca.VirtualSigstore, artifact []byte) []byte {
	t.Helper()
	entity, err := sigstore.Sign(testSubject, githubActionsIssuer, artifact)
	if err != nil {
		t.Fatalf("the test sigstore could not sign: %v", err)
	}
	verification, err := entity.VerificationContent()
	if err != nil {
		t.Fatalf("the signed entity holds no certificate: %v", err)
	}
	content, err := entity.SignatureContent()
	if err != nil {
		t.Fatalf("the signed entity holds no signature: %v", err)
	}
	message := content.MessageSignatureContent()
	if message == nil {
		t.Fatalf("the test sigstore produced something other than a message signature")
	}
	timestamps, err := entity.Timestamps()
	if err != nil {
		t.Fatalf("the signed entity holds no timestamps: %v", err)
	}
	entries, err := entity.TlogEntries()
	if err != nil {
		t.Fatalf("the signed entity holds no transparency log entries: %v", err)
	}
	logged := make([]*protorekor.TransparencyLogEntry, 0, len(entries))
	for _, entry := range entries {
		logged = append(logged, loggedEntry(t, sigstore, entry))
	}
	stamped := make([]*protocommon.RFC3161SignedTimestamp, 0, len(timestamps))
	for _, timestamp := range timestamps {
		stamped = append(stamped, &protocommon.RFC3161SignedTimestamp{SignedTimestamp: timestamp})
	}
	mediaType, err := sigstorebundle.MediaTypeString("v0.3")
	if err != nil {
		t.Fatalf("the bundle media type could not be named: %v", err)
	}
	assembled := &protobundle.Bundle{
		MediaType: mediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_Certificate{
				Certificate: &protocommon.X509Certificate{RawBytes: verification.Certificate().Raw},
			},
			TlogEntries:               logged,
			TimestampVerificationData: &protobundle.TimestampVerificationData{Rfc3161Timestamps: stamped},
		},
		Content: &protobundle.Bundle_MessageSignature{
			MessageSignature: &protocommon.MessageSignature{
				MessageDigest: &protocommon.HashOutput{
					Algorithm: protocommon.HashAlgorithm_SHA2_256,
					Digest:    message.Digest(),
				},
				Signature: message.Signature(),
			},
		},
	}
	wrapped, err := sigstorebundle.NewBundle(assembled)
	if err != nil {
		t.Fatalf("the assembled bundle is not a bundle: %v", err)
	}
	encoded, err := wrapped.MarshalJSON()
	if err != nil {
		t.Fatalf("the bundle could not be serialised: %v", err)
	}
	return encoded
}

// loggedEntry puts back the two things the test sigstore leaves out of the
// protobuf form of a rekor v1 entry: the kind, and the signed entry timestamp
// that is the log's promise the entry was included. A bundle assembled without
// them does not even parse.
//
// The timestamp is re-signed by the same rekor key the trust root holds, so a
// mistake here shows up as the positive test failing rather than as a promise
// nobody checked.
func loggedEntry(t *testing.T, sigstore *ca.VirtualSigstore, entry *tlog.Entry) *protorekor.TransparencyLogEntry {
	t.Helper()
	logged := entry.TransparencyLogEntry()
	var body struct {
		Kind       string `json:"kind"`
		APIVersion string `json:"apiVersion"`
	}
	if err := json.Unmarshal(logged.CanonicalizedBody, &body); err != nil {
		t.Fatalf("the transparency log body is not a rekor entry: %v", err)
	}
	promise, err := sigstore.RekorSignPayload(tlog.RekorPayload{
		Body:           entry.Body(),
		IntegratedTime: logged.IntegratedTime,
		LogIndex:       entry.LogIndex(),
		LogID:          hex.EncodeToString([]byte(entry.LogKeyID())),
	})
	if err != nil {
		t.Fatalf("the test transparency log could not sign its own entry: %v", err)
	}
	logged.KindVersion = &protorekor.KindVersion{Kind: body.Kind, Version: body.APIVersion}
	logged.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: promise}
	logged.InclusionProof = inclusionProof(t, sigstore, logged.CanonicalizedBody)
	return logged
}

// inclusionProof is the merkle proof a bundle of this version is required to
// carry, taken from the same test log that signed the promise above.
func inclusionProof(t *testing.T, sigstore *ca.VirtualSigstore, body []byte) *protorekor.InclusionProof {
	t.Helper()
	proof, err := sigstore.GetInclusionProof(body)
	if err != nil {
		t.Fatalf("the test transparency log could not prove its own entry: %v", err)
	}
	rootHash, err := hex.DecodeString(*proof.RootHash)
	if err != nil {
		t.Fatalf("the inclusion proof root hash is not hex: %v", err)
	}
	hashes := make([][]byte, 0, len(proof.Hashes))
	for _, hash := range proof.Hashes {
		decoded, decodeErr := hex.DecodeString(hash)
		if decodeErr != nil {
			t.Fatalf("an inclusion proof hash is not hex: %v", decodeErr)
		}
		hashes = append(hashes, decoded)
	}
	return &protorekor.InclusionProof{
		LogIndex:   *proof.LogIndex,
		RootHash:   rootHash,
		TreeSize:   *proof.TreeSize,
		Hashes:     hashes,
		Checkpoint: &protorekor.Checkpoint{Envelope: *proof.Checkpoint},
	}
}

func newTestSigstore(t *testing.T) *ca.VirtualSigstore {
	t.Helper()
	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("a test sigstore is needed to sign anything at all: %v", err)
	}
	return sigstore
}

func TestVerifyAcceptsABundleOverTheStateItIsCheckedAgainst(t *testing.T) {
	sigstore := newTestSigstore(t)
	state := validState()

	verified, err := verifyWith(sigstore, testVerificationOptions(), signedBundle(t, sigstore, state), state)
	if err != nil {
		t.Fatalf("a bundle signed over this exact state by this exact sigstore must verify: %v", err)
	}
	if verified.State != state {
		t.Fatalf("the verified state must be the state that was checked, got %+v", verified.State)
	}
	if verified.Identity.Issuer != githubActionsIssuer {
		t.Fatalf("the issuer must be read out of the certificate, got %q", verified.Identity.Issuer)
	}
	if verified.Identity.Subject != testSubject {
		t.Fatalf("the subject must be read out of the certificate, got %q", verified.Identity.Subject)
	}
	// The test certificate authority cannot write the Fulcio source repository
	// extension, so this identity names no repository and must therefore be
	// allowed to speak for nothing. A pass here would mean an identity with no
	// repository in it had been let through.
	if verified.Identity.Repo != "" {
		t.Fatalf("the test certificate names no repository, so none may be reported: %q", verified.Identity.Repo)
	}
	if verified.Identity.MatchesOrigin(state.Origin) {
		t.Fatalf("an identity that names no repository was allowed to speak for %s", state.Origin)
	}
}

func TestVerifyArtifactCoversTheExactBytes(t *testing.T) {
	sigstore := newTestSigstore(t)
	artifact := []byte("signed checksums\n")
	bundle := signedBytes(t, sigstore, artifact)

	if _, err := verifyArtifactWith(sigstore, testVerificationOptions(), bundle, artifact); err != nil {
		t.Fatalf("the signed artifact must verify: %v", err)
	}
	if _, err := verifyArtifactWith(sigstore, testVerificationOptions(), bundle, append(artifact, 'x')); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("got %v, want an artifact mismatch", err)
	}
}

func TestVerifyRefusesABundleForAnotherState(t *testing.T) {
	sigstore := newTestSigstore(t)
	signed := validState()
	bundleJSON := signedBundle(t, sigstore, signed)

	others := map[string]func(*State){
		"a different image":      func(s *State) { s.ImageDigest = "sha256:" + strings.Repeat("d", 64) },
		"a different manifest":   func(s *State) { s.ManifestSHA256 = strings.Repeat("e", 64) },
		"a different lock":       func(s *State) { s.LockSHA256 = strings.Repeat("f", 64) },
		"a different origin":     func(s *State) { s.Origin = "github.com/attacker/cpak" },
		"a different generation": func(s *State) { s.Generation = signed.Generation + 1 },
		"no lock at all":         func(s *State) { s.LockSHA256 = "" },
	}
	for name, change := range others {
		other := signed
		change(&other)
		_, err := verifyWith(sigstore, testVerificationOptions(), bundleJSON, other)
		if err == nil {
			t.Fatalf("a bundle signed over another state verified against one with %s", name)
		}
		if !errors.Is(err, ErrStateMismatch) {
			t.Fatalf("a bundle for another state must be reported as a mismatch, %s gave: %v", name, err)
		}
	}
}

// A repointed tag is the case this whole design exists for: the manifest is
// unchanged, the origin is unchanged, and the image the registry now hands over
// is somebody else's.
func TestVerifyRefusesARepointedImage(t *testing.T) {
	sigstore := newTestSigstore(t)
	signed := validState()
	bundleJSON := signedBundle(t, sigstore, signed)

	repointed := signed
	repointed.ImageDigest = "sha256:" + strings.Repeat("9", 64)
	if _, err := verifyWith(sigstore, testVerificationOptions(), bundleJSON, repointed); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("a tag that now resolves to another digest must be a signature mismatch, got: %v", err)
	}
}

func TestVerifyRefusesATamperedBundle(t *testing.T) {
	sigstore := newTestSigstore(t)
	state := validState()
	original := signedBundle(t, sigstore, state)

	// The signature is base64 in the serialised bundle. Rotating one character
	// of it leaves a well formed bundle whose signature is not the one that was
	// made, which is the difference between parsing a bundle and verifying one.
	tampered := tamperWithField(t, original, "signature")
	if _, err := verifyWith(sigstore, testVerificationOptions(), tampered, state); err == nil {
		t.Fatalf("a bundle whose signature was altered must not verify")
	} else if !errors.Is(err, ErrUntrusted) && !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("a tampered bundle must be reported as untrusted or as a mismatch, got: %v", err)
	}

	// The digest is what binds the signature to the state, so a bundle that
	// claims another one must not be taken for a bundle over this state.
	tampered = tamperWithField(t, original, "digest")
	if _, err := verifyWith(sigstore, testVerificationOptions(), tampered, state); err == nil {
		t.Fatalf("a bundle whose message digest was altered must not verify")
	}

	// The same again on the certificate, which is where the identity lives.
	tampered = tamperWithField(t, original, "rawBytes")
	if _, err := verifyWith(sigstore, testVerificationOptions(), tampered, state); err == nil {
		t.Fatalf("a bundle whose certificate was altered must not verify")
	}

	// And proof that the untouched bundle is not simply unverifiable: the same
	// bytes, unaltered, do verify. Without this the two checks above would pass
	// against a bundle that never worked in the first place.
	if _, err := verifyWith(sigstore, testVerificationOptions(), original, state); err != nil {
		t.Fatalf("the untampered bundle must still verify, otherwise the tampering proved nothing: %v", err)
	}
}

func TestVerifyRefusesABundleFromAnotherSigstore(t *testing.T) {
	trusted := newTestSigstore(t)
	stranger := newTestSigstore(t)
	state := validState()

	bundleJSON := signedBundle(t, stranger, state)
	if _, err := verifyWith(trusted, testVerificationOptions(), bundleJSON, state); err == nil {
		t.Fatalf("a bundle from a certificate authority the trust root does not hold must not verify")
	} else if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("a bundle from an untrusted authority must be reported as untrusted, got: %v", err)
	}
	// The same bundle against the sigstore that made it, so that the refusal
	// above is known to be about trust and not about the bundle being broken.
	if _, err := verifyWith(stranger, testVerificationOptions(), bundleJSON, state); err != nil {
		t.Fatalf("the same bundle must verify against the authority that issued it: %v", err)
	}
}

// The exported entry point is the one that matters, because it is the one cpak
// calls and the only one that decides which trust root is consulted. A bundle
// from a sigstore nobody has heard of must not get through it, whatever it was
// signed over.
func TestVerifyConsultsTheBundledTrustRoot(t *testing.T) {
	sigstore := newTestSigstore(t)
	state := validState()

	if _, err := Verify(signedBundle(t, sigstore, state), state); err == nil {
		t.Fatalf("Verify accepted a bundle from a private certificate authority, so it is not checking the shipped trust root")
	} else if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("a bundle no shipped authority issued must be reported as untrusted, got: %v", err)
	}
}

// The posture Verify ships with is stronger than the one the tests above can
// exercise, and the difference is certificate transparency. This pins that the
// difference is real: the same bundle that passes the test posture is refused
// by the shipped one.
func TestProductionPostureIsStrictlyStronger(t *testing.T) {
	sigstore := newTestSigstore(t)
	state := validState()
	bundleJSON := signedBundle(t, sigstore, state)

	if _, err := verifyWith(sigstore, testVerificationOptions(), bundleJSON, state); err != nil {
		t.Fatalf("the test posture must accept this bundle, otherwise the comparison says nothing: %v", err)
	}
	if _, err := verifyWith(sigstore, verificationOptions(), bundleJSON, state); err == nil {
		t.Fatalf("the shipped posture must refuse a certificate that was never published in certificate transparency")
	}
}

func TestVerifyRefusesAStateThatCannotBeMeaningful(t *testing.T) {
	sigstore := newTestSigstore(t)
	state := validState()
	bundleJSON := signedBundle(t, sigstore, state)

	unusable := state
	unusable.Generation = 0
	if _, err := verifyWith(sigstore, testVerificationOptions(), bundleJSON, unusable); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("a state that cannot mean anything must be refused before a bundle is opened, got: %v", err)
	}
}

func TestVerifyRefusesInputThatIsNotABundle(t *testing.T) {
	sigstore := newTestSigstore(t)
	state := validState()

	unusable := map[string][]byte{
		"nothing at all":     nil,
		"an empty document":  []byte("{}"),
		"a truncated bundle": signedBundle(t, sigstore, state)[:32],
		"plain text":         []byte("this is not a bundle"),
	}
	for name, data := range unusable {
		if _, err := verifyWith(sigstore, testVerificationOptions(), data, state); err == nil {
			t.Fatalf("%s was accepted as a bundle", name)
		}
	}
}

// The signature has to be over the canonical encoding and nothing else. A
// bundle over some other bytes that happen to travel with the package is not a
// signature over the state.
func TestVerifyRefusesASignatureOverOtherBytes(t *testing.T) {
	sigstore := newTestSigstore(t)
	state := validState()

	bundleJSON := signedBytes(t, sigstore, []byte("cpak.signature.state.v1\nabi=1\n"))
	if _, err := verifyWith(sigstore, testVerificationOptions(), bundleJSON, state); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("a signature over bytes that are not the canonical state must be a mismatch, got: %v", err)
	}
}

func TestBundledTrustRootIsARealTrustRoot(t *testing.T) {
	material, err := bundledTrustRoot()
	if err != nil {
		t.Fatalf("the shipped trust root must parse, a build that ships a broken one can verify nothing: %v", err)
	}
	trusted, ok := material.(*root.TrustedRoot)
	if !ok {
		t.Fatalf("the shipped trust root must be a parsed trust root, got %T", material)
	}
	if len(trusted.FulcioCertificateAuthorities()) == 0 {
		t.Fatalf("a trust root with no certificate authority would accept no keyless signature at all")
	}
	if len(trusted.RekorLogs()) == 0 {
		t.Fatalf("a trust root with no transparency log would accept no logged signature at all")
	}
	if len(trusted.CTLogs()) == 0 {
		t.Fatalf("a trust root with no certificate transparency log cannot hold the shipped posture")
	}
}

// tamperWithField rotates one base64 character inside the value of the named
// field, and never the field name itself, so that the document still parses as
// a bundle and its content is no longer what was signed. A test that corrupted
// the JSON instead would prove only that broken JSON is rejected.
func tamperWithField(t *testing.T, bundleJSON []byte, field string) []byte {
	t.Helper()
	marker := []byte(`"` + field + `":"`)
	start := bytes.Index(bundleJSON, marker)
	if start < 0 {
		t.Fatalf("the serialised bundle holds no %s field to tamper with", field)
	}
	for i := start + len(marker); i < len(bundleJSON) && bundleJSON[i] != '"'; i++ {
		character := bundleJSON[i]
		if character >= 'a' && character <= 'y' {
			tampered := append([]byte(nil), bundleJSON...)
			tampered[i] = character + 1
			return tampered
		}
	}
	t.Fatalf("nothing inside the %s field could be altered", field)
	return nil
}
