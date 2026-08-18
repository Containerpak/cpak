/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
)

func writePrivateKey(t *testing.T, directory string) (ed25519.PublicKey, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a signing key failed: %v", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("encoding the signing key failed: %v", err)
	}
	path := filepath.Join(directory, "key.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600); err != nil {
		t.Fatalf("writing the signing key failed: %v", err)
	}
	return publicKey, path
}

func TestCapsuleSigningReadsBackAndOnlyWithTheKeyThatSignedIt(t *testing.T) {
	directory := t.TempDir()
	publicKey, keyPath := writePrivateKey(t, directory)
	packed, err := bootstrap.PackInstaller([]byte("installer"), []byte("cpak"))
	if err != nil {
		t.Fatalf("packing the installer fixture failed: %v", err)
	}
	installerPath := filepath.Join(directory, "installer")
	if err = os.WriteFile(installerPath, packed, 0o755); err != nil {
		t.Fatalf("writing the installer fixture failed: %v", err)
	}
	output := filepath.Join(directory, "capsule")

	if err = signCapsule([]string{
		"--installer", installerPath,
		"--private-key", keyPath,
		"--output", output,
		"--origin", testOrigin,
		"--name", "Example",
		"--description", "An example package",
	}); err != nil {
		t.Fatalf("signing the capsule failed: %v", err)
	}

	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("reading the capsule failed: %v", err)
	}
	capsule, err := bootstrap.ReadCapsule(bytes.NewReader(content), int64(len(content)), publicKey)
	if err != nil {
		t.Fatalf("the signed capsule does not read back: %v", err)
	}
	if capsule.Metadata.Origin != testOrigin || string(capsule.Payload) != "cpak" {
		t.Fatalf("the capsule carries %s and a payload of %q", capsule.Metadata.Origin, capsule.Payload)
	}

	stranger, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a second key failed: %v", err)
	}
	if _, err = bootstrap.ReadCapsule(bytes.NewReader(content), int64(len(content)), stranger); err == nil {
		t.Fatalf("the capsule read back under a key that never signed it")
	}
}

func TestCapsuleRequiresAnInstallerAKeyAndAnOutput(t *testing.T) {
	if err := signCapsule([]string{"--origin", testOrigin}); err == nil {
		t.Fatalf("a capsule was signed without an installer, a key or an output")
	}
}
