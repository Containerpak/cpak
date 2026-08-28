/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"runtime"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type InstallCmd struct {
	Remote          string `arg:"remote" help:"Remote Git repository"`
	Branch          string `cli:"branch,b" help:"Specify a branch"`
	Release         string `cli:"release,r" help:"Install a specific release"`
	Commit          string `cli:"commit,c" help:"Specify a commit"`
	Yes             bool   `cli:"yes,y" help:"Skip the confirmation prompt"`
	SignedInstaller bool   `cli:"signed-installer" help:"Verify consent from the parent application installer"`

	cli.Base
}

func (c *InstallCmd) Run() error {
	remote, err := cpak.NormalizeRepositoryOrigin(c.Remote)
	if err != nil {
		return err
	}

	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while installing cpak: %s", err)
	}

	versionParams := []string{c.Branch, c.Release, c.Commit}
	versionParamsCount := 0
	for _, versionParam := range versionParams {
		if versionParam != "" {
			versionParamsCount++
		}
	}
	if versionParamsCount > 1 {
		return fmt.Errorf("more than one version parameter specified")
	}
	if c.SignedInstaller && c.Commit == "" {
		return fmt.Errorf("a signed installer requires an immutable commit")
	}

	branch := c.Branch
	if versionParamsCount == 0 {
		branch, err = cp.GetDefaultBranch(remote)
		if err != nil {
			return err
		}
		// The branch name is whatever the remote repository calls its default,
		// so it is somebody else's text like everything printed below it.
		c.Logger.Info("No version specified, using the default branch: %s", tools.SanitizeForDisplay(branch))
	}

	manifest, err := cp.FetchManifest(remote, branch, c.Release, c.Commit)
	if err != nil {
		return err
	}
	if err = cp.ValidateManifest(manifest); err != nil {
		return err
	}
	if c.SignedInstaller {
		if err = verifySignedInstaller(remote, c.Commit, manifest); err != nil {
			return err
		}
	}

	c.describeRootPackage(manifest)

	// Every dependency is a package of its own, installed with the permissions
	// its own publisher asked for, so the user is agreeing to those too and
	// cannot be shown the installation without them. The graph is walked here,
	// after the package the user actually named has been described, so that a
	// repository that cannot be reached does not kill the command before it has
	// said anything; and the manifests it brings back are handed to the install
	// below, so none of them is fetched a second time.
	dependencies, err := cp.ResolveDependencies(remote, manifest)
	if err != nil {
		return err
	}

	c.describeDependencies(dependencies)
	c.describeRuntimeSourcesAndPermissions(manifest)

	if !c.Yes && !c.SignedInstaller && !tools.ConfirmOperation("Do you want to continue?") {
		return nil
	}

	return cp.InstallCpakWithOptions(remote, manifest, branch, c.Commit, c.Release, cpak.InstallOptions{
		CreateExports:        true,
		ResolveImageRef:      true,
		ResolvedDependencies: dependencies,
	})
}

// The prompt is the whole of what a user is given to decide on, and every
// string on it was written by the publisher of a package they are about to
// trust. Nothing reaches the terminal from a manifest without going through
// tools.SanitizeForDisplay first: a cursor movement inside one of these values
// redraws the lines above it, so the permissions on the screen would stop being
// the permissions being granted.
func (c *InstallCmd) describeRootPackage(manifest *types.CpakManifest) {
	c.Logger.Info("\nThe following cpak(s) will be installed:")
	c.Logger.Info("  - %s: %s", tools.SanitizeForDisplay(manifest.Name), tools.SanitizeForDisplay(manifest.Description))
	c.Logger.Info("")

	c.Logger.Info("The following will be exported:")
	for _, binary := range manifest.Binaries {
		c.Logger.Info("  - (binary) %s", tools.SanitizeForDisplay(binary))
	}
	for _, entry := range manifest.DesktopEntries {
		c.Logger.Info("  - (desktop entry) %s", tools.SanitizeForDisplay(entry))
	}
	for _, session := range manifest.Sessions {
		c.Logger.Info("  - (%s session) %s", tools.SanitizeForDisplay(session.Kind), tools.SanitizeForDisplay(session.Name))
		c.describePermissions(session.Override)
	}
	if provider := manifest.AddonProvider; provider != nil {
		c.Logger.Info("  - (addon provider) %s for %s (%s)", tools.SanitizeForDisplay(provider.ID), tools.SanitizeForDisplay(provider.Slot), tools.SanitizeForDisplay(provider.Mode))
	}
	c.Logger.Info("")
}

func (c *InstallCmd) describeDependencies(dependencies []cpak.ResolvedDependency) {
	c.Logger.Info("The following dependencies will be installed:")
	for _, dependency := range dependencies {
		// The origin and the description belong to a publisher the user never
		// named, and they are printed straight above the question that grants
		// the permissions below them.
		c.Logger.Info("  - %s: %s", tools.SanitizeForDisplay(dependency.Origin), tools.SanitizeForDisplay(dependency.Manifest.Description))
		c.Logger.Info("    with the following permissions:")
		c.describePermissions(dependency.Manifest.Override)
	}
	c.Logger.Info("")
}

func (c *InstallCmd) describeRuntimeSourcesAndPermissions(manifest *types.CpakManifest) {
	sources := cpak.RuntimeSourcesForArchitecture(manifest.RuntimeSources, runtime.GOARCH)
	if len(sources) > 0 {
		c.Logger.Info("The following files will be downloaded from third parties:")
		for _, source := range sources {
			c.Logger.Info("  - %s (%d bytes)", tools.SanitizeForDisplay(source.URL), source.Size)
		}
		c.Logger.Info("")
	}

	c.Logger.Info("The following permissions will be granted:")
	c.describePermissions(manifest.Override)
	c.Logger.Info("")
}

func (c *InstallCmd) describePermissions(override types.Override) {
	permissions := bootstrap.SummarizePermissions(override)
	if len(permissions) == 0 {
		c.Logger.Info("  - None")
		return
	}
	for _, permission := range permissions {
		c.Logger.Info("  - %s: %s", tools.SanitizeForDisplay(permission.Name), tools.SanitizeForDisplay(permission.Detail))
	}
}
