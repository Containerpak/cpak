/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package oci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const probeArtifactType = "application/vnd.cpak.signature.v1+json"

// ghcr stores a referring manifest and answers 404 for the referrers API, which
// is the registry every cpak package is published to. A reader that stopped
// there would report that nothing is signed, which is the one wrong answer:
// the signature exists and would simply never be served.
func TestReferrersFallBackToTheIndexTagWhenTheAPIAnswersNothing(t *testing.T) {
	subject := "sha256:" + strings.Repeat("a", 64)
	referrerDigest := "sha256:" + strings.Repeat("b", 64)
	index, _ := json.Marshal(imageManifest{
		SchemaVersion: 2,
		MediaType:     mediaOCIIndex,
		Manifests: []Descriptor{{
			MediaType:    mediaOCIManifest,
			Digest:       referrerDigest,
			Size:         42,
			ArtifactType: probeArtifactType,
		}},
	})
	tag := strings.Replace(subject, ":", "-", 1)
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		asked = append(asked, request.URL.Path)
		switch request.URL.Path {
		case "/v2/example/app/referrers/" + subject:
			http.NotFound(writer, request)
		case "/v2/example/app/manifests/" + tag:
			writer.Header().Set("Content-Type", mediaOCIIndex)
			writer.Header().Set("Docker-Content-Digest", digestBytes(index))
			_, _ = writer.Write(index)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	ref, err := ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}
	found, err := (&Client{}).Referrers(context.Background(), ref, subject, probeArtifactType)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Digest != referrerDigest {
		t.Fatalf("the signature under the fallback tag was not found: %+v", found)
	}
	if !strings.Contains(strings.Join(asked, " "), "/referrers/") {
		t.Fatal("the referrers API was never asked, so the fallback is not a fallback")
	}
}

// A subject with nothing attached anywhere must read as nothing attached, not
// as an error, or every unsigned package becomes a failure.
func TestReferrersReportNothingWhenNeitherTheAPINorTheTagHolds(t *testing.T) {
	subject := "sha256:" + strings.Repeat("c", 64)
	server := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer server.Close()

	ref, err := ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}
	found, err := (&Client{}).Referrers(context.Background(), ref, subject, probeArtifactType)
	if err != nil {
		t.Fatalf("a package nobody signed was reported as an error: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("something was found where nothing is published: %+v", found)
	}
}
