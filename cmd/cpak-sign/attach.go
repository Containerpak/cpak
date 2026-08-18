/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/signature"
)

const (
	// signatureArtifactType is how cpak recognises its own referrers among
	// everything else that can hang off an image.
	signatureArtifactType = "application/vnd.cpak.signature.v1+json"

	bundleMediaType   = "application/vnd.dev.sigstore.bundle.v0.3+json"
	defaultBundlePath = "cpak-state.sigstore.json"
	bundleLimit       = 1 << 20
	stateLimit        = 64 << 10

	// generationAnnotation carries the one field of a signed state that the
	// installing machine cannot derive from what it installed. It is a hint and
	// never evidence: a wrong value produces a state the bundle does not cover,
	// which is a refusal, so nothing can be gained by writing a false one.
	generationAnnotation = "dev.cpak.signature.generation"
)

// verifyState is the gate an attach passes before a byte is pushed. A bundle
// that does not cover the state, or one signed by an identity that cannot speak
// for the origin, is a bundle every user would reject, and it is better
// rejected here than in front of them. It is a variable so that a test can hold
// one that fails; nothing in cpak-sign replaces it.
var verifyState = signature.Verify

// referrer is the artifact manifest a signature is published as. The subject is
// the image manifest the state was signed over, which is what makes the
// registry index it beside the image instead of beside a tag.
type referrer struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Subject       descriptor        `json:"subject"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

func attachSignature(arguments []string) error {
	flags := flag.NewFlagSet("attach", flag.ContinueOnError)
	image := flags.String("image", "", "image repository the signature is attached to")
	statePath := flags.String("state", defaultStatePath, "path to the payload that was signed")
	bundlePath := flags.String("bundle", defaultBundlePath, "path to the sigstore bundle")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *image == "" {
		return errors.New("image is required, and names the repository the signed image lives in")
	}
	reference, err := oci.ParseReference(*image)
	if err != nil {
		return err
	}
	state, err := readSignedState(*statePath)
	if err != nil {
		return err
	}
	bundle, err := readFile(*bundlePath, bundleLimit)
	if err != nil {
		return fmt.Errorf("read the bundle: %w", err)
	}
	verified, err := verifyState(bundle, state)
	if err != nil {
		return fmt.Errorf("the bundle in %s does not cover the state in %s: %w", *bundlePath, *statePath, err)
	}
	if !verified.Identity.MatchesOrigin(state.Origin) {
		return fmt.Errorf("the state was signed by %s from %s, which cannot speak for %s", verified.Identity.Subject, verified.Identity.Issuer, state.Origin)
	}
	return attachBundle(context.Background(), reference, state, bundle)
}

func attachBundle(ctx context.Context, reference oci.Reference, state signature.State, bundle []byte) error {
	client := newRegistry(reference)
	if err := client.authorize(ctx); err != nil {
		return err
	}
	subject, err := client.subjectDescriptor(ctx, state.ImageDigest)
	if err != nil {
		return err
	}
	config, err := client.pushBlob(ctx, emptyConfigMediaType, emptyConfig)
	if err != nil {
		return err
	}
	layer, err := client.pushBlob(ctx, bundleMediaType, bundle)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(referrer{
		SchemaVersion: 2,
		MediaType:     manifestMediaType,
		ArtifactType:  signatureArtifactType,
		Config:        config,
		Layers:        []descriptor{layer},
		Subject:       subject,
		Annotations:   map[string]string{generationAnnotation: strconv.FormatUint(state.Generation, 10)},
	})
	if err != nil {
		return fmt.Errorf("encode the signature manifest: %w", err)
	}
	indexed, err := client.pushManifest(ctx, encoded)
	if err != nil {
		return err
	}
	// A signature the registry stored but does not answer for is a signature
	// nobody will ever be served, so it is a failed publication and not a
	// warning.
	if indexed == "" {
		return fmt.Errorf("%s stored the signature but did not index it as a referrer of %s: cpak looks a signature up through the OCI referrers API, which this registry does not implement", reference.Registry, state.ImageDigest)
	}
	if indexed != state.ImageDigest {
		return fmt.Errorf("%s indexed the signature under %s and not %s", reference.Registry, indexed, state.ImageDigest)
	}
	fmt.Fprintf(os.Stderr, "attached %s to %s@%s\n", digestOf(encoded), reference.ContextName(), state.ImageDigest)
	return nil
}

// readSignedState reads back the payload that was signed. A payload that is not
// the canonical encoding of the state it carries is refused: the signature
// covers those exact bytes, so a file that says one thing and encodes another
// would publish a signature no installation can reproduce.
func readSignedState(path string) (signature.State, error) {
	content, err := readFile(path, stateLimit)
	if err != nil {
		return signature.State{}, fmt.Errorf("read the state: %w", err)
	}
	state, err := parseCanonicalState(content)
	if err != nil {
		return signature.State{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if err = state.Validate(); err != nil {
		return signature.State{}, fmt.Errorf("the state in %s is not complete: %w", path, err)
	}
	canonical, err := state.Canonical()
	if err != nil {
		return signature.State{}, fmt.Errorf("encode the state in %s: %w", path, err)
	}
	if !bytes.Equal(canonical, content) {
		return signature.State{}, fmt.Errorf("%s is not the canonical encoding of the state it carries: sign the payload cpak-sign state wrote, byte for byte", path)
	}
	if !digestPattern.MatchString(state.ImageDigest) {
		return signature.State{}, fmt.Errorf("the state in %s names %q, which is not an image digest", path, state.ImageDigest)
	}
	return state, nil
}

// parseCanonicalState reads a payload back into the state it encodes. It is a
// reader for the canonical format and never a second definition of it: what it
// produces is put through Canonical again by the caller and refused unless it
// comes back as the same bytes, so a reading that differs from the one in
// pkg/signature cannot be published.
func parseCanonicalState(content []byte) (signature.State, error) {
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) < 2 {
		return signature.State{}, errors.New("the payload carries no fields")
	}
	fields := make(map[string]string, len(lines))
	for _, line := range lines[1:] {
		name, value, found := strings.Cut(line, "=")
		if !found {
			return signature.State{}, fmt.Errorf("%q is not a field of a state", line)
		}
		fields[name] = value
	}
	abi, err := strconv.Atoi(fields["abi"])
	if err != nil {
		return signature.State{}, fmt.Errorf("the payload names abi %q", fields["abi"])
	}
	generation, err := strconv.ParseUint(fields["generation"], 10, 64)
	if err != nil {
		return signature.State{}, fmt.Errorf("the payload names generation %q", fields["generation"])
	}
	return signature.State{
		ABI:            abi,
		Origin:         fields["origin"],
		ManifestSHA256: fields["manifest_sha256"],
		ImageDigest:    fields["image_digest"],
		LockSHA256:     fields["lock_sha256"],
		Generation:     generation,
	}, nil
}

func readFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, limit)
}
