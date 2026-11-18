/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
	"github.com/mirkobrombin/cpak/pkg/ostree/ostreetest"
)

const testAppstream = `<?xml version="1.0" encoding="UTF-8"?>
<components version="0.8" origin="local">
  <component type="desktop">
    <id>org.example.App.desktop</id>
    <name>Example</name>
    <name xml:lang="it">Esempio</name>
    <summary>Does example things</summary>
    <developer_name>Example Team</developer_name>
    <icon type="cached" width="128" height="128">org.example.App.png</icon>
  </component>
  <component type="runtime">
    <id>org.example.Platform</id>
    <name>Platform</name>
  </component>
  <component type="desktop-application">
    <id>org.example.Unpublished</id>
    <name>Gone</name>
  </component>
</components>
`

func appstreamRepo(t *testing.T) *ostreetest.Repo {
	t.Helper()
	key := testFlatpakKey()
	repo := ostreetest.NewRepo(true, key)
	t.Cleanup(repo.Close)
	repo.Commit("app/org.example.App/x86_64/stable", map[string]ostreetest.File{"files/bin/example": {Content: []byte("x"), Mode: 0o755}}, map[string]gvariant.Variant{"xa.metadata": {Signature: "s", Value: "[Application]\nname=org.example.App\n"}})
	repo.Commit("app/org.example.Bare/x86_64/master", map[string]ostreetest.File{"files/bin/bare": {Content: []byte("x"), Mode: 0o755}}, nil)
	repo.Commit("appstream2/x86_64", map[string]ostreetest.File{
		"appstream.xml":                     {Content: []byte(testAppstream), Mode: 0o644},
		"icons/128x128/org.example.App.png": {Content: []byte("png"), Mode: 0o644},
	}, nil)
	return repo
}

func repoDescription(url string) []byte {
	return []byte("[Flatpak Repo]\nTitle=Example Apps\nUrl=" + url + "\nGPGKey=" + base64.StdEncoding.EncodeToString(testFlatpakKey().Public()) + "\n")
}

func TestFlatpakRemoteAddListAppsRemove(t *testing.T) {
	c := flatpakTestCpak(t)
	repo := appstreamRepo(t)
	added, err := c.AddFlatpakRemote("example", repoDescription(repo.URL()))
	if err != nil {
		t.Fatal(err)
	}
	if added.Title != "Example Apps" || !added.Removable || len(added.Fingerprints) == 0 {
		t.Fatalf("added %+v", added)
	}
	listed, err := c.FlatpakRemoteList()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != "example" || listed[1].Name != "flathub" || listed[1].Removable {
		t.Fatalf("listed %+v", listed)
	}
	if _, err := c.AddFlatpakRemote("again", repoDescription(repo.URL())); err == nil {
		t.Fatal("the same repository was added under a second name")
	}

	apps, err := c.FlatpakRemoteApps("example", "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 {
		t.Fatalf("apps %+v", apps)
	}
	app, bare := apps[0], apps[1]
	if app.ID != "org.example.App" || app.Name != "Example" || app.Summary != "Does example things" || app.Developer != "Example Team" || app.Branch != "stable" || app.Origin != "flatpak/example/org.example.app" {
		t.Fatalf("described app %+v", app)
	}
	icon, err := os.ReadFile(app.Icon)
	if err != nil || string(icon) != "png" {
		t.Fatalf("icon %q: %v", app.Icon, err)
	}
	if bare.ID != "org.example.Bare" || bare.Name != "org.example.Bare" || bare.Branch != "master" {
		t.Fatalf("undescribed app %+v", bare)
	}

	if err := c.RemoveFlatpakRemote("flathub"); err == nil {
		t.Fatal("the built-in remote was removed")
	}
	if err := c.RemoveFlatpakRemote("example"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(app.Icon); !os.IsNotExist(err) {
		t.Fatalf("the cached icon outlived its remote: %v", err)
	}
	if _, err := c.FlatpakRemoteApps("example", "x86_64"); err == nil || !strings.Contains(err.Error(), "no remote named example") {
		t.Fatalf("a removed remote still answered: %v", err)
	}
}

func TestFlatpakRemoteAddRefusesARepositoryItCannotVerify(t *testing.T) {
	c := flatpakTestCpak(t)
	repo := ostreetest.NewRepo(true, ostreetest.NewKey())
	t.Cleanup(repo.Close)
	repo.Commit("app/org.example.App/x86_64/stable", map[string]ostreetest.File{"files/x": {Content: []byte("x")}}, nil)
	if _, err := c.AddFlatpakRemote("example", repoDescription(repo.URL())); err == nil {
		t.Fatal("a repository signed by another key was added")
	}
	listed, err := c.FlatpakRemoteList()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("a refused remote was stored: %+v", listed)
	}
}
