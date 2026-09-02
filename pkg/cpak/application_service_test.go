/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"reflect"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestApplicationServiceProvidesDefaultCommand(t *testing.T) {
	app := types.Application{Name: "Demo", ParsedServices: map[string]types.ApplicationService{
		"server": {Binary: "/usr/bin/demo", Arguments: []string{"serve", "--port", "3000"}},
	}}
	binary, arguments, err := applicationServiceCommand(app, "server", "", []string{"--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if binary != "/usr/bin/demo" || !reflect.DeepEqual(arguments, []string{"serve", "--port", "3000", "--verbose"}) {
		t.Fatalf("command: %s %v", binary, arguments)
	}
}

func TestApplicationServiceRejectsAnExplicitBinary(t *testing.T) {
	app := types.Application{Name: "Demo", ParsedServices: map[string]types.ApplicationService{
		"server": {Binary: "/usr/bin/demo"},
	}}
	if _, _, err := applicationServiceCommand(app, "server", "/usr/bin/other", nil); err == nil {
		t.Fatal("explicit binary was accepted with an application service")
	}
}
