/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func newAliasTestCpak(t *testing.T) *Cpak {
	t.Helper()
	return &Cpak{Options: types.CpakOptions{StorePath: t.TempDir()}}
}

func addAliasTestApplication(t *testing.T, c *Cpak, origin string) {
	t.Helper()
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.NewApplication(types.Application{CpakId: strings.ReplaceAll(origin, "/", "-"), Origin: origin, Version: "main", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
}

func TestAliasesRoundTripAndResolveCaseInsensitiveNames(t *testing.T) {
	c := newAliasTestCpak(t)
	addAliasTestApplication(t, c, "github.com/containerpak/bottles")
	addAliasTestApplication(t, c, "github.com/containerpak/firefox")
	if err := c.SetAlias("Bottles", "github.com/Containerpak/Bottles"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetAlias("bottles", "github.com/containerpak/firefox"); err != nil {
		t.Fatal(err)
	}
	aliases, err := c.ListAliases()
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0] != (types.Alias{Name: "bottles", Origin: "github.com/containerpak/firefox"}) {
		t.Fatalf("aliases: %v", aliases)
	}
	origin, err := c.ResolveInstalledOrigin("BOTTLES")
	if err != nil {
		t.Fatal(err)
	}
	if origin != "github.com/containerpak/firefox" {
		t.Fatalf("origin: %s", origin)
	}
	info, err := os.Stat(c.aliasConfigurationPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("alias permissions: %o", info.Mode().Perm())
	}
}

func TestAliasesRejectMissingOriginsAndNames(t *testing.T) {
	c := newAliasTestCpak(t)
	if err := c.SetAlias("bottles", "github.com/containerpak/bottles"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing origin error: %v", err)
	}
	if err := c.SetAlias("bad_name", "github.com/containerpak/bottles"); err == nil {
		t.Fatal("accepted invalid alias name")
	}
	if _, err := c.ResolveOrigin("bottles"); err == nil || !strings.Contains(err.Error(), "alias not found") {
		t.Fatalf("missing alias error: %v", err)
	}
	if err := c.RemoveAlias("bottles"); err == nil || !strings.Contains(err.Error(), "alias not found") {
		t.Fatalf("missing alias removal error: %v", err)
	}
}

func TestAliasesReportUninstalledAliasOrigins(t *testing.T) {
	c := newAliasTestCpak(t)
	if err := c.saveAliasConfiguration(aliasConfiguration{
		Version: aliasConfigurationVersion,
		Aliases: map[string]string{"bottles": "github.com/containerpak/bottles"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveInstalledOrigin("bottles"); err == nil || !strings.Contains(err.Error(), `alias "bottles" refers to an origin that is not installed`) {
		t.Fatalf("uninstalled alias error: %v", err)
	}
}

func TestAliasesRejectCorruptStorage(t *testing.T) {
	c := newAliasTestCpak(t)
	path := c.aliasConfigurationPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"aliases":{"bottles":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListAliases(); err == nil || !strings.Contains(err.Error(), "alias storage is corrupted") {
		t.Fatalf("corrupt storage error: %v", err)
	}
}
