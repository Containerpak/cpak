/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
)

func TestRegistryTokenCannotDiscardAUsername(t *testing.T) {
	err := (&AuthCmd{Token: true, Username: "account"}).login(cpak.Cpak{}, "github.com/example/app")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("ambiguous registry credential was accepted: %v", err)
	}
}

func TestGithubLoginRejectsRegistryCredentialFlags(t *testing.T) {
	err := (&AuthCmd{GitHub: true, Username: "account"}).login(cpak.Cpak{}, "github.com/example/app")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("mixed GitHub credential was accepted: %v", err)
	}
}

func TestReadGithubSecretUsesTheAuthenticatedCLI(t *testing.T) {
	original := githubToken
	t.Cleanup(func() { githubToken = original })
	githubToken = func() (string, error) { return "source-secret", nil }
	secret, err := (&AuthCmd{}).readGitHubSecret()
	if err != nil || secret != "source-secret" {
		t.Fatalf("GitHub CLI credential: %q %v", secret, err)
	}
}

func TestReadGithubSecretReportsMissingHeadlessLogin(t *testing.T) {
	original := githubToken
	t.Cleanup(func() { githubToken = original })
	githubToken = func() (string, error) { return "", errors.New("not logged in") }
	secret, err := (&AuthCmd{}).readGitHubSecret()
	if secret != "" || err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("missing GitHub CLI login: %q %v", secret, err)
	}
}

func TestAuthenticationTypeDescribesBothScopes(t *testing.T) {
	if got := authenticationType(registryauth.Record{SourceHost: "github.com"}); got != "github" {
		t.Fatalf("source authentication type: %q", got)
	}
	if got := authenticationType(registryauth.Record{SourceHost: "github.com", Registry: "ghcr.io", Username: "account"}); got != "github+basic" {
		t.Fatalf("combined authentication type: %q", got)
	}
}
