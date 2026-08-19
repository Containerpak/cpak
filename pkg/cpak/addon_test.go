/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
		ImageDigest:  "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		ParsedLayers: []string{"base", "sdk"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if without == with {
		t.Fatal("enabling an addon did not change the container policy hash")
	}
}

// An addon that names other packages as layer dependencies is how a group of
// tools is offered as one choice: the parent declares one addon instead of
// listing every SDK, and the group can grow without touching the parent. Until
// the dependencies were composed, such a bundle contributed nothing but its
// own empty image, so enabling it looked like it worked and changed nothing.
func TestAnAddonBringsWhatItIsBuiltOn(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{
		Name:         "Code",
		Origin:       "github.com/containerpak/vscode",
		Version:      "main",
		ParsedAddons: []string{"github.com/containerpak/sdk-bundle"},
	}
	goSDK := types.Application{
		CpakId:       "go",
		Origin:       "github.com/containerpak/sdk-go",
		Branch:       "main",
		Version:      "main",
		ParsedLayers: []string{"base", "go"},
	}
	node := types.Application{
		CpakId:       "node",
		Origin:       "github.com/containerpak/sdk-node-lts",
		Branch:       "main",
		Version:      "main",
		ParsedLayers: []string{"base", "node"},
	}
	bundle := types.Application{
		CpakId:       "bundle",
		Origin:       "github.com/containerpak/sdk-bundle",
		Branch:       "main",
		Version:      "main",
		ParsedLayers: []string{"bundle"},
		ParsedDependencies: []types.Dependency{
			{Origin: goSDK.Origin, Id: goSDK.CpakId, Mode: "layer"},
			{Origin: node.Origin, Id: node.CpakId, Mode: "layer"},
		},
	}
	seedApplication(t, c, goSDK)
	seedApplication(t, c, node)
	seedApplication(t, c, bundle)
	if err := saveEnabledAddons(app, []string{bundle.Origin}); err != nil {
		t.Fatal(err)
	}

	addons, err := c.resolveEnabledAddons(app)
	if err != nil {
		t.Fatal(err)
	}
	layers := combinedLayers(types.Application{ParsedLayers: []string{"base", "app"}}, addons)
	want := []string{"base", "app", "go", "node", "bundle"}
	if !reflect.DeepEqual(layers, want) {
		t.Fatalf("the bundle did not bring what it names: got %v, want %v", layers, want)
	}
}

// A dependency sits under what depends on it, so a bundle's own image must
// come last or it cannot override anything it ships alongside.
func TestABundleIsStackedAboveWhatItBringsIn(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{
		Origin:       "github.com/containerpak/vscode",
		Version:      "main",
		ParsedAddons: []string{"github.com/containerpak/sdk-bundle"},
	}
	tool := types.Application{
		CpakId: "tool", Origin: "github.com/containerpak/sdk-go",
		Branch: "main", Version: "main", ParsedLayers: []string{"tool"},
	}
	bundle := types.Application{
		CpakId: "bundle", Origin: "github.com/containerpak/sdk-bundle",
		Branch: "main", Version: "main", ParsedLayers: []string{"bundle"},
		ParsedDependencies: []types.Dependency{{Origin: tool.Origin, Id: tool.CpakId, Mode: "layer"}},
	}
	seedApplication(t, c, tool)
	seedApplication(t, c, bundle)
	if err := saveEnabledAddons(app, []string{bundle.Origin}); err != nil {
		t.Fatal(err)
	}
	addons, err := c.resolveEnabledAddons(app)
	if err != nil {
		t.Fatal(err)
	}
	if len(addons) != 2 || addons[0].Origin != tool.Origin || addons[1].Origin != bundle.Origin {
		t.Fatalf("wrong order: %+v", addons)
	}
}
