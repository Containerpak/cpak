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

func TestPrivateRegistryRequiresExactCredentials(t *testing.T) {
	built, err := buildImage([]layerFile{{path: "probe", mode: 0755, data: []byte("probe")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry{images: map[string]image{privateRepository: built}})
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/" + privateRepository + ":latest"
	wrong := oci.Client{Credentials: staticCredential{username: privateUsername, password: "wrong"}}
	if _, err = wrong.Resolve(context.Background(), reference); err == nil {
		t.Fatal("private image accepted the wrong credential")
	}
	client := oci.Client{Credentials: staticCredential{username: privateUsername, password: privatePassword}}
	if _, err = client.Resolve(context.Background(), reference); err != nil {
		t.Fatalf("private image rejected the bound credential: %v", err)
	}
}

type staticCredential struct {
	username string
	password string
}

func (c staticCredential) Credential(context.Context, oci.Reference) (oci.Credential, error) {
	return oci.Credential{Username: c.username, Password: c.password}, nil
}
