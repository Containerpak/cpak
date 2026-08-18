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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	signatureRepository = "example/app"

	// githubActionsIssuer is the one authority a keyless signature can name a
	// repository through, and an identity without it speaks for no origin.
	githubActionsIssuer = "https://token.actions.githubusercontent.com"

	// sigstoreBundleMediaType is what the payload of a cpak signature is, and
	// is deliberately not what the artifact is recognised by: the referrer is
	// found through its artifactType and the bundle sits inside it.
	sigstoreBundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"

	// cosignLegacyType stands for anything else that can hang off an image.
	cosignLegacyType = "application/vnd.dev.cosign.artifact.sig.v1+json"
)

// signatureRegistry serves what a registry serves around a referrer: the index
// of what is attached to a subject digest, the artifact manifests that index
// names, and the blobs those carry.
//
// It ignores the artifactType query on purpose, which the distribution spec
// allows a registry to do, so that what cpak makes of an unfiltered answer is
// exercised instead of assumed.
type signatureRegistry struct {
	referrers map[string][]oci.Descriptor
	manifests map[string][]byte
	blobs     map[string][]byte
	silent    map[string]bool
}

func newSignatureRegistry() *signatureRegistry {
	return &signatureRegistry{
		referrers: map[string][]oci.Descriptor{},
		manifests: map[string][]byte{},
		blobs:     map[string][]byte{},
		silent:    map[string]bool{},
	}
}

// attach files one artifact against a subject digest, the way cosign attaches
// a signature: a manifest with a single layer, and the payload in that layer.
func (r *signatureRegistry) attach(t *testing.T, subject, artifactType string, payload []byte) {
	t.Helper()

	payloadDigest := contentDigest(payload)
	r.blobs[payloadDigest] = payload
	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  artifactType,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    contentDigest([]byte("{}")),
			"size":      2,
		},
		"layers": []any{map[string]any{
			"mediaType": sigstoreBundleMediaType,
			"digest":    payloadDigest,
			"size":      len(payload),
		}},
		"subject": map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    subject,
			"size":      1,
		},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("the referrer manifest could not be encoded: %v", err)
	}
	digest := contentDigest(encoded)
	r.manifests[digest] = encoded
	r.referrers[subject] = append(r.referrers[subject], oci.Descriptor{
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		ArtifactType: artifactType,
		Digest:       digest,
		Size:         int64(len(encoded)),
	})
}

func (r *signatureRegistry) start(t *testing.T) oci.Reference {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(server.Close)
	ref, err := oci.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/" + signatureRepository + ":main")
	if err != nil {
		t.Fatalf("the test registry reference is invalid: %v", err)
	}
	return ref
}

func (r *signatureRegistry) serve(writer http.ResponseWriter, request *http.Request) {
	prefix := "/v2/" + signatureRepository + "/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(writer, request)
		return
	}
	path := strings.TrimPrefix(request.URL.Path, prefix)
	switch {
	case strings.HasPrefix(path, "referrers/"):
		subject := strings.TrimPrefix(path, "referrers/")
		if r.silent[subject] {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.oci.image.index.v1+json",
			"manifests":     r.referrers[subject],
		})
	case strings.HasPrefix(path, "manifests/"):
		body, found := r.manifests[strings.TrimPrefix(path, "manifests/")]
		if !found {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		_, _ = writer.Write(body)
	case strings.HasPrefix(path, "blobs/"):
		body, found := r.blobs[strings.TrimPrefix(path, "blobs/")]
		if !found {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(body)
	default:
		http.NotFound(writer, request)
	}
}

func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// newSignatureCpak keeps the host out of the test: registry credentials are
// read from an environment variable when one is set, and a machine that has
// one would otherwise answer differently from a machine that has not.
func newSignatureCpak(t *testing.T) *Cpak {
	t.Helper()

	t.Setenv("CPAK_REGISTRY_AUTH_FILE", "")
	return newTestCpak(t)
}

// useSignatureVerifier points the offline check at one the test drives, and
// puts back the one cpak uses when the test ends.
func useSignatureVerifier(t *testing.T, verify func([]byte, signature.State) (signature.Verified, error)) {
	t.Helper()

	previous := verifySignature
	verifySignature = verify
	t.Cleanup(func() { verifySignature = previous })
}

// validatedTestManifest is the manifest as an installation sees it, which is
// the only form a state may be named from: validation fills the defaults in
// and migrates what cpak no longer reads, and the publisher hashed the result.
func validatedTestManifest(t *testing.T) *types.CpakManifest {
	t.Helper()

	manifest := newTestManifest()
	if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
		t.Fatalf("the test manifest is not valid: %v", err)
	}
	return manifest
}

