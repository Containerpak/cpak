/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package catalog

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
)

func TestDecodeVerifiesAndSortsPackages(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	first := testMetadata("github.com/example/zeta", "Zeta")
	second := testMetadata("github.com/example/alpha", "Alpha")
	document := signedDocument(t, privateKey, first, second)

	packages, err := Decode(document, "amd64", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 || packages[0].Metadata.Name != "Alpha" || !packages[0].Installable {
		t.Fatalf("unexpected packages: %#v", packages)
	}
}

func TestDecodeRejectsChangedMetadata(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := signedDocument(t, privateKey, testMetadata("github.com/example/app", "App"))
	encoded = []byte(strings.Replace(string(encoded), "QXBw", "QmFk", 1))
	if _, err = Decode(encoded, "amd64", publicKey); err == nil {
		t.Fatal("expected changed catalog metadata to be rejected")
	}
}

func TestDecodeAcceptsLegacyMetadataForBrowsing(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	metadata := testMetadata("github.com/example/app", "App")
	metadata.Schema = 1
	metadata.ManifestDigest = ""
	encoded := signedDocument(t, privateKey, metadata)
	packages, err := Decode(encoded, "amd64", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || packages[0].Installable {
		t.Fatalf("legacy package should be browse-only: %#v", packages)
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := append(signedDocument(t, privateKey, testMetadata("github.com/example/app", "App")), []byte(` {}`)...)
	if _, err = Decode(encoded, "amd64", publicKey); err == nil {
		t.Fatal("expected trailing JSON to be rejected")
	}
}

func testMetadata(origin, name string) bootstrap.Metadata {
	return bootstrap.Metadata{
		Schema:          bootstrap.SchemaVersion,
		Origin:          origin,
		Name:            name,
		Description:     name + " description",
		Version:         "1.0.0",
		RefType:         "commit",
		Ref:             strings.Repeat("a", 40),
		ManifestDigest:  "sha256:" + strings.Repeat("b", 64),
		Arch:            "amd64",
		InstallerSHA256: strings.Repeat("c", 64),
	}
}

func signedDocument(t *testing.T, privateKey ed25519.PrivateKey, metadata ...bootstrap.Metadata) []byte {
	t.Helper()
	packages := map[string]map[string]signedEntry{}
	for _, item := range metadata {
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		packages[item.Origin] = map[string]signedEntry{
			"amd64": {
				Metadata:  base64.StdEncoding.EncodeToString(encoded),
				Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, encoded)),
			},
		}
	}
	result, err := json.Marshal(document{Schema: 1, Release: "v3.0.0", Packages: packages})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
