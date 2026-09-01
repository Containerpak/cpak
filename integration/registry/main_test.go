/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/oci"
)

func TestRegistryServesResolvableImages(t *testing.T) {
	built, err := buildImage([]layerFile{{path: "probe", mode: 0755, data: []byte("probe")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry{images: map[string]image{"probe": built}})
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/probe:latest"
	client := oci.Client{}
	resolved, err := client.Resolve(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Layers) != 1 {
		t.Fatalf("resolved layers: got %d, want 1", len(resolved.Layers))
	}
	blob, err := client.Blob(context.Background(), resolved.Reference, resolved.Layers[0])
	if err != nil {
		t.Fatal(err)
	}
	defer blob.Close()
	data, err := io.ReadAll(blob)
	if err != nil {
		t.Fatal(err)
	}
	if digest(data) != resolved.Layers[0].Digest {
		t.Fatal("the served layer does not match its descriptor")
	}
}
