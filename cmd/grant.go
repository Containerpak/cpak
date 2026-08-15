/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type GrantCmd struct {
	List   GrantListCmd   `cmd:"list" help:"List persistent file grants"`
	Revoke GrantRevokeCmd `cmd:"revoke" help:"Revoke a persistent file grant"`
	Manage GrantManageCmd `cmd:"manage" help:"Manage persistent file grants"`
}

type GrantManageCmd struct {
	Remote string `arg:"remote" help:"Installed package origin or alias"`

	cli.Base
}

func (c *GrantManageCmd) Run() error {
	cp, app, err := resolveGrantApplication(c.Remote)
	if err != nil {
		return err
	}
	store := filegrant.Store{Directory: filepath.Join(cp.Options.StorePath, "grants")}
	grants, err := store.Load(app.Origin)
	if err != nil {
		return err
	}
	return desktopui.ManageFileGrants(desktopui.GrantManagerRequest{
		Application: app.Name,
		Grants:      grants,
		Revoke: func(grant filegrant.Grant) error {
			if err := store.Remove(app.Origin, grant.ID); err != nil {
				return err
			}
			return cp.Stop(app.Origin, app.Version, app.Branch, app.Commit, app.Release)
		},
	})
}

type GrantListCmd struct {
	Remote string `arg:"remote" help:"Installed package origin or alias"`
	JSON   bool   `cli:"json,j" help:"Print output in JSON format"`

	cli.Base
}

func (c *GrantListCmd) Run() error {
	cp, app, err := resolveGrantApplication(c.Remote)
	if err != nil {
		return err
	}
	store := filegrant.Store{Directory: filepath.Join(cp.Options.StorePath, "grants")}
	grants, err := store.Load(app.Origin)
	if err != nil {
		return err
	}
	if c.JSON {
		encoded, err := json.MarshalIndent(grants, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	rows := make([][]string, 0, len(grants))
	for _, grant := range grants {
		rows = append(rows, []string{grant.ID[:12], grant.Selection, grant.Kind, grant.Access})
	}
	tools.ShowTable([]string{"ID", "Selection", "Kind", "Access"}, rows)
	return nil
}

type GrantRevokeCmd struct {
	Remote string `arg:"remote" help:"Installed package origin or alias"`
	ID     string `arg:"id" help:"Persistent grant ID or unique ID prefix"`

	cli.Base
}

func (c *GrantRevokeCmd) Run() error {
	cp, app, err := resolveGrantApplication(c.Remote)
	if err != nil {
		return err
	}
	store := filegrant.Store{Directory: filepath.Join(cp.Options.StorePath, "grants")}
	grants, err := store.Load(app.Origin)
	if err != nil {
		return err
	}
	id := ""
	for _, grant := range grants {
		if grant.ID == c.ID || len(c.ID) >= 8 && len(c.ID) < len(grant.ID) && grant.ID[:len(c.ID)] == c.ID {
			if id != "" {
				return fmt.Errorf("file grant ID prefix is ambiguous: %s", c.ID)
			}
			id = grant.ID
		}
	}
	if id == "" {
		return fmt.Errorf("file grant not found: %s", c.ID)
	}
	if err = store.Remove(app.Origin, id); err != nil {
		return err
	}
	if err = cp.Stop(app.Origin, app.Version, app.Branch, app.Commit, app.Release); err != nil {
		return fmt.Errorf("file grant was revoked but the running package could not be stopped: %w", err)
	}
	c.Logger.Success("File grant %s revoked for %s", id[:12], app.Name)
	return nil
}

func resolveGrantApplication(value string) (cpak.Cpak, types.Application, error) {
	cp, err := cpak.NewCpak()
	if err != nil {
		return cpak.Cpak{}, types.Application{}, err
	}
	origin, err := resolveApplicationOrigin(cp, value)
	if err != nil {
		return cpak.Cpak{}, types.Application{}, err
	}
	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		return cpak.Cpak{}, types.Application{}, err
	}
	defer store.Close()
	app, err := store.GetApplicationByOrigin(origin, "", "", "", "")
	return cp, app, err
}
