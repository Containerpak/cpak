/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package bootstrap

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCapsuleRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := testPackInstaller(t)
	metadata := testMetadata()
	digest := sha256.Sum256(base)
	metadata.InstallerSHA256 = hex.EncodeToString(digest[:])
	packed, err := SignCapsule(base, metadata, privateKey)
	if err != nil {
		t.Fatal(err)
	}

	capsule, err := ReadCapsule(bytes.NewReader(packed), int64(len(packed)), publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capsule.Metadata, metadata) {
		t.Fatalf("metadata mismatch: %#v", capsule.Metadata)
	}
	if string(capsule.Payload) != "cpak" {
		t.Fatalf("payload mismatch: %q", capsule.Payload)
	}
}

func TestMetadataRejectsInvalidPermissions(t *testing.T) {
	metadata := testMetadata()
	metadata.Permissions = []Permission{{Name: "Files", Detail: "home\nread-write"}}
	if err := metadata.Validate(); err == nil {
		t.Fatal("metadata accepted a permission containing a newline")
	}
}

func TestCapsuleRejectsTamperedMetadata(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := SignCapsule(testPackInstaller(t), testMetadata(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	index := bytes.Index(packed, []byte("Bottles"))
	if index < 0 {
		t.Fatal("metadata marker not found")
	}
	packed[index] = 'X'

	_, err = ReadCapsule(bytes.NewReader(packed), int64(len(packed)), publicKey)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature error, got %v", err)
	}
}

func TestCapsuleRejectsTamperedPayload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := SignCapsule(testPackInstaller(t), testMetadata(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	index := bytes.Index(packed, []byte("cpak"))
	if index < 0 {
		t.Fatal("payload marker not found")
	}
	packed[index] = 'X'

	_, err = ReadCapsule(bytes.NewReader(packed), int64(len(packed)), publicKey)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected installer digest error, got %v", err)
	}
}

func TestCapsuleRejectsTruncatedInput(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadCapsule(bytes.NewReader([]byte("short")), 5, publicKey)
	if err == nil {
		t.Fatal("expected truncated capsule error")
	}
}

func TestAppendSignedMetadataRejectsInvalidSignatureLength(t *testing.T) {
	_, err := AppendSignedMetadata([]byte("installer"), []byte("{}"), []byte("short"))
	if err == nil || !strings.Contains(err.Error(), "signature length") {
		t.Fatalf("expected signature length error, got %v", err)
	}
}

func TestCapsuleRejectsWrongArchitecture(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := testMetadata()
	if runtime.GOARCH == "amd64" {
		metadata.Arch = "arm64"
	} else {
		metadata.Arch = "amd64"
	}
	packed, err := SignCapsule(testPackInstaller(t), metadata, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadCapsule(bytes.NewReader(packed), int64(len(packed)), publicKey)
	if err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("expected architecture error, got %v", err)
	}
}

func TestCheckedCapacityRejectsOverflow(t *testing.T) {
	maximum := int(^uint(0) >> 1)
	if _, err := checkedCapacity(maximum, 1); err == nil {
		t.Fatal("overflowing capacity was accepted")
	}
}

func testPackInstaller(t *testing.T) []byte {
	t.Helper()
	packed, err := PackInstaller([]byte("installer"), []byte("cpak"))
	if err != nil {
		t.Fatal(err)
	}
	return packed
}

func testMetadata() Metadata {
	return Metadata{
		Schema:      SchemaVersion,
		Origin:      "github.com/bottlesdevs/bottles",
		Name:        "Bottles",
		Description: "Run Windows software on Linux.",
		RefType:     "branch",
		Ref:         "main",
		Arch:        runtime.GOARCH,
	}
}
