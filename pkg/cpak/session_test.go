/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestDisableRegisteredSessionsOnlyRemovesOwnedEntries(t *testing.T) {
	root := t.TempDir()
	registry := systemauthority.Registry{
		RegistryDirectory: filepath.Join(root, "registry"),
		SessionDirectory:  filepath.Join(root, "sessions"),
		LauncherPath:      "/usr/local/bin/cpak",
		OwnerUID:          uint32(os.Getuid()),
	}
	owned := systemauthority.Session{
		ID:     "dev.sinty.singularity",
		Origin: "github.com/singularityos-lab/singularity-desktop",
		Name:   "Singularity",
		Kind:   "desktop",
	}
	foreign := systemauthority.Session{
		ID:     "com.example.desktop",
		Origin: "github.com/example/desktop",
		Name:   "Example",
		Kind:   "desktop",
	}
	if err := registry.Register(owned); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(foreign); err != nil {
		t.Fatal(err)
	}
	removed := []string{}
	err := disableRegisteredSessions(registry, owned.Origin, []types.Session{
		{ID: owned.ID},
		{ID: foreign.ID},
		{ID: "org.example.missing"},
	}, func(id, origin string) error {
		removed = append(removed, id+":"+origin)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != owned.ID+":"+owned.Origin {
		t.Fatalf("unexpected session removals: %v", removed)
	}
}

func TestSessionsRemovedByVersionSelectionKeepsOtherVersions(t *testing.T) {
	apps := []types.Application{
		{Branch: "main", ParsedSessions: []types.Session{{ID: "dev.sinty.main"}}},
		{Branch: "preview", ParsedSessions: []types.Session{{ID: "dev.sinty.preview"}}},
	}
	sessions, remaining := sessionsRemovedByVersionSelection(apps, "main", "", "")
	if remaining != 1 || len(sessions) != 1 || sessions[0].ID != "dev.sinty.main" {
		t.Fatalf("unexpected selection: sessions=%v remaining=%d", sessions, remaining)
	}
}

func TestSessionsRemovedByVersionSelectionKeepsSharedSession(t *testing.T) {
	apps := []types.Application{
		{Branch: "main", ParsedSessions: []types.Session{{ID: "dev.sinty.singularity"}, {ID: "dev.sinty.main"}}},
		{Branch: "preview", ParsedSessions: []types.Session{{ID: "dev.sinty.singularity"}}},
	}
	sessions, remaining := sessionsRemovedByVersionSelection(apps, "main", "", "")
	if remaining != 1 || len(sessions) != 1 || sessions[0].ID != "dev.sinty.main" {
		t.Fatalf("unexpected selection: sessions=%v remaining=%d", sessions, remaining)
	}
}
