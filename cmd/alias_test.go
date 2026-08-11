/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"bytes"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

func TestAliasCommandSetAndRemove(t *testing.T) {
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
	var output bytes.Buffer
	base := cli.Base{Logger: clilog.NewWriter(&output, &output)}
	if err = (&AliasCmd{Action: "set", Name: "bottles", Origin: "github.com/containerpak/bottles", Base: base}).Run(); err != nil {
		t.Fatal(err)
	}
	if err = (&AliasCmd{Action: "remove", Name: "bottles", Base: base}).Run(); err != nil {
		t.Fatal(err)
	}
}
