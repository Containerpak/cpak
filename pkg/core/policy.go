/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"bytes"
	"encoding/json"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// PolicySource names who decided the policy an application runs under, before
// the host had its say.
type PolicySource string

const (
	// PolicyFromManifest means the author asked and nobody narrowed it.
	PolicyFromManifest PolicySource = "manifest"

	// PolicyFromUser means the owner of the installation decided, and their
	// decision replaces the manifest rather than adding to it.
	PolicyFromUser PolicySource = "user"
)

// EffectiveOverride answers the policy an application actually runs under, and
// which side asked for it.
//
// There are three parties and they do not all work the same way. The manifest
// asks. The owner of the installation replaces that request outright when they
// have written an override, which is why a policy denying everything reads as
// a decision rather than as silence. The administrator's ceiling then narrows
// whatever survived, and cannot widen it: nothing here can grant a permission
// that neither the author nor the owner asked for.
func EffectiveOverride(manifest types.Override, user *types.Override, ceiling Ceiling, h Host) (types.Override, PolicySource) {
	requested := manifest
	source := PolicyFromManifest
	if user != nil {
		requested = *user
		source = PolicyFromUser
	}
	return UnderCeiling(requested, ceiling, h), source
}

// DecodeCeiling reads a ceiling file.
//
// It reads the bytes twice on purpose. Once for the values, and once for the
// keys that are actually in it, which is what decides how far the ceiling
// reaches: the decoded struct cannot tell a permission set to false apart from
// one nobody wrote.
func DecodeCeiling(data []byte) (Ceiling, error) {
	var policy types.Override
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return Ceiling{}, err
	}
	if err := types.ValidateFilesystemPermissions(policy.Filesystem); err != nil {
		return Ceiling{}, err
	}
	var written map[string]json.RawMessage
	if err := json.Unmarshal(data, &written); err != nil {
		return Ceiling{}, err
	}
	named := make(map[string]bool, len(written))
	for key := range written {
		named[key] = true
	}
	return Ceiling{Present: true, Policy: policy, Named: named}, nil
}
