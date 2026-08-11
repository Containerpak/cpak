/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestResolveApplicationOriginUsesAlias(t *testing.T) {
	installation := t.TempDir()
	t.Setenv("CPAK_INSTALLATION_PATH", installation)
	cp, err := cpak.NewCpak()
	if err != nil {
		t.Fatal(err)
	}
	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.NewApplication(types.Application{CpakId: "bottles", Origin: "github.com/containerpak/bottles", Version: "main", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cp.SetAlias("bottles", "github.com/containerpak/bottles"); err != nil {
		t.Fatal(err)
	}
	origin, err := resolveApplicationOrigin(cp, "bottles")
	if err != nil {
		t.Fatal(err)
	}
	if origin != "github.com/containerpak/bottles" {
		t.Fatalf("origin: %s", origin)
	}
}
