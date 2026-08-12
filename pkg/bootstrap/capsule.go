/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package bootstrap

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
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

const (
	SchemaVersion = 1
	footerSize    = 16
	signatureSize = ed25519.SignatureSize
)

var (
	payloadMagic = [8]byte{'C', 'P', 'A', 'K', 'P', 'A', 'Y', '1'}
	capsuleMagic = [8]byte{'C', 'P', 'A', 'K', 'A', 'P', 'P', '1'}
)

type Metadata struct {
	Schema          int          `json:"schema"`
	Origin          string       `json:"origin"`
	Name            string       `json:"name"`
	Description     string       `json:"description"`
	IconSVG         string       `json:"icon_svg,omitempty"`
	Permissions     []Permission `json:"permissions,omitempty"`
	RefType         string       `json:"ref_type,omitempty"`
	Ref             string       `json:"ref,omitempty"`
	Arch            string       `json:"arch"`
	InstallerSHA256 string       `json:"installer_sha256"`
}

type Permission struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type Capsule struct {
	Metadata Metadata
	Payload  []byte
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
	switch m.RefType {
	case "":
		if m.Ref != "" {
			return errors.New("package reference type is missing")
		}
	case "branch", "release", "commit":
		if m.Ref == "" {
			return errors.New("package reference is missing")
		}
	default:
		return fmt.Errorf("unsupported package reference type: %s", m.RefType)
	}
	return nil
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
	capacity, err := checkedCapacity(len(installer), len(cpak), footerSize)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, capacity)
	result = append(result, installer...)
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
	return Capsule{Metadata: metadata, Payload: payload}, nil
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
		return 0, 0, errors.New("capsule footer is missing")
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
