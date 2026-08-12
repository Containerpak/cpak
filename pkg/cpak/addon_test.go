/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"reflect"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestAddonConfigurationRoundTrip(t *testing.T) {
	newTestCpak(t)
	app := types.Application{
		Origin:  "github.com/containerpak/vscode",
		Version: "main",
	}
	want := []string{"github.com/containerpak/sdk-go", "github.com/containerpak/sdk-node-lts"}
	if err := saveEnabledAddons(app, []string{want[1], want[0], want[1]}); err != nil {
		t.Fatal(err)
	}
	got, err := loadEnabledAddons(app)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled addons: got %v, want %v", got, want)
	}
	path, err := addonConfigurationPath(app)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("addon configuration mode: got %o, want 600", info.Mode().Perm())
	}
}

func TestAddonConfigurationFollowsReleaseUpdates(t *testing.T) {
	newTestCpak(t)
	oldApp := types.Application{
		Origin:  "github.com/containerpak/vscode",
		Version: "v1.0.0",
		Release: "v1.0.0",
	}
	newApp := oldApp
	newApp.Version = "v2.0.0"
	newApp.Release = "v2.0.0"
	if err := saveEnabledAddons(oldApp, []string{"github.com/containerpak/sdk-go"}); err != nil {
		t.Fatal(err)
	}
	got, err := loadEnabledAddons(newApp)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"github.com/containerpak/sdk-go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled addons after release update: got %v, want %v", got, want)
	}
}

func TestResolveEnabledAddonsUsesManifestOrder(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{
		Name:    "Code",
		Origin:  "github.com/containerpak/vscode",
		Version: "main",
		ParsedAddons: []string{
			"github.com/containerpak/sdk-node-lts",
			"github.com/containerpak/sdk-go",
		},
	}
	node := types.Application{
		CpakId:       "node",
		Origin:       "github.com/containerpak/sdk-node-lts",
		Branch:       "main",
		Version:      "main",
		ParsedLayers: []string{"base", "node"},
	}
	goSDK := types.Application{
		CpakId:       "go",
		Origin:       "github.com/containerpak/sdk-go",
		Branch:       "main",
		Version:      "main",
		ParsedLayers: []string{"base", "go"},
	}
	seedApplication(t, c, node)
	seedApplication(t, c, goSDK)
	if err := saveEnabledAddons(app, []string{goSDK.Origin, node.Origin}); err != nil {
		t.Fatal(err)
	}
	addons, err := c.resolveEnabledAddons(app)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{addons[0].Origin, addons[1].Origin}; !reflect.DeepEqual(got, app.ParsedAddons) {
		t.Fatalf("addon order: got %v, want %v", got, app.ParsedAddons)
	}
	layers := combinedLayers(types.Application{ParsedLayers: []string{"base", "app"}}, addons)
	if want := []string{"base", "app", "node", "go"}; !reflect.DeepEqual(layers, want) {
		t.Fatalf("combined layers: got %v, want %v", layers, want)
	}
}

func TestResolveEnabledAddonsRejectsRemovedSupport(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{
		Name:    "Code",
		Origin:  "github.com/containerpak/vscode",
		Version: "main",
	}
	if err := saveEnabledAddons(app, []string{"github.com/containerpak/sdk-go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.resolveEnabledAddons(app); err == nil {
		t.Fatal("an enabled addon removed from the manifest was accepted")
	}
}

func TestAddonUsersFindEnabledParents(t *testing.T) {
	c := newTestCpak(t)
	parent := types.Application{
		CpakId:  "code",
		Name:    "Code",
		Origin:  "github.com/containerpak/vscode",
		Branch:  "main",
		Version: "main",
	}
	seedApplication(t, c, parent)
	if err := saveEnabledAddons(parent, []string{"github.com/containerpak/sdk-go"}); err != nil {
		t.Fatal(err)
	}
	users, err := c.addonUsers("github.com/containerpak/sdk-go")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Origin != parent.Origin {
		t.Fatalf("addon users: got %v, want %s", users, parent.Origin)
	}
}

func TestContainerPolicyHashChangesWithAddon(t *testing.T) {
	override := types.NewOverride()
	without, err := containerPolicyHash(override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	with, err := containerPolicyHash(override, nil, []types.Application{{
		CpakId:       "sdk",
		ImageDigest:  "sha256:one",
		ParsedLayers: []string{"base", "sdk"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if without == with {
		t.Fatal("enabling an addon did not change the container policy hash")
	}
}
