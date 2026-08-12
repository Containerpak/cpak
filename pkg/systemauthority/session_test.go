/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systemauthority

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRegistry(t *testing.T) Registry {
	t.Helper()
	root := t.TempDir()
	return Registry{
		RegistryDirectory: filepath.Join(root, "registry"),
		SessionDirectory:  filepath.Join(root, "wayland-sessions"),
		SystemSessions:    filepath.Join(root, "system-sessions"),
		LauncherPath:      "/usr/local/bin/cpak",
		OwnerUID:          uint32(os.Getuid()),
	}
}

func testSession() Session {
	return Session{
		ID:          "dev.sinty.singularity",
		Origin:      "github.com/singularityos-lab/singularity-desktop",
		Name:        "Singularity",
		Description: "Singularity Desktop Environment",
		Kind:        "desktop",
	}
}

func TestRegistryWritesFixedSessionLauncher(t *testing.T) {
	registry := testRegistry(t)
	session := testSession()
	if err := registry.Register(session); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(registry.SessionDirectory, session.ID+".desktop"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "Exec=/usr/local/bin/cpak session launch dev.sinty.singularity\n") {
		t.Fatalf("session does not use the fixed launcher: %s", text)
	}
	if strings.Contains(text, session.Origin+" ") {
		t.Fatal("package origin was added to the executable command")
	}
	loaded, err := registry.Load(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != session {
		t.Fatalf("got %+v, want %+v", loaded, session)
	}
}

func TestRegistryRejectsDesktopEntryInjection(t *testing.T) {
	registry := testRegistry(t)
	session := testSession()
	session.Name = "Singularity\nExec=/bin/sh"
	if err := registry.Register(session); err == nil {
		t.Fatal("accepted a desktop entry injection")
	}
	if _, err := os.Stat(filepath.Join(registry.SessionDirectory, session.ID+".desktop")); !os.IsNotExist(err) {
		t.Fatal("invalid session was written")
	}
}

func TestRegistryRejectsPathTraversal(t *testing.T) {
	registry := testRegistry(t)
	session := testSession()
	session.ID = "../singularity"
	if err := registry.Register(session); err == nil {
		t.Fatal("accepted a session path traversal")
	}
	if err := registry.Remove("../singularity", session.Origin); err == nil {
		t.Fatal("accepted a removal path traversal")
	}
}

func TestRegistryRejectsSessionIdentifierOwnedByAnotherPackage(t *testing.T) {
	registry := testRegistry(t)
	session := testSession()
	if err := registry.Register(session); err != nil {
		t.Fatal(err)
	}
	other := session
	other.Origin = "github.com/example/desktop"
	if err := registry.Register(other); err == nil {
		t.Fatal("replaced a session owned by another package")
	}
	if err := registry.Remove(session.ID, other.Origin); err == nil {
		t.Fatal("removed a session owned by another package")
	}
}

func TestRegistryRejectsUntrustedDirectory(t *testing.T) {
	registry := testRegistry(t)
	if err := os.MkdirAll(registry.RegistryDirectory, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registry.RegistryDirectory, 0777); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testSession()); err == nil {
		t.Fatal("accepted a writable registry directory")
	}
}

func TestRegistryMergesTrustedSystemSessions(t *testing.T) {
	registry := testRegistry(t)
	if err := os.MkdirAll(registry.SystemSessions, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(registry.SystemSessions, "existing.desktop")
	if err := os.WriteFile(source, []byte("[Desktop Entry]\nName=Existing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := registry.Prepare(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(registry.SessionDirectory, "existing.desktop")
	linked, err := os.Readlink(target)
	if err != nil {
		t.Fatal(err)
	}
	if linked != source {
		t.Fatalf("system session link points to %s", linked)
	}

	session := testSession()
	session.ID = "existing"
	if err := registry.Register(session); err == nil {
		t.Fatal("cpak session replaced an existing system session")
	}
}

func TestRegistryPurgeRemovesOnlyRegisteredSessions(t *testing.T) {
	registry := testRegistry(t)
	if err := registry.Register(testSession()); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(registry.SessionDirectory, "unrelated.desktop")
	if err := os.WriteFile(unrelated, []byte("[Desktop Entry]\nName=Unrelated\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := registry.Purge(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(registry.RegistryDirectory, testSession().ID+".json")); !os.IsNotExist(err) {
		t.Fatal("registered session remains after purge")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated session was removed: %v", err)
	}
}
