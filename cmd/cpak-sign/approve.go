/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/signature"
)

// This is the command an organisation runs, and it is the only one in
// cpak-sign that is not for the publisher.
//
// A publisher signature says a package came from where it says it came from.
// Every package is signed by its own repository, so that is a fact about a
// name and it approves nothing. An approval is the second party: the same
// state the publisher signed, signed again by an identity that is not the
// publisher's, which is the only way trust can come from somewhere other than
// the package asserting itself.
//
// Two things this command deliberately does not need. It does not need the
// publisher's key, because it makes a signature of its own over bytes the
// publisher wrote and never touches what the publisher signed. It does not
// need the publisher's signature to exist at all, because a host may require
// an approval without requiring a publisher signature, and an approval that
// only worked on already signed packages would be no use on the software an
// organisation most needs to vet.
//
// What it does need is the exact state. An approval over a state that is a
// near miss is an approval of nothing, so the payload is read back, re-encoded
// and refused unless it comes out byte for byte the way it went in.
//
// The approval is pushed to the repository named by --image, which is the one
// this organisation can push to. For its own software that is where it
// publishes; for somebody else's it is the mirror it installs from, because a
// referrer lives beside the image and nobody can attach one to a registry they
// have no account on.

const (
	// approvalArtifactType is how cpak recognises a counter-signature among
	// everything else that can hang off an image, and is the same string
	// pkg/cpak reads one under. It is deliberately not the type a publisher
	// signature is attached under: a verifier that could not tell the two
	// apart would count every signed package as approved by itself.
	approvalArtifactType = "application/vnd.cpak.approval.v1+json"

	defaultApprovalBundlePath = "cpak-approval.sigstore.json"
)

func approveState(arguments []string) error {
	flags := flag.NewFlagSet("approve", flag.ContinueOnError)
	image := flags.String("image", "", "image repository the approval is attached to, which is the one this organisation can push to")
	statePath := flags.String("state", defaultStatePath, "path to the exact payload being approved")
	bundlePath := flags.String("bundle", defaultApprovalBundlePath, "path to this organisation's sigstore bundle over that payload")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *image == "" {
		return errors.New("image is required, and names the repository the approved image lives in")
	}
	reference, err := oci.ParseReference(*image)
	if err != nil {
		return err
	}
	// The state is read before the bundle so that a payload this command
	// cannot name exactly is refused without anything being signed over,
	// pushed, or reported as approved.
	state, err := readSignedState(*statePath)
	if err != nil {
		return err
	}
	bundle, err := readFile(*bundlePath, bundleLimit)
	if err != nil {
		return fmt.Errorf("read the approval bundle: %w", err)
	}
	verified, err := verifyState(bundle, state)
	if err != nil {
		return fmt.Errorf("the bundle in %s does not approve the state in %s: %w", *bundlePath, *statePath, err)
	}
	if verified.Identity.MatchesOrigin(state.Origin) {
		return fmt.Errorf("refusing to attach an approval made by %s, which is the publisher of %s: an approval is a second party's word about a release, and one made by the publisher says nothing its own signature does not already say", identityOf(verified.Identity), state.Origin)
	}
	return attachApproval(context.Background(), reference, state, bundle, verified.Identity)
}

// attachApproval publishes the counter-signature beside the image, under its
// own artifact type and with the subject the publisher's signature also hangs
// off. Nothing the publisher pushed is read, replaced or re-signed.
func attachApproval(ctx context.Context, reference oci.Reference, state signature.State, bundle []byte, approver signature.Identity) error {
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
	// The generation travels in the same annotation a publisher signature
	// carries it in, because it names the same field of the same state. A host
	// that already holds the publisher's state ignores it; one that requires
	// an approval and no publisher signature has nothing else to name the
	// state by, and a wrong value there produces a state this approval does
	// not cover, which is a refusal.
	encoded, err := json.Marshal(referrer{
		SchemaVersion: 2,
		MediaType:     manifestMediaType,
		ArtifactType:  approvalArtifactType,
		Config:        config,
		Layers:        []descriptor{layer},
		Subject:       subject,
		Annotations:   map[string]string{generationAnnotation: strconv.FormatUint(state.Generation, 10)},
	})
	if err != nil {
		return fmt.Errorf("encode the approval manifest: %w", err)
	}
	indexed, err := client.pushManifest(ctx, encoded)
	if err != nil {
		return err
	}
	// An approval the registry stored but does not answer for is one no
	// installation will ever be served, so it is a failed publication and not
	// a warning.
	if indexed == "" {
		return fmt.Errorf("%s stored the approval but did not index it as a referrer of %s: cpak looks an approval up through the OCI referrers API, which this registry does not implement", reference.Registry, state.ImageDigest)
	}
	if indexed != state.ImageDigest {
		return fmt.Errorf("%s indexed the approval under %s and not %s", reference.Registry, indexed, state.ImageDigest)
	}
	fmt.Fprintf(os.Stderr, "%s approved %s at %s, generation %d\n", identityOf(approver), state.Origin, state.ImageDigest, state.Generation)
	return nil
}

// identityOf names an approver the way a person reading the output would: the
// repository whose CI holds the identity, and the certificate subject when the
// certificate names no repository this version can read one from.
func identityOf(approver signature.Identity) string {
	if approver.Repo != "" {
		return approver.Repo
	}
	if approver.Subject != "" {
		return approver.Subject
	}
	return "an identity whose certificate names neither a repository nor a subject"
}
