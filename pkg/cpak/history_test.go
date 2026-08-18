/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestRollbackRestoresPreviousInstallation(t *testing.T) {
	c := newTestCpak(t)
	previous := types.Application{
		CpakId:       testCpakId("branch", "main"),
		Name:         "demo",
		Version:      "main",
		Branch:       "main",
		Origin:       testOrigin,
		ParsedLayers: []string{"oldlayer"},
		Config:       "{}",
	}
	seedApplication(t, c, previous)

	stub := &updateStub{manifest: newTestManifest(), layers: []string{"newlayer"}, config: "{}", imageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111"}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}
	if results[0].Status != types.UpdateStatusUpdated {
		t.Fatalf("expected update, got %q", results[0].Status)
	}

	result, err := c.Rollback(testOrigin)
	if err != nil {
		t.Fatalf("rollback returned an error: %v", err)
	}
	if result.FromVersion != "main" || result.ToVersion != "main" {
		t.Fatalf("unexpected rollback result: %+v", result)
	}
	apps := storedApplications(t, c)
	if len(apps) != 1 || len(apps[0].ParsedLayers) != 1 || apps[0].ParsedLayers[0] != "oldlayer" {
		t.Fatalf("rollback did not restore previous application: %+v", apps)
	}
	history, err := c.readRollbackHistory(testOrigin)
	if err != nil {
		t.Fatalf("read rollback history: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("expected consumed history, got %+v", history)
	}
}

func TestRollbackRejectsMissingHistory(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:  testCpakId("branch", "main"),
		Origin:  testOrigin,
		Branch:  "main",
		Version: "main",
	})
	if _, err := c.Rollback(testOrigin); err == nil {
		t.Fatal("expected rollback to reject missing history")
	}
}

func TestRollbackHistoryIsBounded(t *testing.T) {
	c := newTestCpak(t)
	for version := 0; version < rollbackHistoryLimit+2; version++ {
		app := types.Application{Origin: testOrigin, Version: strconv.Itoa(version)}
		if err := c.recordRollbackHistory(app); err != nil {
			t.Fatalf("record history: %v", err)
		}
	}
	history, err := c.readRollbackHistory(testOrigin)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != rollbackHistoryLimit || history[0].Version != "2" {
		t.Fatalf("unexpected bounded history: %+v", history)
	}
}

func TestCollectGarbageKeepsRollbackLayers(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(filepath.Dir(c.Options.StorePath), "cache")
	for _, layer := range []string{"current", "rollback", "orphan"} {
		if err := os.MkdirAll(c.GetInStoreDir("layers", layer), 0755); err != nil {
			t.Fatalf("create layer: %v", err)
		}
	}
	if err := c.recordRollbackHistory(types.Application{Origin: testOrigin, ParsedLayers: []string{"rollback"}}); err != nil {
		t.Fatalf("record history: %v", err)
	}
	if err := c.collectGarbage([]types.Application{{ParsedLayers: []string{"current"}}}, true); err != nil {
		t.Fatalf("collect garbage: %v", err)
	}
	for _, layer := range []string{"current", "rollback"} {
		if _, err := os.Stat(c.GetInStoreDir("layers", layer)); err != nil {
			t.Fatalf("referenced layer was removed: %s: %v", layer, err)
		}
		if _, err := os.Stat(c.fvsLayerPath(layer)); !os.IsNotExist(err) {
			t.Fatalf("garbage collection migrated layer %s: %v", layer, err)
		}
	}
	if _, err := os.Stat(c.GetInStoreDir("layers", "orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan layer still exists: %v", err)
	}
}
