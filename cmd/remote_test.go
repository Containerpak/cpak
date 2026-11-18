/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

func TestRemoteCommandRefusesWhatItCannotDo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CPAK_INSTALLATION_PATH", t.TempDir())
	var output bytes.Buffer
	base := cli.Base{Logger: clilog.NewWriter(&output, &output)}
	if err := (&RemoteCmd{Action: "remove", Name: "flathub", Base: base}).Run(); err == nil || !strings.Contains(err.Error(), "built in") {
		t.Fatalf("the built-in remote was removed: %v", err)
	}
	if err := (&RemoteCmd{Action: "add", Name: "example", Base: base}).Run(); err == nil {
		t.Fatal("a remote was added without a description")
	}
	if err := (&RemoteCmd{Action: "add", Name: "example", Location: "http://example.org/x.flatpakrepo", Base: base}).Run(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("a description over plain HTTP was read: %v", err)
	}
	if err := (&RemoteCmd{Action: "rename", Base: base}).Run(); err == nil {
		t.Fatal("an unknown action was accepted")
	}
	if err := (&RemoteCmd{Action: "list", Base: base}).Run(); err != nil {
		t.Fatal(err)
	}
}
