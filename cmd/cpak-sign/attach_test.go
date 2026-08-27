/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/signature"
)

func signedIdentity() signature.Identity {
	return signature.Identity{
		Issuer:  "https://token.actions.githubusercontent.com",
		Subject: "https://github.com/example/app/.github/workflows/publish.yml@refs/heads/main",
		Repo:    testOrigin,
	}
}

// stubVerify holds the gate open or shut for one test and counts how often it
// was asked, so that a push can never be mistaken for a verified push.
func stubVerify(t *testing.T, identity signature.Identity, refusal error) *int {
	t.Helper()
	previous := verifyState
	calls := 0
	verifyState = func(bundle []byte, state signature.State) (signature.Verified, error) {
		calls++
		if refusal != nil {
			return signature.Verified{}, refusal
		}
		return signature.Verified{State: state, Identity: identity}, nil
	}
	t.Cleanup(func() { verifyState = previous })
	return &calls
}

func stageSignature(t *testing.T, directory string, state signature.State) (string, string, []byte) {
	t.Helper()
	payload, err := state.Canonical()
	if err != nil {
		t.Fatalf("encoding the state fixture failed: %v", err)
	}
	statePath := filepath.Join(directory, defaultStatePath)
	if err = os.WriteFile(statePath, payload, 0o644); err != nil {
		t.Fatalf("writing the state fixture failed: %v", err)
	}
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","verificationMaterial":{}}`)
	bundlePath := filepath.Join(directory, defaultBundlePath)
	if err = os.WriteFile(bundlePath, bundle, 0o644); err != nil {
		t.Fatalf("writing the bundle fixture failed: %v", err)
	}
	return statePath, bundlePath, bundle
}

func signedState(imageDigest string) signature.State {
	return signature.State{
		ABI:            signature.ABIVersion,
		Origin:         testOrigin,
		ManifestSHA256: strings.Repeat("d", 64),
		ImageDigest:    imageDigest,
		Generation:     4,
	}
}

// TestAttachVerifiesThroughTheSignaturePackage keeps the tests below honest:
// they drive the gate through a stub, and this is what says the gate they are
// standing in for is the real one.
func TestAttachVerifiesThroughTheSignaturePackage(t *testing.T) {
	if reflect.ValueOf(verifyState).Pointer() != reflect.ValueOf(signature.VerifyPublisher).Pointer() {
		t.Fatalf("attach does not verify through pkg/signature, so nothing proves a published bundle was ever checked")
	}
}

