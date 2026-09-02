/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"golang.org/x/term"
)

type AuthCmd struct {
	Action     string   `arg:"action" help:"Action: login, logout, list or status"`
	Origin     string   `arg:"origin" help:"Package origin"`
	Username   string   `cli:"username,u" help:"Registry username for basic authentication"`
	Token      bool     `cli:"token" help:"Store an access token instead of a password"`
	SecretFile string   `cli:"secret-file" help:"Read the password or token from a mode 0600 file"`
	TokenHosts []string `cli:"token-host" help:"Allow a separate registry token host"`
	GitHub     bool     `cli:"github" help:"Use the authenticated GitHub CLI for a private repository and GHCR"`

	cli.Base
}

func (c *AuthCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	origin := c.Origin
	if origin != "" {
		origin, err = cpak.NormalizeRepositoryOrigin(origin)
		if err != nil {
			return err
		}
	}
	switch strings.ToLower(c.Action) {
	case "login":
		if origin == "" {
			return fmt.Errorf("package origin is required for login")
		}
		return c.login(cp, origin)
	case "logout":
		if origin == "" {
			return fmt.Errorf("package origin is required for logout")
		}
		if err = registryauth.Remove(cp.Ctx, cp.Options.RegistryAuthPath, origin); err != nil {
			return err
		}
		c.Logger.Success("Package access removed for %s", origin)
		return nil
	case "list":
		records, err := registryauth.Load(cp.Options.RegistryAuthPath)
		if err != nil {
			return err
		}
		rows := make([][]string, 0, len(records))
		for _, record := range records {
			rows = append(rows, []string{record.Origin, record.SourceHost, record.Registry, record.Repository, authenticationType(record)})
		}
		tools.ShowTable([]string{"Origin", "Source", "Registry", "Repository", "Type"}, rows)
		return nil
	case "status":
		if origin == "" {
			return fmt.Errorf("package origin is required for status")
		}
		records, err := registryauth.Load(cp.Options.RegistryAuthPath)
		if err != nil {
			return err
		}
		found := false
		for _, record := range records {
			if record.Origin == origin {
				c.Logger.Info("%s may authenticate to %s using %s", origin, authenticationScope(record), authenticationType(record))
				found = true
			}
		}
		if found {
			return nil
		}
		return fmt.Errorf("no package access is stored for %s", origin)
	default:
		return fmt.Errorf("unsupported auth action %q", c.Action)
	}
}

func (c *AuthCmd) login(cp cpak.Cpak, origin string) error {
	if c.GitHub {
		return c.loginGitHub(cp, origin)
	}
	if c.Token && c.Username != "" {
		return fmt.Errorf("--token cannot be combined with --username; use --github for GitHub and GHCR")
	}
	branch, err := cp.GetDefaultBranch(origin)
	if err != nil {
		return err
	}
	manifest, err := cp.FetchManifest(origin, branch, "", "")
	if err != nil {
		return err
	}
	ref, err := oci.ParseReference(manifest.Image)
	if err != nil {
		return err
	}
	secret, err := c.readSecret()
	if err != nil {
		return err
	}
	record := registryauth.Record{
		Origin:     origin,
		Registry:   ref.Registry,
		Repository: ref.Repository,
		Username:   c.Username,
		TokenHosts: append([]string{}, c.TokenHosts...),
	}
	if c.SecretFile != "" {
		record.SecretFile, err = filepath.Abs(c.SecretFile)
		if err != nil {
			return err
		}
	}
	if c.Token {
		record.Username = ""
	}
	c.Logger.Info("Registry access will be limited to %s/%s for %s.", record.Registry, record.Repository, origin)
	if err = registryauth.Save(cp.Ctx, cp.Options.RegistryAuthPath, record, secret); err != nil {
		return err
	}
	c.reportCredentialFallback(cp.Options.RegistryAuthPath, origin)
	c.Logger.Success("Registry access saved for %s", origin)
	return nil
}

