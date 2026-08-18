/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package integrity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// bindingLimit bounds what is read back. A record is three short strings, so
// anything larger than this was not written by us.
const bindingLimit = 4096

var (
	errInvalidLayerDigest = errors.New("integrity: layer digest is not a sha256 reference")
	errBindingConflict    = errors.New("integrity: layer is already bound to another state")
)

// DirectoryBindings keeps one record per layer under a single directory. A
// record is written once and never rewritten: the link between a registry
// digest and a store state is provable only at the instant the download that
// produced it was verified, so a second, different answer for the same digest
// is a disagreement about what the layer is, not a newer truth.
type DirectoryBindings struct {
	directory string
}

var _ Bindings = (*DirectoryBindings)(nil)

// NewDirectoryBindings prepares the directory the records live in.
func NewDirectoryBindings(directory string) (*DirectoryBindings, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("integrity: bindings directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("integrity: prepare bindings directory: %w", err)
	}
	return &DirectoryBindings{directory: directory}, nil
}

// Bind records the state a layer produced. Recording a binding that is already
// on disk says nothing new and is accepted; recording a different state for a
// digest that is already bound is refused, because one of the two answers is
// not the layer the registry named.
func (b *DirectoryBindings) Bind(binding LayerBinding) error {
	digest, err := canonicalLayerDigest(binding.OCIDigest)
	if err != nil {
		return err
	}
	if binding.StateID == "" || binding.StateRoot == "" {
		return fmt.Errorf("%w: %s", errUnboundLayer, digest)
	}
	binding.OCIDigest = digest
	staged, err := b.stage(binding)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	// A hard link publishes the whole record in one step and refuses to replace
	// a name that exists, so a concurrent pull of the same layer is told about
	// the disagreement instead of quietly losing to it.
	err = os.Link(staged, b.path(digest))
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("integrity: publish layer binding: %w", err)
	}
	existing, found, err := b.Lookup(digest)
	if err != nil {
		return err
	}
	if !found || existing != binding {
		return fmt.Errorf("%w: %s", errBindingConflict, digest)
	}
	return nil
}

// Lookup answers with the binding recorded for a layer. A record that does not
// name the layer it is filed under is refused rather than returned, so moving a
// record cannot make it answer for another layer.
func (b *DirectoryBindings) Lookup(ociDigest string) (LayerBinding, bool, error) {
	digest, err := canonicalLayerDigest(ociDigest)
	if err != nil {
		return LayerBinding{}, false, err
	}
	data, err := os.ReadFile(b.path(digest))
	if errors.Is(err, fs.ErrNotExist) {
		return LayerBinding{}, false, nil
	}
	if err != nil {
		return LayerBinding{}, false, fmt.Errorf("integrity: read layer binding: %w", err)
	}
	if len(data) > bindingLimit {
		return LayerBinding{}, false, fmt.Errorf("integrity: layer binding %s is too large", digest)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var binding LayerBinding
	if err = decoder.Decode(&binding); err != nil {
		return LayerBinding{}, false, fmt.Errorf("integrity: decode layer binding %s: %w", digest, err)
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return LayerBinding{}, false, fmt.Errorf("integrity: layer binding %s carries more than one record", digest)
	}
	if binding.OCIDigest != digest {
		return LayerBinding{}, false, fmt.Errorf("integrity: layer binding %s does not name its own layer", digest)
	}
	if binding.StateID == "" || binding.StateRoot == "" {
		return LayerBinding{}, false, fmt.Errorf("%w: %s", errUnboundLayer, digest)
	}
	return binding, true, nil
}

// stage writes the record beside its destination, so that publishing it is a
// link within one directory and never a copy across filesystems.
func (b *DirectoryBindings) stage(binding LayerBinding) (string, error) {
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", fmt.Errorf("integrity: encode layer binding: %w", err)
	}
	file, err := os.CreateTemp(b.directory, ".binding-")
	if err != nil {
		return "", fmt.Errorf("integrity: stage layer binding: %w", err)
	}
	if err = writeStaged(file, append(encoded, '\n')); err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("integrity: stage layer binding: %w", err)
	}
	return file.Name(), nil
}

func writeStaged(file *os.File, data []byte) error {
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// path is built from the canonical digest alone, which is sixty-four
// hexadecimal characters, so no reference a caller supplies can name a file
// outside the directory.
func (b *DirectoryBindings) path(digest string) string {
	return filepath.Join(b.directory, digest+".json")
}

// canonicalLayerDigest reduces a layer reference to its bare hexadecimal form.
// The prefixed and the bare spelling reach the same record, so one layer is
// never bound twice under two names for the same digest.
func canonicalLayerDigest(reference string) (string, error) {
	digest := strings.TrimPrefix(reference, "sha256:")
	if len(digest) != 64 {
		return "", fmt.Errorf("%w: %q", errInvalidLayerDigest, reference)
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", fmt.Errorf("%w: %q", errInvalidLayerDigest, reference)
		}
	}
	return digest, nil
}
