/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemAuthorityCmd struct {
	Socket       string `cli:"socket" help:"path of the authority socket"`
	VerifyBundle bool   `cli:"verify-bundle" help:"read one signature verification request from standard input and answer it"`
	EnrolRequest bool   `cli:"enrol-request" help:"apply one privileged enrolment request from standard input"`

	cli.Base
}

func (c *SystemAuthorityCmd) Run() error {
	// The verifier is this program in its other role, and it is meant to run
	// with no privileges, so it is answered before the rule that the authority
	// itself must be root.
	if c.VerifyBundle {
		return systemauthority.RunVerifier(os.Stdin, os.Stdout)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("system authority must run as root")
	}
	if c.EnrolRequest {
		return systemauthority.RunEnrolmentRequest(os.Stdin)
	}
	ctx, stop := signalContext()
	defer stop()
	return systemauthority.Serve(ctx, c.Socket)
}
