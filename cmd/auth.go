/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"fmt"
	"os"
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

	cli.Base
}

func (c *AuthCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	origin := strings.ToLower(c.Origin)
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
		c.Logger.Success("Registry access removed for %s", origin)
		return nil
	case "list":
		records, err := registryauth.Load(cp.Options.RegistryAuthPath)
		if err != nil {
			return err
		}
		rows := make([][]string, 0, len(records))
		for _, record := range records {
			rows = append(rows, []string{record.Origin, record.Registry, record.Repository, authenticationType(record)})
		}
		tools.ShowTable([]string{"Origin", "Registry", "Repository", "Type"}, rows)
		return nil
	case "status":
		if origin == "" {
			return fmt.Errorf("package origin is required for status")
		}
		records, err := registryauth.Load(cp.Options.RegistryAuthPath)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Origin == origin {
				c.Logger.Info("%s may authenticate to %s/%s using %s", origin, record.Registry, record.Repository, authenticationType(record))
				return nil
			}
		}
		return fmt.Errorf("no registry access is stored for %s", origin)
	default:
		return fmt.Errorf("unsupported auth action %q", c.Action)
	}
}

func (c *AuthCmd) login(cp cpak.Cpak, origin string) error {
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
	c.Logger.Success("Registry access saved for %s", origin)
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
	if record.Username == "" {
		return "token"
	}
	return "basic"
}
