/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package bootstrap

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
)

// ErrCapsuleFooterMissing means the input is not a complete application installer.
var ErrCapsuleFooterMissing = errors.New("capsule footer is missing")

const (
	SchemaVersion = 2
	footerSize    = 16
	signatureSize = ed25519.SignatureSize
)

const installerPublicKeyBase64 = "pOCiCYoqrBX+5Laung0E5d/XysacWo3hYduW764U5o8="

var (
	payloadMagic   = [8]byte{'C', 'P', 'A', 'K', 'P', 'A', 'Y', '1'}
	companionMagic = [8]byte{'C', 'P', 'A', 'K', 'F', 'V', 'S', '1'}
	brandMagic     = [8]byte{'C', 'P', 'A', 'K', 'I', 'C', 'N', '1'}
	capsuleMagic   = [8]byte{'C', 'P', 'A', 'K', 'A', 'P', 'P', '1'}
)

type Metadata struct {
	Schema          int          `json:"schema"`
	Origin          string       `json:"origin"`
	Name            string       `json:"name"`
	Description     string       `json:"description"`
	Version         string       `json:"version,omitempty"`
	IconSVG         string       `json:"icon_svg,omitempty"`
	IconPNG         string       `json:"icon_png,omitempty"`
	Permissions     []Permission `json:"permissions,omitempty"`
	RefType         string       `json:"ref_type,omitempty"`
	Ref             string       `json:"ref,omitempty"`
	ManifestDigest  string       `json:"manifest_digest"`
	Arch            string       `json:"arch"`
	InstallerSHA256 string       `json:"installer_sha256"`
}

type Permission struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type Capsule struct {
	Metadata  Metadata
	Payload   []byte
	Companion []byte
	BrandIcon []byte
}

func InstallerPublicKey() (ed25519.PublicKey, error) {
	encoded, err := base64.StdEncoding.DecodeString(installerPublicKeyBase64)
	if err != nil {
		return nil, err
	}
	if len(encoded) != ed25519.PublicKeySize {
		return nil, errors.New("installer public key is invalid")
	}
	return ed25519.PublicKey(encoded), nil
}

func ParsePrivateKeyPEM(encoded []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(encoded)
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not Ed25519")
	}
	return privateKey, nil
}

func (m Metadata) Validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("unsupported capsule schema: %d", m.Schema)
	}
	parts := strings.Split(m.Origin, "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return fmt.Errorf("invalid package origin: %s", m.Origin)
	}
	if m.Name == "" || m.Description == "" {
		return errors.New("package name and description are required")
	}
	if m.Version != "" && !validMetadataText(m.Version, 80) {
		return errors.New("package version is invalid")
	}
	if len(m.Permissions) > 32 {
		return errors.New("package permission list is too long")
	}
	for _, permission := range m.Permissions {
		if !validMetadataText(permission.Name, 80) || !validMetadataText(permission.Detail, 160) {
			return errors.New("package permission is invalid")
		}
	}
	if m.Arch != "amd64" && m.Arch != "arm64" {
		return fmt.Errorf("unsupported installer architecture: %s", m.Arch)
	}
	digest, err := hex.DecodeString(m.InstallerSHA256)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("installer SHA-256 is invalid")
	}
	if m.RefType != "commit" {
		return errors.New("signed installers require an immutable commit reference")
	}
	commit, err := hex.DecodeString(m.Ref)
	if err != nil || len(commit) != 20 {
		return errors.New("signed installer commit is invalid")
	}
	if !validSHA256Reference(m.ManifestDigest) {
		return errors.New("signed installer manifest digest is invalid")
	}
	return nil
}

func validSHA256Reference(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(digest) == sha256.Size
}

func validMetadataText(value string, limit int) bool {
	runes := []rune(value)
	if len(runes) == 0 || len(runes) > limit {
		return false
	}
	for _, character := range runes {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func PackInstaller(installer, cpak []byte) ([]byte, error) {
	return PackInstallerWithCompanion(installer, cpak, nil)
}

func PackInstallerWithCompanion(installer, cpak, companion []byte) ([]byte, error) {
	return PackInstallerWithAssets(installer, cpak, companion, nil)
}

func PackInstallerWithAssets(installer, cpak, companion, brandIcon []byte) ([]byte, error) {
	capacity, err := checkedCapacity(len(installer), len(brandIcon), footerSize, len(companion), footerSize, len(cpak), footerSize)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, capacity)
	result = append(result, installer...)
	if len(brandIcon) > 0 {
		result = append(result, brandIcon...)
		result = appendFooter(result, brandMagic, uint64(len(brandIcon)))
	}
	if len(companion) > 0 {
		result = append(result, companion...)
		result = appendFooter(result, companionMagic, uint64(len(companion)))
	}
	result = append(result, cpak...)
	result = appendFooter(result, payloadMagic, uint64(len(cpak)))
	return result, nil
}

func SignCapsule(installer []byte, metadata Metadata, privateKey ed25519.PrivateKey) ([]byte, error) {
	digest := sha256.Sum256(installer)
	metadata.InstallerSHA256 = hex.EncodeToString(digest[:])
	encoded, signature, err := SignMetadata(metadata, privateKey)
	if err != nil {
		return nil, err
	}
	return AppendSignedMetadata(installer, encoded, signature)
}

func SignMetadata(metadata Metadata, privateKey ed25519.PrivateKey) ([]byte, []byte, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, nil, err
	}
	if err = metadata.Validate(); err != nil {
		return nil, nil, err
	}
	return encoded, ed25519.Sign(privateKey, encoded), nil
}

