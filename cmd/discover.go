/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/catalog"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type DiscoverCmd struct {
	Action     string `arg:"action" help:"Action: list, install or remove"`
	Origin     string `arg:"origin" help:"Package origin for install or remove"`
	CatalogURL string `cli:"catalog" help:"Signed catalog URL"`

	cli.Base
}

type discoverDocument struct {
	Schema   int               `json:"schema"`
	Release  string            `json:"release"`
	Packages []discoverPackage `json:"packages"`
}

type discoverPackage struct {
	Origin           string                 `json:"origin"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	IconSVG          string                 `json:"icon_svg,omitempty"`
	IconPNG          string                 `json:"icon_png,omitempty"`
	Permissions      []bootstrap.Permission `json:"permissions,omitempty"`
	AvailableVersion string                 `json:"available_version"`
	InstalledVersion string                 `json:"installed_version,omitempty"`
	Installed        bool                   `json:"installed"`
	Installable      bool                   `json:"installable"`
}

func (c *DiscoverCmd) Run() error {
	action := strings.ToLower(c.Action)
	if action == "remove" {
		return c.remove()
	}
	if action != "list" && action != "install" {
		return fmt.Errorf("unsupported Discover action %q", c.Action)
	}
	packages, err := c.fetchCatalog()
	if err != nil {
		return err
	}
	if action == "install" {
		return c.install(packages)
	}
	return c.list(packages)
}

func (c *DiscoverCmd) fetchCatalog() ([]catalog.Package, error) {
	key, err := bootstrap.InstallerPublicKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	return catalog.Fetch(ctx, client, c.CatalogURL, runtime.GOARCH, key)
}

func (c *DiscoverCmd) list(packages []catalog.Package) error {
	installed, err := installedApplications()
	if err != nil {
		return err
	}
	document := discoverDocument{Schema: 1, Packages: make([]discoverPackage, 0, len(packages))}
	if len(packages) > 0 {
		document.Release = packages[0].Release
	}
	for _, item := range packages {
		metadata := item.Metadata
		result := discoverPackage{
			Origin:           metadata.Origin,
			Name:             metadata.Name,
			Description:      metadata.Description,
			IconSVG:          metadata.IconSVG,
			IconPNG:          metadata.IconPNG,
			Permissions:      metadata.Permissions,
			AvailableVersion: catalogVersion(item),
			Installable:      item.Installable,
		}
		if app, ok := installed[metadata.Origin]; ok {
			result.Installed = true
			result.InstalledVersion = applicationVersion(app)
		}
		document.Packages = append(document.Packages, result)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func catalogVersion(item catalog.Package) string {
	if item.Metadata.Version != "" {
		return item.Metadata.Version
	}
	if len(item.Metadata.Ref) > 12 {
		return item.Metadata.Ref[:12]
	}
	return item.Metadata.Ref
}

func (c *DiscoverCmd) install(packages []catalog.Package) error {
	origin := strings.ToLower(c.Origin)
	if origin == "" {
		return errors.New("package origin is required for install")
	}
	item, err := catalog.Find(packages, origin)
	if err != nil {
		return err
	}
	if !item.Installable {
		return errors.New("this catalog predates signed manifest binding; wait for the next cpak catalog")
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	manifest, err := cp.FetchManifest(origin, "", "", item.Metadata.Ref)
	if err != nil {
		return err
	}
	if err = cp.ValidateManifest(manifest); err != nil {
		return err
	}
	if err = verifySignedInstallerMetadata(item.Metadata, origin, item.Metadata.Ref, manifest); err != nil {
		return fmt.Errorf("verify the signed catalog entry: %w", err)
	}
	return cp.InstallCpakWithOptions(origin, manifest, "", item.Metadata.Ref, "", cpak.InstallOptions{
		CreateExports:   true,
		ResolveImageRef: true,
	})
}

func (c *DiscoverCmd) remove() error {
	origin := strings.ToLower(c.Origin)
	if origin == "" {
		return errors.New("package origin is required for remove")
	}
	installed, err := installedApplications()
	if err != nil {
		return err
	}
	app, ok := installed[origin]
	if !ok {
		return fmt.Errorf("application is not installed: %s", origin)
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	return cp.Remove(origin, app.Branch, app.Commit, app.Release)
}

func installedApplications() (map[string]types.Application, error) {
	cp, err := cpak.NewCpak()
	if err != nil {
		return nil, err
	}
	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	apps, err := store.GetApplications()
	if err != nil {
		return nil, err
	}
	result := make(map[string]types.Application, len(apps))
	for _, app := range apps {
		if _, exists := result[app.Origin]; !exists {
			result[app.Origin] = app
		}
	}
	return result, nil
}

func applicationVersion(app types.Application) string {
	switch {
	case app.Release != "":
		return app.Release
	case app.Commit != "":
		return app.Commit
	case app.Branch != "":
		return app.Branch
	default:
		return app.Version
	}
}