// signedState is the state a publisher would have signed for the test package.
// It is built through PackageState rather than by hand so that a verification
// test never checks a state cpak itself would not produce.
func signedState(t *testing.T, imageDigest string) signature.State {
	t.Helper()

	state, err := PackageState(testOrigin, validatedTestManifest(t), imageDigest, nil)
	if err != nil {
		t.Fatalf("the state of the test package could not be named: %v", err)
	}
	state.Generation = 1
	return state
}

// The bundle a fetch returns is the one attached to the digest it was given,
// read back through the manifest that names it and checked against the digest
// that manifest declared.
func TestFetchPackageSignatureReturnsWhatIsAttachedToTheResolvedDigest(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)
	registry.attach(t, resolved, packageSignatureArtifactType, bundle)
	ref := registry.start(t)

	fetched, found, err := cp.FetchPackageSignature(ref, resolved)
	if err != nil {
		t.Fatalf("the attached signature could not be fetched: %v", err)
	}
	if !found {
		t.Fatal("the fetch reported no signature for a digest one is attached to")
	}
	if string(fetched) != string(bundle) {
		t.Fatalf("got bundle %q, want the payload the registry serves %q", fetched, bundle)
	}
}

// The subject of the fetch is the digest that was resolved, so a signature
// attached to any other digest is not this package's signature. This is the
// shape of the whole mechanism: an image the tag was repointed to carries no
// signature of the state that was signed, and it must read as unsigned rather
// than as the signature of something else.
func TestFetchPackageSignatureAnswersNothingForADigestNothingIsAttachedTo(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	signed := contentDigest([]byte("the image that was signed"))
	registry.attach(t, signed, packageSignatureArtifactType, []byte("bundle"))
	ref := registry.start(t)

	other := contentDigest([]byte("the image the tag now points at"))
	fetched, found, err := cp.FetchPackageSignature(ref, other)
	if err != nil {
		t.Fatalf("a digest with nothing attached must not be an error: %v", err)
	}
	if found || fetched != nil {
		t.Fatalf("got bundle %q found=%v, want nothing for a digest no signature covers", fetched, found)
	}
}

// A registry is allowed to ignore the artifactType filter. What comes back
// unfiltered must still be filtered here, or an artifact of another kind would
// be handed to the verifier as though it were a bundle.
func TestFetchPackageSignatureIgnoresAttachedArtifactsThatAreNotBundles(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	registry.attach(t, resolved, cosignLegacyType, []byte("simple signing"))
	ref := registry.start(t)

	fetched, found, err := cp.FetchPackageSignature(ref, resolved)
	if err != nil {
		t.Fatalf("an artifact of another kind must not be an error: %v", err)
	}
	if found || fetched != nil {
		t.Fatalf("got bundle %q found=%v, want an artifact that is not a bundle to be ignored", fetched, found)
	}
}

// A registry that does not implement the referrers API answers 404. An
// unsigned package must keep working, so that is nothing attached and not a
// failed installation.
func TestFetchPackageSignatureAnswersNothingWhenTheRegistryHasNoReferrersAPI(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	registry.silent[resolved] = true
	ref := registry.start(t)

	fetched, found, err := cp.FetchPackageSignature(ref, resolved)
	if err != nil {
		t.Fatalf("a registry without the referrers API must not be an error: %v", err)
	}
	if found || fetched != nil {
		t.Fatalf("got bundle %q found=%v, want nothing from a registry that answered 404", fetched, found)
	}
}

// The payload is checked against the digest the manifest gave it. A registry
// that serves other bytes under that digest is broken, and reporting it as an
// unsigned package would turn a fault into a silent downgrade.
func TestFetchPackageSignatureRefusesAPayloadTheRegistryMisnamed(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	bundle := []byte("bundle-bytes")
	registry.attach(t, resolved, packageSignatureArtifactType, bundle)
	registry.blobs[contentDigest(bundle)] = []byte("tampered!xyz")
	ref := registry.start(t)

	fetched, found, err := cp.FetchPackageSignature(ref, resolved)
	if err == nil {
		t.Fatalf("got bundle %q found=%v, want a payload that is not what its digest names to be refused", fetched, found)
	}
	if found {
		t.Fatal("a refused payload was still reported as a signature")
	}
}

