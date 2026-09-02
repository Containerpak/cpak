/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package appservice

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testDefinition(name string) Definition {
	return Definition{Name: name, Origin: "github.com/example/" + name, Restart: RestartOnFailure, Enabled: true}
}

func TestDefinitionAcceptsColonsInEnvironmentValues(t *testing.T) {
	definition := testDefinition("server")
	definition.Environment = []string{"DATABASE_URL=postgres://localhost:5432/app"}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreUsesPrivateFilesAndRestoresDefinitions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "services")
	store := Store{Directory: directory}
	definition := testDefinition("server")
	if err := store.Put(definition); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("store mode: %o", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(directory, "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("definition mode: %o", info.Mode().Perm())
	}
	loaded, err := store.Get("server")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, definition) {
		t.Fatalf("loaded definition: %#v", loaded)
	}
}

func TestOrderRejectsMissingDependenciesAndCycles(t *testing.T) {
	server := testDefinition("server")
	server.DependsOn = []string{"database"}
	if _, err := Order([]Definition{server}); err == nil {
		t.Fatal("missing dependency was accepted")
	}
	database := testDefinition("database")
	database.DependsOn = []string{"server"}
	if _, err := Order([]Definition{server, database}); err == nil {
		t.Fatal("dependency cycle was accepted")
	}
}

func TestOrderStartsDependenciesFirst(t *testing.T) {
	server := testDefinition("server")
	server.DependsOn = []string{"database"}
	database := testDefinition("database")
	order, err := Order([]Definition{server, database})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"database", "server"}) {
		t.Fatalf("order: %v", order)
	}
}

func TestStoreRejectsTrailingJSON(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "services")
	store := Store{Directory: directory}
	if err := store.Put(testDefinition("server")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "server.json")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get("server"); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("trailing JSON error: %v", err)
	}
}

func TestStoreRefusesToRemoveARequiredService(t *testing.T) {
	store := Store{Directory: filepath.Join(t.TempDir(), "services")}
	database := testDefinition("database")
	server := testDefinition("server")
	server.DependsOn = []string{"database"}
	if err := store.Put(database); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(server); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("database"); err == nil || !strings.Contains(err.Error(), "required by service server") {
		t.Fatalf("remove error: %v", err)
	}
	if _, err := store.Get("database"); err != nil {
		t.Fatal(err)
	}
}