func TestAttachPublishesTheBundleAsAReferrerOfTheSignedImage(t *testing.T) {
	registry := newFakeRegistry(t)
	registry.requireToken = true
	t.Setenv(usernameVariable, fakeUsername)
	t.Setenv(passwordVariable, fakePassword)
	imageDigest := registry.publishImage("main")

	identity := signedIdentity()
	if !identity.MatchesOrigin(testOrigin) {
		t.Fatalf("the identity that names %s cannot speak for it, so no publication of it could ever be attached", testOrigin)
	}
	calls := stubVerify(t, identity, nil)

	directory := t.TempDir()
	statePath, bundlePath, bundle := stageSignature(t, directory, signedState(imageDigest))
	if err := attachSignature([]string{"--image", registry.reference(""), "--state", statePath, "--bundle", bundlePath}); err != nil {
		t.Fatalf("attaching the signature failed: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("the bundle was published after %d verifications", *calls)
	}
	if len(registry.pushed) != 1 {
		t.Fatalf("the registry took %d manifests", len(registry.pushed))
	}

	var published referrer
	var publishedDigest string
	for digest, body := range registry.pushed {
		publishedDigest = digest
		if err := json.Unmarshal(body, &published); err != nil {
			t.Fatalf("the published manifest is not a manifest: %v", err)
		}
	}
	if published.Subject.Digest != imageDigest {
		t.Fatalf("the signature hangs off %s and the state was signed over %s", published.Subject.Digest, imageDigest)
	}
	if published.Subject.MediaType != manifestMediaType {
		t.Fatalf("the subject is described as %s", published.Subject.MediaType)
	}
	if registry.subjects[publishedDigest] != imageDigest {
		t.Fatalf("the registry did not index the signature against %s", imageDigest)
	}
	if published.ArtifactType != signatureArtifactType {
		t.Fatalf("the signature was published as %s, which cpak does not look for", published.ArtifactType)
	}
	if published.SchemaVersion != 2 || published.MediaType != manifestMediaType {
		t.Fatalf("the signature manifest is a %s of schema %d", published.MediaType, published.SchemaVersion)
	}
	if len(published.Layers) != 1 {
		t.Fatalf("the signature carries %d layers", len(published.Layers))
	}
	layer := published.Layers[0]
	if layer.Digest != digestOf(bundle) || layer.Size != int64(len(bundle)) || layer.MediaType != bundleMediaType {
		t.Fatalf("the layer describes %s of %d bytes as %s", layer.Digest, layer.Size, layer.MediaType)
	}
	if stored := registry.uploaded[layer.Digest]; !bytes.Equal(stored, bundle) {
		t.Fatalf("the registry holds %q where the bundle was expected", stored)
	}
	if generation := published.Annotations[generationAnnotation]; generation != "4" {
		t.Fatalf("the referrer names generation %q, which is not the one that was signed", generation)
	}
	if published.Config.Digest != digestOf(emptyConfig) {
		t.Fatalf("the manifest points its configuration at %s", published.Config.Digest)
	}
	if stored := registry.uploaded[published.Config.Digest]; !bytes.Equal(stored, emptyConfig) {
		t.Fatalf("the configuration blob the manifest names was never uploaded")
	}
}

// A registry that stores a manifest without indexing it answers nothing for it,
// which is where the specification's fallback tag applies. Refusing instead
// would mean no package on such a registry could ever carry a signature, and
// that includes the one every cpak package is published to.
func TestAttachKeepsItsOwnIndexWhenTheRegistryDoesNotIndexReferrers(t *testing.T) {
	registry := newFakeRegistry(t)
	registry.indexReferrers = false
	imageDigest := registry.publishImage("main")
	stubVerify(t, signedIdentity(), nil)

	directory := t.TempDir()
	statePath, bundlePath, _ := stageSignature(t, directory, signedState(imageDigest))
	if err := attachSignature([]string{"--image", registry.reference(""), "--state", statePath, "--bundle", bundlePath}); err != nil {
		t.Fatalf("the fallback was not taken: %v", err)
	}

	tag := strings.Replace(imageDigest, ":", "-", 1)
	stored, found := registry.manifests[tag]
	if !found {
		t.Fatalf("nothing was written to %s, so the signature is one nobody will be served", tag)
	}
	var index fallbackIndex
	if err := json.Unmarshal(stored, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 {
		t.Fatalf("the index names %d referrers", len(index.Manifests))
	}
	if index.Manifests[0].ArtifactType != signatureArtifactType {
		t.Fatalf("the referrer is filed as %q", index.Manifests[0].ArtifactType)
	}
	// A reader takes the publisher generation off the referrer, so an index
	// without it publishes a signature that is found and then skipped.
	if got := index.Manifests[0].Annotations[generationAnnotation]; got != "4" {
		t.Fatalf("the referrer names generation %q, so nothing can tell which state it covers", got)
	}
	// The descriptor has to name a manifest the registry actually holds,
	// because a reader follows it by digest and nothing else.
	if _, held := registry.manifests[index.Manifests[0].Digest]; !held {
		t.Fatal("the index names a manifest the registry never stored")
	}
}

// Publishing twice must leave one signature and not a growing list, and must
// not throw away anything published beside it.
func TestTheFallbackIndexKeepsOneSignatureAndWhateverElseIsThere(t *testing.T) {
	registry := newFakeRegistry(t)
	registry.indexReferrers = false
	imageDigest := registry.publishImage("main")
	stubVerify(t, signedIdentity(), nil)

	directory := t.TempDir()
	statePath, bundlePath, _ := stageSignature(t, directory, signedState(imageDigest))
	for attempt := 0; attempt < 2; attempt++ {
		if err := attachSignature([]string{"--image", registry.reference(""), "--state", statePath, "--bundle", bundlePath}); err != nil {
			t.Fatalf("attach %d failed: %v", attempt, err)
		}
	}
	var index fallbackIndex
	if err := json.Unmarshal(registry.manifests[strings.Replace(imageDigest, ":", "-", 1)], &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 {
		t.Fatalf("two publications left %d referrers", len(index.Manifests))
	}
}

func TestAttachPublishesNothingWhenTheBundleDoesNotVerify(t *testing.T) {
	registry := newFakeRegistry(t)
	imageDigest := registry.publishImage("main")
	calls := stubVerify(t, signedIdentity(), errors.New("the bundle covers another state"))

	directory := t.TempDir()
	statePath, bundlePath, _ := stageSignature(t, directory, signedState(imageDigest))
	err := attachSignature([]string{"--image", registry.reference(""), "--state", statePath, "--bundle", bundlePath})
	if err == nil {
		t.Fatalf("a bundle that verifies against nothing was published")
	}
	if *calls != 1 {
		t.Fatalf("the gate was asked %d times", *calls)
	}
	if registry.requests != 0 {
		t.Fatalf("the registry was contacted %d times for a bundle that does not verify", registry.requests)
	}
}

func TestAttachRefusesAnIdentityThatCannotSpeakForTheOrigin(t *testing.T) {
	registry := newFakeRegistry(t)
	imageDigest := registry.publishImage("main")
	stranger := signature.Identity{
		Issuer:  "https://token.actions.githubusercontent.com",
		Subject: "https://github.com/someone/else/.github/workflows/publish.yml@refs/heads/main",
		Repo:    "github.com/someone/else",
	}
	if stranger.MatchesOrigin(testOrigin) {
		t.Fatalf("%s is allowed to speak for %s", stranger.Repo, testOrigin)
	}
	stubVerify(t, stranger, nil)

	directory := t.TempDir()
	statePath, bundlePath, _ := stageSignature(t, directory, signedState(imageDigest))
	err := attachSignature([]string{"--image", registry.reference(""), "--state", statePath, "--bundle", bundlePath})
	if err == nil {
		t.Fatalf("a signature from another repository was published for %s", testOrigin)
	}
	if registry.requests != 0 {
		t.Fatalf("the registry was contacted %d times for a signature from another repository", registry.requests)
	}
}

func TestAttachRefusesAPayloadThatIsNotCanonical(t *testing.T) {
	registry := newFakeRegistry(t)
	imageDigest := registry.publishImage("main")
	calls := stubVerify(t, signedIdentity(), nil)

	directory := t.TempDir()
	statePath, bundlePath, _ := stageSignature(t, directory, signedState(imageDigest))
	content, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading the payload fixture failed: %v", err)
	}
	// A payload that decodes to the same state and is written differently is
	// the case that matters: the signature covers the bytes, so those bytes are
	// what has to be published.
	rewritten := bytes.Replace(content, []byte("generation=4"), []byte("generation=04"), 1)
	if bytes.Equal(rewritten, content) {
		t.Fatalf("the payload fixture does not carry the generation as expected: %s", content)
	}
	if err = os.WriteFile(statePath, rewritten, 0o644); err != nil {
		t.Fatalf("rewriting the payload fixture failed: %v", err)
	}

	err = attachSignature([]string{"--image", registry.reference(""), "--state", statePath, "--bundle", bundlePath})
	if err == nil {
		t.Fatalf("a payload that is not the canonical state was published, and no installation could reproduce it")
	}
	if !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("the refusal does not say the payload is not canonical: %v", err)
	}
	if *calls != 0 || registry.requests != 0 {
		t.Fatalf("the payload reached the gate %d times and the registry %d times", *calls, registry.requests)
	}
}
