/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// referrersLimit bounds the index of what is attached to one subject. It is
// generous for a list of descriptors and small enough that a registry cannot
// choose how much a client reads before it has agreed to read anything.
const referrersLimit = 4 << 20

// Referrers lists the artifacts a registry holds against one subject manifest.
//
// The subject is a digest and never a tag, because what is attached is
// attached to an immutable manifest: asking about a tag would ask the registry
// to resolve it again, which is a different question from the one the caller
// already has an answer to.
//
// An empty list is an answer and not a failure. A registry that implements the
// API and holds nothing answers with an empty index, and a registry that does
// not implement it answers 404; the two are not distinguishable from here, so
// both are reported as nothing attached and a caller that reads that as proof
// of absence is claiming more than the registry said.
func (c *Client) Referrers(ctx context.Context, ref Reference, subject, artifactType string) ([]Descriptor, error) {
	if !digestPattern.MatchString(subject) {
		return nil, fmt.Errorf("oci: invalid referrers subject %q", subject)
	}
	path := "/v2/" + ref.Repository + "/referrers/" + subject
	if artifactType != "" {
		path += "?artifactType=" + url.QueryEscape(artifactType)
	}
	response, err := c.request(ctx, ref, http.MethodGet, path, mediaOCIIndex)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		// A registry that does not implement the API is where the
		// specification's fallback tag applies, and a publisher that met one
		// wrote the index there.
		return c.fallbackReferrers(ctx, ref, subject, artifactType)
	}
	if response.StatusCode != http.StatusOK {
		return nil, responseError("list referrers", response)
	}
	body, err := readBoundedResponse(response.Body, referrersLimit, "referrers")
	if err != nil {
		return nil, err
	}
	var index imageManifest
	if err = json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("oci: decode referrers: %w", err)
	}
	referrers := make([]Descriptor, 0, len(index.Manifests))
	for _, descriptor := range index.Manifests {
		if !validDescriptor(descriptor) {
			return nil, fmt.Errorf("oci: invalid referrer descriptor")
		}
		// The filter is applied again on this side because artifactType is a
		// request the registry is free to ignore, and an answer it did not
		// filter looks exactly like one it did.
		if artifactType != "" && descriptor.ArtifactType != artifactType {
			continue
		}
		referrers = append(referrers, descriptor)
	}
	if len(referrers) == 0 {
		// An empty answer from a registry that implements the API and an empty
		// answer from one that only pretends to look the same from here, so
		// the tag is consulted before reporting that nothing is attached.
		return c.fallbackReferrers(ctx, ref, subject, artifactType)
	}
	return referrers, nil
}

// fallbackReferrers reads the index a publisher keeps under the tag reserved
// for subjects whose registry does not index referrers. Nothing there is an
// answer of nothing attached, exactly as an empty index from the API is.
func (c *Client) fallbackReferrers(ctx context.Context, ref Reference, subject, artifactType string) ([]Descriptor, error) {
	tag := strings.Replace(subject, ":", "-", 1)
	body, _, _, err := c.fetchManifest(ctx, ref, tag)
	if err != nil {
		// The tag is optional, so a subject that has none is not a failure to
		// report over a signature that was simply never published this way.
		return nil, nil
	}
	var index imageManifest
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("oci: decode the referrers index at %s: %w", tag, err)
	}
	referrers := make([]Descriptor, 0, len(index.Manifests))
	for _, descriptor := range index.Manifests {
		if !validDescriptor(descriptor) {
			return nil, fmt.Errorf("oci: invalid referrer descriptor")
		}
		if artifactType != "" && descriptor.ArtifactType != artifactType {
			continue
		}
		referrers = append(referrers, descriptor)
	}
	return referrers, nil
}

// ReferrerPayload reads the one blob a referring artifact is made of.
//
// An artifact carrying anything other than a single layer is refused rather
// than guessed at: which of several blobs is the payload is not something the
// registry states, and choosing one would be inventing it.
func (c *Client) ReferrerPayload(ctx context.Context, ref Reference, referrer Descriptor, limit int64) ([]byte, error) {
	if !validDescriptor(referrer) {
		return nil, fmt.Errorf("oci: invalid referrer descriptor")
	}
	body, _, mediaType, err := c.fetchManifest(ctx, ref, referrer.Digest)
	if err != nil {
		return nil, err
	}
	if mediaType != mediaOCIManifest {
		return nil, fmt.Errorf("oci: unsupported referrer media type %q", mediaType)
	}
	var manifest imageManifest
	if err = json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("oci: decode referrer manifest: %w", err)
	}
	if len(manifest.Layers) != 1 {
		return nil, fmt.Errorf("oci: referrer carries %d layers, expected one", len(manifest.Layers))
	}
	payload := manifest.Layers[0]
	if !validDescriptor(payload) {
		return nil, fmt.Errorf("oci: invalid referrer payload descriptor")
	}
	if limit > 0 && payload.Size > limit {
		return nil, fmt.Errorf("oci: referrer payload is too large")
	}
	reader, err := c.Blob(ctx, ref, payload)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, payload.Size+1))
	if err != nil {
		return nil, fmt.Errorf("oci: read referrer payload: %w", err)
	}
	if int64(len(content)) != payload.Size || digestBytes(content) != payload.Digest {
		return nil, fmt.Errorf("oci: referrer payload digest mismatch")
	}
	return content, nil
}