func (c *AuthCmd) loginGitHub(cp cpak.Cpak, origin string) error {
	if c.Username != "" || c.Token || len(c.TokenHosts) != 0 {
		return fmt.Errorf("--github cannot be combined with registry credential options")
	}
	parts := strings.Split(origin, "/")
	if len(parts) != 3 || parts[0] != "github.com" {
		return fmt.Errorf("--github requires a github.com package origin")
	}
	secret, err := c.readGitHubSecret()
	if err != nil {
		return err
	}
	provider, err := cpak.NewRepoProvider(origin, cp.Options.ManifestsPath)
	if err != nil {
		return err
	}
	provider.AccessToken = secret
	username, err := provider.GetAuthenticatedUser()
	if err != nil {
		return err
	}
	branch, err := provider.GetDefaultBranch()
	if err != nil {
		return err
	}
	content, err := provider.GetFileInBranch("cpak.json", branch)
	if err != nil {
		return fmt.Errorf("failed to get manifest file: %w", err)
	}
	manifest, err := cpak.DecodeManifest(content)
	if err != nil {
		return fmt.Errorf("failed to decode manifest file: %w", err)
	}
	ref, err := oci.ParseReference(manifest.Image)
	if err != nil {
		return err
	}
	record := registryauth.Record{Origin: origin, SourceHost: "github.com"}
	if ref.Registry == "ghcr.io" {
		record.Registry = ref.Registry
		record.Repository = ref.Repository
		record.Username = username
	}
	if c.SecretFile != "" {
		record.SecretFile, err = filepath.Abs(c.SecretFile)
		if err != nil {
			return err
		}
	}
	c.Logger.Info("GitHub access will be limited to %s for %s.", record.SourceHost, origin)
	if record.Registry != "" {
		c.Logger.Info("GHCR access will be limited to %s/%s for %s.", record.Registry, record.Repository, origin)
	}
	if err = registryauth.Save(cp.Ctx, cp.Options.RegistryAuthPath, record, secret); err != nil {
		return err
	}
	c.reportCredentialFallback(cp.Options.RegistryAuthPath, origin)
	c.Logger.Success("GitHub access saved for %s", origin)
	return nil
}

func (c *AuthCmd) reportCredentialFallback(path, origin string) {
	records, err := registryauth.Load(path)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.Origin == origin && record.SecretFileManaged {
			c.Logger.Info("Secret Service is unavailable; cpak stored the credential in its private configuration directory with mode 0600.")
			return
		}
	}
}

func (c *AuthCmd) readGitHubSecret() (string, error) {
	if c.SecretFile != "" {
		return registryauth.ReadSecretFile(c.SecretFile)
	}
	secret, err := githubToken()
	if err == nil {
		return secret, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("GitHub CLI is not authenticated; run gh auth login or use --secret-file: %w", err)
	}
	if err = githubLogin(); err != nil {
		return "", err
	}
	return githubToken()
}

var githubToken = func() (string, error) {
	output, err := exec.Command("gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		return "", fmt.Errorf("read GitHub CLI token: %w", err)
	}
	secret := strings.TrimSpace(string(output))
	if secret == "" {
		return "", fmt.Errorf("GitHub CLI returned an empty token")
	}
	return secret, nil
}

var githubLogin = func() error {
	command := exec.Command("gh", "auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--web", "--scopes", "repo,read:packages")
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("authenticate GitHub CLI: %w", err)
	}
	return nil
}

func (c *AuthCmd) readSecret() (string, error) {
	if c.SecretFile != "" {
		return registryauth.ReadSecretFile(c.SecretFile)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("a terminal or --secret-file is required")
	}
	label := "Password"
	if c.Token || c.Username == "" {
		label = "Access token"
		c.Token = true
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("registry credential is required")
	}
	return string(secret), nil
}

func authenticationType(record registryauth.Record) string {
	prefix := ""
	if record.SourceHost != "" {
		prefix = "github+"
	}
	if record.Registry == "" {
		return strings.TrimSuffix(prefix, "+")
	}
	if record.Username == "" {
		return prefix + "token"
	}
	return prefix + "basic"
}

func authenticationScope(record registryauth.Record) string {
	scopes := make([]string, 0, 2)
	if record.SourceHost != "" {
		scopes = append(scopes, record.SourceHost)
	}
	if record.Registry != "" {
		scopes = append(scopes, record.Registry+"/"+record.Repository)
	}
	return strings.Join(scopes, " and ")
}
