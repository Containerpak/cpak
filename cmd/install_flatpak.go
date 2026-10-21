/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
)

const maxFlatpakRefFile = 1 << 20

// installFlatpak installs a Flatpak application. Its exit code tells a
// refusal of the permissions, an unreachable remote and an answer that failed
// verification apart, and with --json-progress the last event carries it too.
func (c *InstallCmd) installFlatpak() error {
	var progress *progressWriter
	if c.JSONProgress {
		progress = newProgressWriter(&c.Base)
	}
	err := c.runFlatpakInstall(progress)
	if progress != nil {
		progress.done(exitCode(err), err, nil)
	}
	return exitWith(err)
}

func (c *InstallCmd) runFlatpakInstall(progress *progressWriter) error {
	if c.Release != "" || c.Commit != "" {
		return errors.New("a Flatpak application is installed from a branch; use --branch")
	}
	if c.SignedInstaller {
		return errors.New("a signed installer cannot install a Flatpak application")
	}
	if c.Remote == "" {
		return errors.New("name the application to install, for example: cpak install --flatpak org.gnome.Calculator")
	}
	request := cpak.FlatpakRequest{Branch: c.Branch, Remote: c.FlatpakRemote}
	if strings.HasSuffix(c.Remote, ".flatpakref") {
		data, err := readFlatpakRefFile(c.Remote)
		if err != nil {
			return err
		}
		request.RefFile = data
	} else {
		request.ID = c.Remote
	}

	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while installing cpak: %s", err)
	}
	if progress != nil {
		cp.SetFlatpakProgress(progress.report)
		progress.report(cpak.FlatpakProgress{Phase: cpak.FlatpakPhaseResolve})
	}
	plan, err := cp.ResolveFlatpak(request)
	if err != nil {
		return err
	}
	c.describeFlatpakPlan(plan)
	if !c.Yes && !tools.ConfirmOperation("Do you want to continue?") {
		return errPermissionsRefused
	}
	return cp.InstallFlatpak(plan)
}

// readFlatpakRefFile reads a .flatpakref from a path or an HTTPS address. It
// carries the key the repository is then trusted through, so it is never read
// over plain HTTP.
func readFlatpakRefFile(location string) ([]byte, error) {
	parsed, err := url.Parse(location)
	if err == nil && parsed.Scheme != "" {
		if parsed.Scheme != "https" {
			return nil, fmt.Errorf("a .flatpakref is only read over HTTPS")
		}
		client := &http.Client{Timeout: 30 * time.Second}
		response, err := client.Get(location)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", location, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: %s", location, response.Status)
		}
		return readLimited(response.Body)
	}
	file, err := os.Open(location)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file)
}

func readLimited(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxFlatpakRefFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFlatpakRefFile {
		return nil, errors.New("the .flatpakref is too large")
	}
	return data, nil
}

// Everything printed here comes from the remote, which is somebody else's
// text like a manifest is, so it goes through the same sanitising.
func (c *InstallCmd) describeFlatpakPlan(plan *cpak.FlatpakPlan) {
	show := tools.SanitizeForDisplay
	if plan.AddRemote {
		fingerprints, _ := plan.Remote.Fingerprints()
		c.Logger.Warning("A new Flatpak remote will be added: %s (%s)", show(plan.Remote.Name), show(plan.Remote.URL))
		for _, fingerprint := range fingerprints {
			c.Logger.Warning("  trusted through the key %s", show(fingerprint))
		}
		c.Logger.Info("")
	}
	c.Logger.Info("\nThe following Flatpak application will be installed in the cpak sandbox:")
	c.Logger.Info("  - %s from %s (commit %s)", show(plan.App.Ref.String()), show(plan.Remote.Name), plan.App.Commit.Checksum.String()[:12])
	c.Logger.Info("    runtime %s (commit %s)", show(plan.Runtime.Ref.String()), plan.Runtime.Commit.Checksum.String()[:12])
	for _, extension := range append(append([]cpak.FlatpakPart{}, plan.RuntimeExtensions...), plan.AppExtensions...) {
		c.Logger.Info("    extension %s", show(extension.Ref.String()))
	}
	c.Logger.Info("    installed as %s", show(plan.Origin))
	c.Logger.Info("")
	c.Logger.Info("The following permissions will be granted:")
	c.describePermissions(plan.Translation.Override)
	c.Logger.Info("")
	refusals := plan.Translation.Refusals()
	if len(refusals) > 0 {
		c.Logger.Info("The application asked for more, which cpak does not grant:")
		for _, finding := range refusals {
			label := "refused"
			if finding.Decision == flatpak.Narrowed {
				label = "narrowed"
			}
			c.Logger.Info("  - %s (%s): %s", show(finding.Request), label, show(finding.Reason))
		}
		c.Logger.Info("Grant any of it later with cpak override %s.", show(plan.Origin))
		c.Logger.Info("")
	}
}
