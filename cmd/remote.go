/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type RemoteCmd struct {
	Action   string `arg:"action" help:"Action: list, add, remove or apps"`
	Name     string `arg:"name" help:"Remote name for add, remove or apps"`
	Location string `arg:"location" help:".flatpakrepo file or HTTPS address for add"`
	Arch     string `cli:"arch" help:"Architecture for apps (default: this machine)"`
	JSON     bool   `cli:"json,j" help:"Print output in JSON format"`

	cli.Base
}

func (c *RemoteCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	switch strings.ToLower(c.Action) {
	case "", "list":
		remotes, err := cp.FlatpakRemoteList()
		if err != nil {
			return err
		}
		if c.JSON {
			return printJSON(remotes)
		}
		rows := make([][]string, 0, len(remotes))
		for _, remote := range remotes {
			rows = append(rows, []string{remote.Name, tools.SanitizeForDisplay(remote.Title), remote.URL})
		}
		tools.ShowTable([]string{"Name", "Title", "URL"}, rows)
		return nil
	case "add":
		if c.Name == "" || c.Location == "" {
			return errors.New("name the remote and its .flatpakrepo, for example: cpak remote add example https://example.org/example.flatpakrepo")
		}
		description, err := readFlatpakDescription(c.Location)
		if err != nil {
			return err
		}
		remote, err := cp.AddFlatpakRemote(c.Name, description)
		if err != nil {
			return err
		}
		if c.JSON {
			return printJSON(remote)
		}
		c.Logger.Success("Remote %s added (%s)", remote.Name, remote.URL)
		for _, fingerprint := range remote.Fingerprints {
			c.Logger.Info("  trusted through the key %s", fingerprint)
		}
		return nil
	case "remove":
		if c.Name == "" {
			return errors.New("name the remote to remove")
		}
		if err := cp.RemoveFlatpakRemote(c.Name); err != nil {
			return err
		}
		if !c.JSON {
			c.Logger.Success("Remote %s removed", c.Name)
		}
		return nil
	case "apps":
		if c.Name == "" {
			return errors.New("name the remote to list the applications of")
		}
		apps, err := cp.FlatpakRemoteApps(c.Name, c.Arch)
		if err != nil {
			return err
		}
		if c.JSON {
			return printJSON(apps)
		}
		rows := make([][]string, 0, len(apps))
		for _, app := range apps {
			rows = append(rows, []string{app.ID, tools.SanitizeForDisplay(app.Name), app.Branch})
		}
		tools.ShowTable([]string{"ID", "Name", "Branch"}, rows)
		return nil
	default:
		return fmt.Errorf("unsupported remote action %q", c.Action)
	}
}

func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