// The one mistake this file exists to prevent: a fetch asked about what the
// manifest declared instead of what the registry resolved. A tag is refused
// rather than treated as a subject, because a signature over a tag is a
// signature over whatever it was last pointed at.
func TestFetchPackageSignatureRefusesAReferenceInPlaceOfAResolvedDigest(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)

	for _, asked := range []string{"", "main", "ghcr.io/example/app:main", "sha256:not-a-digest"} {
		fetched, found, err := cp.FetchPackageSignature(ref, asked)
		if err == nil {
			t.Fatalf("got bundle %q found=%v for %q, want anything that is not a resolved digest to be refused", fetched, found, asked)
		}
	}
}

// Verification is handed the bundle the registry served and the state the
// caller resolved, unchanged. If either were rebuilt on the way, a signature
// would be checked against something other than what is being installed.
func TestVerifyPackageStateChecksTheBundleAgainstTheStateItWasGiven(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	bundle := []byte("the bundle the registry holds")
	registry.attach(t, resolved, packageSignatureArtifactType, bundle)
	ref := registry.start(t)
	state := signedState(t, resolved)

	var checkedBundle []byte
	var checkedState signature.State
	useSignatureVerifier(t, func(given []byte, against signature.State) (signature.Verified, error) {
		checkedBundle = given
		checkedState = against
		return signature.Verified{
			State:    against,
			Identity: signature.Identity{Issuer: githubActionsIssuer, Subject: "https://" + testOrigin + "/.github/workflows/release.yml@refs/heads/main", Repo: testOrigin},
		}, nil
	})

	verified, err := cp.verifyPackageState(ref, testOrigin, state)
	if err != nil {
		t.Fatalf("a signature made by the origin was refused: %v", err)
	}
	if string(checkedBundle) != string(bundle) {
		t.Fatalf("got bundle %q, want the one the registry served %q", checkedBundle, bundle)
	}
	if checkedState != state {
		t.Fatalf("got state %+v, want the resolved state %+v", checkedState, state)
	}
	if verified.Identity.Repo != testOrigin {
		t.Fatalf("got identity %+v, want the one verification reported", verified.Identity)
	}
}

// Nothing attached and something attached that does not hold are two different
// facts. A caller allowing unsigned packages while refusing broken signatures
// can only do that if it can tell them apart.
func TestVerifyPackageStateSeparatesUnsignedFromUnverified(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	resolved := contentDigest([]byte("resolved image"))

	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		t.Fatal("verification ran for a package nothing is attached to")
		return signature.Verified{}, nil
	})

	_, err := cp.verifyPackageState(ref, testOrigin, signedState(t, resolved))
	if !errors.Is(err, ErrPackageUnsigned) {
		t.Fatalf("got %v, want an unsigned package to be reported as %v", err, ErrPackageUnsigned)
	}
	if errors.Is(err, ErrSignatureUnverified) || errors.Is(err, ErrSignatureForeign) {
		t.Fatalf("got %v, want an unsigned package not to be read as a failed check", err)
	}
}

// A bundle that does not hold is its own answer, and it must never be softened
// into the absence of a signature.
func TestVerifyPackageStateReportsASignatureThatDoesNotHold(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	registry.attach(t, resolved, packageSignatureArtifactType, []byte("bundle"))
	ref := registry.start(t)

	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("the certificate does not cover this state")
	})

	_, err := cp.verifyPackageState(ref, testOrigin, signedState(t, resolved))
	if !errors.Is(err, ErrSignatureUnverified) {
		t.Fatalf("got %v, want a signature that does not hold to be reported as %v", err, ErrSignatureUnverified)
	}
	if errors.Is(err, ErrPackageUnsigned) || errors.Is(err, ErrSignatureForeign) {
		t.Fatalf("got %v, want a failed check not to be read as unsigned or foreign", err)
	}
	if !strings.Contains(err.Error(), "the certificate does not cover this state") {
		t.Fatalf("got %v, want the reason verification gave to survive", err)
	}
}

// A signature that holds is not enough. The certificate says who signed, and
// an identity that cannot speak for this origin is a signature of somebody
// else's software attached to this image.
func TestVerifyPackageStateRefusesAnIdentityThatCannotSpeakForTheOrigin(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	registry.attach(t, resolved, packageSignatureArtifactType, []byte("bundle"))
	ref := registry.start(t)

	useSignatureVerifier(t, func(_ []byte, against signature.State) (signature.Verified, error) {
		return signature.Verified{
			State:    against,
			Identity: signature.Identity{Issuer: githubActionsIssuer, Subject: "https://github.com/attacker/demo/.github/workflows/release.yml@refs/heads/main", Repo: "github.com/attacker/demo"},
		}, nil
	})

	verified, err := cp.verifyPackageState(ref, testOrigin, signedState(t, resolved))
	if !errors.Is(err, ErrSignatureForeign) {
		t.Fatalf("got %+v %v, want a signature made by another identity to be reported as %v", verified, err, ErrSignatureForeign)
	}
	if errors.Is(err, ErrPackageUnsigned) || errors.Is(err, ErrSignatureUnverified) {
		t.Fatalf("got %v, want a foreign signature not to be read as unsigned or unverified", err)
	}
}

