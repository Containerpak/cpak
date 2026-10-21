/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

// flatpakDeletionConsent is typed in full: deleting the Flatpak installations
// takes their data with them, and no flag is allowed to say yes to that.
const flatpakDeletionConsent = "Yes, I confirm I want to delete all my Flatpaks once the cpak migration is completed, losing all my data."

type MigrateFlatpakCmd struct {
	App            string `arg:"app" help:"Flatpak application id, every installed Flatpak application if omitted"`
	Yes            bool   `cli:"yes,y" help:"Skip the confirmation prompt"`
	DeleteFlatpaks bool   `cli:"delete-flatpaks" help:"Uninstall the Flatpak applications and their data once they are migrated"`

	cli.Base
}

type flatpakMigration struct {
	source flatpak.InstalledApp
	plan   *cpak.FlatpakPlan
}

func (c *MigrateFlatpakCmd) Run() error {
	if c.App != "" {
		if err := flatpak.ValidateID(c.App); err != nil {
			return err
		}
	}
	if c.DeleteFlatpaks {
		if _, err := exec.LookPath("flatpak"); err != nil {
			return errors.New("--delete-flatpaks needs flatpak to uninstall the applications, and it is not installed")
		}
	}
	found, err := c.hostApplications()
	if err != nil {
		return err
	}
	if len(found) == 0 {
		if c.App != "" {
			return fmt.Errorf("%s is not installed with Flatpak", c.App)
		}
		c.Logger.Info("No Flatpak application is installed on this system.")
		return nil
	}

	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while migrating Flatpak applications: %s", err)
	}
	migrations := []flatpakMigration{}
	unresolved := 0
	for _, source := range found {
		plan, err := cp.ResolveFlatpakMigration(source)
		if err != nil {
			c.Logger.Warning("%s (%s installation) cannot be migrated: %s", source.Ref.ID, source.Installation.Kind, tools.SanitizeForDisplay(err.Error()))
			unresolved++
			continue
		}
		migrations = append(migrations, flatpakMigration{source: source, plan: plan})
	}
	if len(migrations) == 0 {
		return errors.New("no Flatpak application can be migrated")
	}

	install := InstallCmd{Base: c.Base}
	for _, migration := range migrations {
		install.describeFlatpakPlan(migration.plan)
	}
	c.Logger.Info("The data each application kept in ~/.var/app is copied into the private home cpak gives it, without its cache.")
	if !c.DeleteFlatpaks {
		c.Logger.Info("Your Flatpak applications and their data are left as they are.")
		if !c.Yes && !tools.ConfirmOperation("Do you want to continue?") {
			return nil
		}
	} else if !c.confirmDeletion(unresolved) {
		c.Logger.Info("Nothing was migrated or deleted.")
		return nil
	}

	migrated := []flatpak.InstalledApp{}
	for _, migration := range migrations {
		if c.migrate(cp, migration) {
			migrated = append(migrated, migration.source)
		}
	}
	c.Logger.Success("%d of %d Flatpak applications are now installed with cpak", len(migrated), len(found))
	if !c.DeleteFlatpaks {
		return nil
	}
	return c.deleteMigrated(migrated, len(found))
}

func (c *MigrateFlatpakCmd) hostApplications() ([]flatpak.InstalledApp, error) {
	found := []flatpak.InstalledApp{}
	for _, installation := range flatpak.HostInstallations() {
		apps, err := installation.Apps()
		if err != nil {
			return nil, err
		}
		for _, app := range apps {
			if c.App == "" || app.Ref.ID == c.App {
				found = append(found, app)
			}
		}
	}
	return found, nil
}

// confirmDeletion asks for the consent sentence, typed exactly; --yes does
// not stand in for it.
func (c *MigrateFlatpakCmd) confirmDeletion(unresolved int) bool {
	c.Logger.Warning("--delete-flatpaks uninstalls every migrated application from Flatpak once the migration is completed, together with everything it kept in ~/.var/app.")
	if unresolved > 0 {
		c.Logger.Warning("The %d applications that cannot be migrated are not deleted.", unresolved)
	}
	c.Logger.Warning("To continue, type the following sentence exactly:")
	c.Logger.Info("  %s", flatpakDeletionConsent)
	logger.Printf("> ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	return strings.TrimSpace(line) == flatpakDeletionConsent
}

// migrate installs one application and moves its data. A failure is reported
// and leaves the Flatpak installation alone, whatever was asked for it.
func (c *MigrateFlatpakCmd) migrate(cp cpak.Cpak, migration flatpakMigration) bool {
	id := migration.source.Ref.ID
	c.Logger.Info("Migrating %s", id)
	if err := cp.InstallFlatpak(migration.plan); err != nil {
		c.Logger.Error("%s was not installed: %s", id, err)
		return false
	}
	app, err := cp.InstalledFlatpak(migration.plan)
	if err != nil {
		c.Logger.Error("%s: %s", id, err)
		return false
	}
	data, err := cp.MigrateFlatpakData(app)
	if err != nil {
		c.Logger.Error("the data of %s was not migrated: %s", id, err)
		return false
	}
	if data.HostHome {
		c.Logger.Warning("%s is granted your home, so its data was left in %s; it is not deleted", id, data.Source)
		return false
	}
	if data.Kept > 0 {
		c.Logger.Info("  %d files copied, %d already in the cpak home kept", data.Copied, data.Kept)
	} else if data.Copied > 0 {
		c.Logger.Info("  %d files copied", data.Copied)
	}
	return true
}

func (c *MigrateFlatpakCmd) deleteMigrated(migrated []flatpak.InstalledApp, found int) error {
	if len(migrated) == 0 {
		return errors.New("no application was migrated, so no Flatpak was deleted")
	}
	installations := map[string]flatpak.Installation{}
	failed := 0
	for _, app := range migrated {
		removed, err := cpak.RemoveHostFlatpak(app)
		if !removed {
			c.Logger.Error("%s is still installed with Flatpak: %v", app.Ref.ID, err)
			failed++
			continue
		}
		if err != nil {
			c.Logger.Warning("%s was removed, but Flatpak reported: %s", app.Ref.ID, err)
		}
		installations[app.Installation.Kind] = app.Installation
	}
	for _, installation := range installations {
		if err := cpak.RemoveUnusedHostFlatpaks(installation); err != nil {
			c.Logger.Warning("unused Flatpak runtimes were not removed from the %s installation: %s", installation.Kind, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d Flatpak applications were migrated but not deleted", failed)
	}
	if kept := found - len(migrated); kept > 0 {
		c.Logger.Warning("%d Flatpak applications were not migrated and are still installed", kept)
	}
	c.Logger.Success("%d Flatpak applications were deleted", len(migrated))
	return nil
}
