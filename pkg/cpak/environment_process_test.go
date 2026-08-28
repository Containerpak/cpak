/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"reflect"
	"testing"
)

func TestEnvironmentSignalNamesAreStable(t *testing.T) {
	want := []string{"CONT", "HUP", "INT", "KILL", "STOP", "TERM", "USR1", "USR2"}
	if got := EnvironmentSignalNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment signal names: got %v, want %v", got, want)
	}
}