// A publisher that re-signs a state leaves the earlier bundle attached beside
// the new one. Every bundle is put to the same question, so an image that
// carries one that no longer holds is not refused because of it.
func TestVerifyPackageStateAcceptsTheBundleThatHoldsAmongSeveral(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	registry.attach(t, resolved, packageSignatureArtifactType, []byte("stale bundle"))
	registry.attach(t, resolved, packageSignatureArtifactType, []byte("current bundle"))
	ref := registry.start(t)

	useSignatureVerifier(t, func(given []byte, against signature.State) (signature.Verified, error) {
		if string(given) != "current bundle" {
			return signature.Verified{}, errors.New("this bundle does not cover the state")
		}
		return signature.Verified{State: against, Identity: signature.Identity{Issuer: githubActionsIssuer, Repo: testOrigin}}, nil
	})

	verified, err := cp.verifyPackageState(ref, testOrigin, signedState(t, resolved))
	if err != nil {
		t.Fatalf("a state one attached bundle covers was refused: %v", err)
	}
	if verified.Identity.Repo != testOrigin {
		t.Fatalf("got identity %+v, want the bundle that holds to be the one reported", verified.Identity)
	}
}

// The exported entry point has no reference, so it takes the one the digest
// was installed from. An origin installed more than once must not answer with
// another of its installations: that would fetch the signature of a package
// nobody asked about.
func TestVerifyPackageStateFindsTheInstallationTheDigestNames(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	registry.attach(t, resolved, packageSignatureArtifactType, []byte("bundle"))
	ref := registry.start(t)

	seedApplication(t, cp, types.Application{
		CpakId:      testCpakId("branch", "other"),
		Name:        "demo",
		Version:     "other",
		Branch:      "other",
		Origin:      testOrigin,
		Image:       ref.ContextName() + "-elsewhere:other",
		ImageDigest: contentDigest([]byte("another image")),
	})
	seedApplication(t, cp, types.Application{
		CpakId:      testCpakId("branch", "main"),
		Name:        "demo",
		Version:     "main",
		Branch:      "main",
		Origin:      testOrigin,
		Image:       ref.Name(),
		ImageDigest: resolved,
	})

	useSignatureVerifier(t, func(_ []byte, against signature.State) (signature.Verified, error) {
		return signature.Verified{State: against, Identity: signature.Identity{Issuer: githubActionsIssuer, Repo: testOrigin}}, nil
	})

	verified, err := cp.VerifyPackageState(testOrigin, signedState(t, resolved))
	if err != nil {
		t.Fatalf("the installation the digest names was not found: %v", err)
	}
	if verified.State.ImageDigest != resolved {
		t.Fatalf("got state %+v, want the one resolved for %s", verified.State, resolved)
	}
}

// A digest no installation was resolved to is not a verification failure, and
// it must not be answered with some other installation of the same origin.
func TestVerifyPackageStateRefusesADigestNoInstallationWasResolvedTo(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	seedApplication(t, cp, types.Application{
		CpakId:      testCpakId("branch", "main"),
		Name:        "demo",
		Version:     "main",
		Branch:      "main",
		Origin:      testOrigin,
		Image:       ref.Name(),
		ImageDigest: contentDigest([]byte("what is installed")),
	})

	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		t.Fatal("verification ran for a digest nothing was installed from")
		return signature.Verified{}, nil
	})

	_, err := cp.VerifyPackageState(testOrigin, signedState(t, contentDigest([]byte("never installed"))))
	if err == nil {
		t.Fatal("a digest no installation was resolved to was accepted")
	}
	if errors.Is(err, ErrPackageUnsigned) || errors.Is(err, ErrSignatureUnverified) || errors.Is(err, ErrSignatureForeign) {
		t.Fatalf("got %v, want a missing installation not to be read as an answer about a signature", err)
	}
}