func AppendSignedMetadata(installer, metadata, signature []byte) ([]byte, error) {
	if len(signature) != signatureSize {
		return nil, fmt.Errorf("invalid signature length: %d", len(signature))
	}
	capacity, err := checkedCapacity(len(installer), len(metadata), signatureSize, footerSize)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, capacity)
	result = append(result, installer...)
	result = append(result, metadata...)
	result = append(result, signature...)
	result = appendFooter(result, capsuleMagic, uint64(len(metadata)))
	return result, nil
}

func checkedCapacity(parts ...int) (int, error) {
	capacity := 0
	maximum := int(^uint(0) >> 1)
	for _, part := range parts {
		if part < 0 || part > maximum-capacity {
			return 0, errors.New("capsule size exceeds the platform limit")
		}
		capacity += part
	}
	return capacity, nil
}

func ReadCapsule(source io.ReaderAt, size int64, publicKey ed25519.PublicKey) (Capsule, error) {
	metadataOffset, metadataLength, err := section(source, size, capsuleMagic, signatureSize)
	if err != nil {
		return Capsule{}, err
	}
	encoded := make([]byte, metadataLength)
	if _, err = source.ReadAt(encoded, metadataOffset); err != nil {
		return Capsule{}, err
	}
	signature := make([]byte, signatureSize)
	if _, err = source.ReadAt(signature, metadataOffset+int64(metadataLength)); err != nil {
		return Capsule{}, err
	}
	if !ed25519.Verify(publicKey, encoded, signature) {
		return Capsule{}, errors.New("invalid capsule signature")
	}

	var metadata Metadata
	if err = json.Unmarshal(encoded, &metadata); err != nil {
		return Capsule{}, fmt.Errorf("decode capsule metadata: %w", err)
	}
	if err = metadata.Validate(); err != nil {
		return Capsule{}, err
	}
	if metadata.Arch != runtime.GOARCH {
		return Capsule{}, fmt.Errorf("installer architecture is %s, package requires %s", runtime.GOARCH, metadata.Arch)
	}

	baseSize := metadataOffset
	expectedDigest, _ := hex.DecodeString(metadata.InstallerSHA256)
	hasher := sha256.New()
	if _, err = io.Copy(hasher, io.NewSectionReader(source, 0, baseSize)); err != nil {
		return Capsule{}, fmt.Errorf("hash installer: %w", err)
	}
	if subtle.ConstantTimeCompare(hasher.Sum(nil), expectedDigest) != 1 {
		return Capsule{}, errors.New("installer SHA-256 does not match signed metadata")
	}
	payloadOffset, payloadLength, err := section(source, baseSize, payloadMagic, 0)
	if err != nil {
		return Capsule{}, fmt.Errorf("read embedded cpak: %w", err)
	}
	payload := make([]byte, payloadLength)
	if _, err = source.ReadAt(payload, payloadOffset); err != nil {
		return Capsule{}, err
	}
	companionOffset, companionLength, found, err := optionalSection(source, payloadOffset, companionMagic)
	if err != nil {
		return Capsule{}, fmt.Errorf("read embedded storage service: %w", err)
	}
	var companion []byte
	if found {
		companion = make([]byte, companionLength)
		if _, err = source.ReadAt(companion, companionOffset); err != nil {
			return Capsule{}, err
		}
	}
	brandSearchOffset := payloadOffset
	if found {
		brandSearchOffset = companionOffset
	}
	brandOffset, brandLength, brandFound, err := optionalSection(source, brandSearchOffset, brandMagic)
	if err != nil {
		return Capsule{}, fmt.Errorf("read embedded brand icon: %w", err)
	}
	var brandIcon []byte
	if brandFound {
		brandIcon = make([]byte, brandLength)
		if _, err = source.ReadAt(brandIcon, brandOffset); err != nil {
			return Capsule{}, err
		}
	}
	return Capsule{Metadata: metadata, Payload: payload, Companion: companion, BrandIcon: brandIcon}, nil
}

func optionalSection(source io.ReaderAt, size int64, magic [8]byte) (int64, int, bool, error) {
	if size < footerSize {
		return 0, 0, false, nil
	}
	footer := make([]byte, footerSize)
	if _, err := source.ReadAt(footer, size-footerSize); err != nil {
		return 0, 0, false, err
	}
	if string(footer[:8]) != string(magic[:]) {
		return 0, 0, false, nil
	}
	offset, length, err := section(source, size, magic, 0)
	return offset, length, true, err
}

func section(source io.ReaderAt, size int64, magic [8]byte, trailing int) (int64, int, error) {
	if size < footerSize+int64(trailing) {
		return 0, 0, errors.New("capsule is truncated")
	}
	footer := make([]byte, footerSize)
	if _, err := source.ReadAt(footer, size-footerSize); err != nil {
		return 0, 0, err
	}
	if string(footer[:8]) != string(magic[:]) {
		return 0, 0, ErrCapsuleFooterMissing
	}
	length := binary.LittleEndian.Uint64(footer[8:])
	if length > uint64(size-footerSize-int64(trailing)) {
		return 0, 0, errors.New("capsule section length is invalid")
	}
	offset := size - footerSize - int64(trailing) - int64(length)
	return offset, int(length), nil
}

func appendFooter(target []byte, magic [8]byte, length uint64) []byte {
	target = append(target, magic[:]...)
	encoded := make([]byte, 8)
	binary.LittleEndian.PutUint64(encoded, length)
	return append(target, encoded...)
}
