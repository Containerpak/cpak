/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package signature

import (
	_ "embed"
	"fmt"
	"sync"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// trustedRootJSON is the sigstore public good trust root, shipped inside cpak
// so that verification needs nothing but the bundle and this file.
//
// It is the TUF target trusted_root.json from tuf-repo-cdn.sigstore.dev,
// fetched through the TUF client and copied here byte for byte. Refresh it with
// a cpak release, the way any other pinned trust material is refreshed: a
// signature made against a key sigstore has since rotated out has to keep
// verifying on a machine that has not updated yet, and a machine that has
// updated has to see the new keys.
//
//go:embed trustroot/trusted_root.json
var trustedRootJSON []byte

var (
	trustRootOnce sync.Once
	trustRoot     *root.TrustedRoot
	trustRootErr  error
)

// bundledTrustRoot parses the shipped trust root, once. A failure here is a
// broken cpak build and not a bad signature, so it is never folded into
// ErrUntrusted: an installation must be able to tell a package that cannot be
// trusted from a cpak that cannot check.
func bundledTrustRoot() (root.TrustedMaterial, error) {
	trustRootOnce.Do(func() {
		trustRoot, trustRootErr = root.NewTrustedRootFromJSON(trustedRootJSON)
	})
	if trustRootErr != nil {
		return nil, fmt.Errorf("signature: read the bundled trust root: %w", trustRootErr)
	}
	return trustRoot, nil
}
