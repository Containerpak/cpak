/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

// VerifySignatureCmd checks one bundle against one state by hand. It exists so
// that the format can be exercised with cosign before anything in cpak calls
// it: --print-canonical writes the exact bytes a publisher signs, and the same
// command then verifies whatever cosign made of them.
//
// It reads two files and writes none.
type VerifySignatureCmd struct {
	Bundle         string `arg:"bundle" help:"Path to the sigstore bundle attached to the image"`
	State          string `cli:"state" help:"Path to a JSON file holding the signed state"`
	Origin         string `cli:"origin" help:"Package origin, as host/owner/repository"`
	ManifestSHA256 string `cli:"manifest-sha256" help:"SHA-256 of the manifest the package is configured by"`
	ImageDigest    string `cli:"image-digest" help:"Digest the image reference resolved to, as sha256:..."`
	LockSHA256     string `cli:"lock-sha256" help:"SHA-256 of the lock file, when the package has one"`
	Generation     int    `cli:"generation" help:"Generation of the signed state, starting at 1"`
	PrintCanonical bool   `cli:"print-canonical" help:"Write the exact bytes a publisher signs to standard output and stop"`
	AnyIdentity    bool   `cli:"any-identity" help:"Report who signed without requiring that they may speak for the origin"`

	cli.Base
}

func (c *VerifySignatureCmd) Run() error {
	state, err := c.signedState()
	if err != nil {
		return err
	}
	if c.PrintCanonical {
		canonical, canonicalErr := state.Canonical()
		if canonicalErr != nil {
			return canonicalErr
		}
		_, writeErr := os.Stdout.Write(canonical)
		return writeErr
	}
	if c.Bundle == "" {
		return errors.New("a bundle is needed to verify anything: pass the path to the sigstore bundle")
	}
	bundleJSON, err := os.ReadFile(c.Bundle)
	if err != nil {
		return err
	}
	verified, err := signature.Verify(bundleJSON, state)
	if err != nil {
		return err
	}
	c.reportSigner(verified)
	return c.reportOrigin(verified, state.Origin)
}

// signedState reads the state either from the file a publisher signed or from
// the flags. Both at once is refused rather than merged: a state that came half
// from a file and half from a command line is not a state anybody signed.
func (c *VerifySignatureCmd) signedState() (signature.State, error) {
	if c.State == "" {
		return c.stateFromFlags()
	}
	if c.Origin != "" || c.ManifestSHA256 != "" || c.ImageDigest != "" || c.LockSHA256 != "" || c.Generation != 0 {
		return signature.State{}, errors.New("give the state as a file or as flags, not as both")
	}
	encoded, err := os.ReadFile(c.State)
	if err != nil {
		return signature.State{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	state := signature.State{}
	if err = decoder.Decode(&state); err != nil {
		return signature.State{}, fmt.Errorf("read the signed state: %w", err)
	}
	if state.ABI == 0 {
		state.ABI = signature.ABIVersion
	}
	return state, nil
}

func (c *VerifySignatureCmd) stateFromFlags() (signature.State, error) {
	if c.Generation < 0 {
		return signature.State{}, fmt.Errorf("a generation counts up from 1, got %d", c.Generation)
	}
	return signature.State{
		ABI:            signature.ABIVersion,
		Origin:         c.Origin,
		ManifestSHA256: c.ManifestSHA256,
		ImageDigest:    c.ImageDigest,
		LockSHA256:     c.LockSHA256,
		Generation:     uint64(c.Generation),
	}, nil
}

func (c *VerifySignatureCmd) reportSigner(verified signature.Verified) {
	c.Logger.Info("The bundle covers this state and was issued to")
	c.Logger.Info("  issuer:     %s", orNone(verified.Identity.Issuer))
	c.Logger.Info("  subject:    %s", orNone(verified.Identity.Subject))
	c.Logger.Info("  repository: %s", orNone(verified.Identity.Repo))
}

// reportOrigin is the half that decides. Verifying a bundle says somebody
// signed this state; only the repository in the certificate says whether that
// somebody was the publisher of this origin.
func (c *VerifySignatureCmd) reportOrigin(verified signature.Verified, origin string) error {
	if verified.Identity.MatchesOrigin(origin) {
		c.Logger.Success("The certificate names %s, which is the origin of this package.", origin)
		c.Logger.Info("What that proves: the package came from the CI of that repository and was not altered on the way. What it does not prove: that the software is safe, or that the repository itself was not taken over.")
		return nil
	}
	if c.AnyIdentity {
		c.Logger.Warning("The certificate does not name %s, and --any-identity was given, so nothing was decided about it.", origin)
		return nil
	}
	return fmt.Errorf("the certificate does not name %s, so it may not speak for this package", origin)
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
