/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	"os"

	"github.com/mirkobrombin/cpak/pkg/oci"
)

const (
	usernameVariable = "CPAK_REGISTRY_USERNAME"
	passwordVariable = "CPAK_REGISTRY_PASSWORD"
)

// environmentCredentials reads registry credentials from the environment. A
// workflow already holds them there, and a password passed as a flag is
// readable by every process on the machine for as long as the command runs.
type environmentCredentials struct{}

var _ oci.CredentialProvider = environmentCredentials{}

func (environmentCredentials) Credential(context.Context, oci.Reference) (oci.Credential, error) {
	return registryCredential(), nil
}

func registryCredential() oci.Credential {
	return oci.Credential{
		Username: os.Getenv(usernameVariable),
		Password: os.Getenv(passwordVariable),
	}
}
