/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

// A refusal that names a command nobody can type is the same dead end as a
// refusal that names nothing. pkg/cpak tells a user whose installation was
// refused over what a removal left behind to run cpak system clear-removal
// ORIGIN, so this is the other half of that sentence: the verb exists, it is
// reached by that exact name, and it says what it wants when it is given
// nothing.
func TestSystemClearRemovalIsReachableByTheNameARefusalNames(t *testing.T) {
	t.Setenv("CPAK_INSTALLATION_PATH", t.TempDir())
	var output bytes.Buffer
	base := cli.Base{Logger: clilog.NewWriter(&output, &output)}

	err := (&SystemCmd{Action: "clear-removal", Base: base}).Run()
	if err == nil {
		t.Fatal("clear-removal with nothing to clear was accepted")
	}
	if strings.Contains(err.Error(), "unsupported system action") {
		t.Fatalf("the verb a refusal names is not a verb: %v", err)
	}
	if !strings.Contains(err.Error(), "cpak system clear-removal ORIGIN") {
		t.Fatalf("got %v, want it to say what it takes", err)
	}

	// And it is named in the help of the command it belongs to, which is where
	// somebody who half remembers it looks.
	action, ok := reflect.TypeOf(SystemCmd{}).FieldByName("Action")
	if !ok {
		t.Fatal("cpak system takes no action")
	}
	if !strings.Contains(action.Tag.Get("help"), "clear-removal") {
		t.Fatalf("cpak system does not list clear-removal among its actions: %q", action.Tag.Get("help"))
	}
}
