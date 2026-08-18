/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// A registry that does not implement the referrers API stores the manifest and
// answers nothing for it, so a signature published only that way is one nobody
// will ever be served. The OCI distribution specification defines the way out:
// the client keeps the index itself, under a tag derived from the subject
// digest, and readers that get nothing from the API look there.
//
// This is not a lesser signature. The same bytes are signed and the same bundle
// is stored; only the path a reader walks to find it differs.
type fallbackIndex struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []descriptor `json:"manifests"`
}

// fallbackTag is the subject digest with its separator replaced, which is the
// tag the specification reserves for this.
func fallbackTag(subject string) string {
	return strings.Replace(subject, ":", "-", 1)
}

// publishFallbackIndex adds one referrer to the index kept under the fallback
// tag. An index that is already there is read first, so that publishing a
// signature does not remove an approval published beside it, and a referrer
// already listed is replaced rather than duplicated.
func publishFallbackIndex(ctx context.Context, r *registry, subject string, referrer descriptor, artifactType string) error {
	tag := fallbackTag(subject)
	index := fallbackIndex{SchemaVersion: 2, MediaType: indexMediaType}
	existing, err := r.fetchManifestByTag(ctx, tag)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &index); err != nil {
			return fmt.Errorf("read the referrers index at %s: %w", tag, err)
		}
		index.SchemaVersion = 2
		index.MediaType = indexMediaType
	}
	referrer.ArtifactType = artifactType
	kept := make([]descriptor, 0, len(index.Manifests)+1)
	for _, held := range index.Manifests {
		if held.Digest == referrer.Digest {
			continue
		}
		// One artifact type answers for one thing, so a newer signature
		// replaces the older one instead of both being served.
		if held.ArtifactType == artifactType {
			continue
		}
		kept = append(kept, held)
	}
	index.Manifests = append(kept, referrer)
	encoded, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode the referrers index: %w", err)
	}
	return r.pushManifestTag(ctx, tag, encoded, indexMediaType)
}

// fetchManifestByTag answers with nothing when the tag does not exist, which is
// the ordinary case of the first artifact attached to an image.
func (r *registry) fetchManifestByTag(ctx context.Context, tag string) ([]byte, error) {
	response, err := r.send(ctx, http.MethodGet, r.repository("/manifests/"+tag), nil, map[string]string{"Accept": manifestAccept})
	if err != nil {
		return nil, err
	}
	defer closeResponse(response)
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, responseError("read "+tag, response)
	}
	return readBounded(response.Body, 4<<20)
}

func (r *registry) pushManifestTag(ctx context.Context, tag string, content []byte, mediaType string) error {
	response, err := r.send(ctx, http.MethodPut, r.repository("/manifests/"+tag), content, map[string]string{"Content-Type": mediaType})
	if err != nil {
		return err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return responseError("push "+tag, response)
	}
	return nil
}