// The gap this round leaves open, pinned so that it cannot be mistaken for a
// working path: a state nobody supplied a generation for is refused before a
// byte is fetched, and the refusal is none of the three answers about a
// signature, because no signature was looked at.
func TestVerifyPackageStateRefusesAStateWithNoGeneration(t *testing.T) {
	cp := newSignatureCpak(t)
	registry := newSignatureRegistry()
	resolved := contentDigest([]byte("resolved image"))
	registry.attach(t, resolved, packageSignatureArtifactType, []byte("bundle"))
	ref := registry.start(t)

	useSignatureVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		t.Fatal("a state that cannot mean anything was put to verification")
		return signature.Verified{}, nil
	})

	state, err := PackageState(testOrigin, validatedTestManifest(t), resolved, nil)
	if err != nil {
		t.Fatalf("the state of a resolved installation could not be named: %v", err)
	}
	if state.Generation != 0 {
		t.Fatalf("got generation %d, want none until something supplies it", state.Generation)
	}
	if _, err = cp.verifyPackageState(ref, testOrigin, state); !errors.Is(err, signature.ErrInvalidState) {
		t.Fatalf("got %v, want a state with no generation to be refused as not well formed", err)
	}
	if errors.Is(err, ErrPackageUnsigned) || errors.Is(err, ErrSignatureUnverified) || errors.Is(err, ErrSignatureForeign) {
		t.Fatalf("got %v, want a state that was never checked not to be read as an answer about a signature", err)
	}
}

// A manifest that has not been validated is not the manifest an installation
// is made from, and the two do not hash the same. Naming a state from one
// would produce a payload no signature can ever cover.
func TestPackageStateRefusesAManifestThatWasNotValidated(t *testing.T) {
	raw := newTestManifest()
	resolved := contentDigest([]byte("resolved image"))

	state, err := PackageState(testOrigin, raw, resolved, nil)
	if err == nil {
		t.Fatalf("got state %+v, want a manifest that was not validated to be refused", state)
	}
	before, err := manifestDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, err := manifestDigest(validatedTestManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("validation left the manifest hash unchanged, so the guard is protecting nothing")
	}
}

// The state is named by what was resolved. A reference is not a resolution, so
// naming a state with the one the manifest declared is refused instead of
// hashed into a payload that would then be checked against a registry answer.
func TestPackageStateRefusesTheReferenceAManifestAskedFor(t *testing.T) {
	manifest := validatedTestManifest(t)
	manifest.ImageRef = "source"

	for _, asked := range []string{"", manifest.Image, "main", "sha256:short"} {
		state, err := PackageState(testOrigin, manifest, asked, nil)
		if err == nil {
			t.Fatalf("got state %+v for %q, want anything that is not a resolved digest to be refused", state, asked)
		}
	}
}

// What a state names: the origin, the manifest as cpak applied it, the digest
// that came back, and the lock when the installation went through one. The
// digest is the one passed in and never the one the manifest declared, and two
// different locks are two different states.
func TestPackageStateNamesTheResolvedDigestAndTheLock(t *testing.T) {
	manifest := validatedTestManifest(t)
	manifest.ImageRef = "source"
	resolved := contentDigest([]byte("what the tag resolved to"))

	state, err := PackageState(strings.ToUpper(testOrigin), manifest, resolved, nil)
	if err != nil {
		t.Fatalf("the state of a resolved installation could not be named: %v", err)
	}
	if state.Origin != testOrigin {
		t.Fatalf("got origin %q, want the one cpak files an installation under %q", state.Origin, testOrigin)
	}
	if state.ImageDigest != resolved {
		t.Fatalf("got image digest %q, want the resolved one %q", state.ImageDigest, resolved)
	}
	if strings.Contains(state.ImageDigest, manifest.Image) {
		t.Fatalf("got image digest %q, want nothing of the reference the manifest asked for", state.ImageDigest)
	}
	applied, err := manifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if state.ManifestSHA256 != applied {
		t.Fatalf("got manifest digest %q, want the one the publisher signs %q", state.ManifestSHA256, applied)
	}
	if state.LockSHA256 != "" {
		t.Fatalf("got lock digest %q, want none for an installation resolved without a lock", state.LockSHA256)
	}

	locked, err := PackageState(testOrigin, manifest, resolved, &types.ManifestLock{LockVersion: types.ManifestLockVersion, Root: types.LockedPackage{Origin: testOrigin}})
	if err != nil {
		t.Fatalf("the state of a locked installation could not be named: %v", err)
	}
	if locked.LockSHA256 == "" {
		t.Fatal("an installation resolved through a lock named no lock")
	}
	other, err := PackageState(testOrigin, manifest, resolved, &types.ManifestLock{LockVersion: types.ManifestLockVersion, Root: types.LockedPackage{Origin: testOrigin, Commit: "9f1c2d3"}})
	if err != nil {
		t.Fatal(err)
	}
	if other.LockSHA256 == locked.LockSHA256 {
		t.Fatalf("two different locks named the same state %q", locked.LockSHA256)
	}
}
