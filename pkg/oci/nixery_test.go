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
	"sync"
	"testing"
)

func TestNixeryReferencesSelectTheRequestedArchitecture(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		ref, err := NixeryReference("", []string{"shell", "qt6.qtbase"}, arch)
		expected := "nixery.dev/shell/qt6.qtbase:latest"
		if arch == "arm64" {
			expected = "nixery.dev/arm64/shell/qt6.qtbase:latest"
		}
		if err != nil || ref.Name() != expected {
			t.Fatalf("%s: %s %v", arch, ref.Name(), err)
		}
	}
	for _, packages := range [][]string{nil, {""}, {"../shell"}, {"shell/hello"}, {"shell:latest"}, {"shell@sha256:abc"}, {"arm64"}, {"Shell"}, {"hello\n"}} {
		if _, err := NixeryReference("", packages, "amd64"); err == nil {
			t.Fatalf("invalid package set accepted: %q", packages)
		}
	}
	for _, registry := range []string{"https://nixery.dev", "nixery.dev/extra", "nixery.dev@sha256:bad", "docker.io"} {
		if _, err := NixeryReference(registry, []string{"hello"}, "amd64"); err == nil {
			t.Fatalf("invalid registry accepted: %s", registry)
		}
	}
	if _, err := NixeryReference("", []string{"hello"}, "riscv64"); err == nil {
		t.Fatal("unsupported Nixery architecture accepted")
	}
}

func TestImagePinVerifiesTargetConfigAndImmutableRetrieval(t *testing.T) {
	bodies := map[string][]byte{}
	descriptors := []Descriptor{}
	digests := map[string]string{}
	for _, arch := range []string{"amd64", "arm64"} {
		config, _ := json.Marshal(ConfigFile{OS: "linux", Architecture: arch})
		configDigest := digestBytes(config)
		manifest, _ := json.Marshal(imageManifest{SchemaVersion: 2, MediaType: mediaOCIManifest, Config: Descriptor{Digest: configDigest, Size: int64(len(config))}})
		digest := digestBytes(manifest)
		bodies["/blobs/"+configDigest] = config
		bodies["/manifests/"+digest] = manifest
		descriptors = append(descriptors, Descriptor{Digest: digest, Size: int64(len(manifest)), MediaType: mediaOCIManifest, Platform: Platform{OS: "linux", Architecture: arch}})
		digests[arch] = digest
	}
	index, _ := json.Marshal(imageManifest{SchemaVersion: 2, MediaType: mediaOCIIndex, Manifests: descriptors})
	bodies["/manifests/latest"] = index
	serveDigest := true
	var mu sync.RWMutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		path := strings.TrimPrefix(r.URL.Path, "/v2/shell/hello")
		if !serveDigest && strings.HasPrefix(path, "/manifests/sha256:") {
			http.NotFound(w, r)
			return
		}
		body, ok := bodies[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		var metadata struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(body, &metadata)
		w.Header().Set("Content-Type", metadata.MediaType)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	source := strings.TrimPrefix(server.URL, "http://") + "/shell/hello"
	client := &Client{}
	pinned, err := client.Pin(context.Background(), source, Platform{OS: "linux", Architecture: "arm64"})
	if err != nil || pinned != source+"@"+digests["arm64"] {
		t.Fatalf("cross-architecture pin: %s %v", pinned, err)
	}
	if _, err = client.Pin(context.Background(), source+"@"+digests["amd64"], Platform{OS: "linux", Architecture: "arm64"}); err == nil {
		t.Fatal("amd64 config accepted as an arm64 image")
	}
	if _, err = client.Pin(context.Background(), source, Platform{OS: "linux", Architecture: "riscv64"}); err == nil {
		t.Fatal("missing target architecture accepted")
	}
	mu.Lock()
	serveDigest = false
	// A single-image registry may resolve latest but still refuse digest pulls.
	bodies["/manifests/latest"] = bodies["/manifests/"+digests["amd64"]]
	mu.Unlock()
	if _, err = client.Pin(context.Background(), source, Platform{OS: "linux", Architecture: "amd64"}); err == nil || !strings.Contains(err.Error(), "retrieve resolved image by digest") {
		t.Fatalf("unretrievable image pin: %v", err)
	}
}
