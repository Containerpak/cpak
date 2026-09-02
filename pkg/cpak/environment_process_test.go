/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"reflect"
	"testing"
)

func TestProcessIDParsingRejectsInvalidAndOverflowingValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "2147483648", "not-a-pid"} {
		if _, _, ok := parseProcessID(value); ok {
			t.Fatalf("accepted process ID %q", value)
		}
	}
	pid, processID, ok := parseProcessID("2147483647")
	if !ok || pid != 2147483647 || processID != 2147483647 {
		t.Fatalf("largest process ID: got %d, %d, %v", pid, processID, ok)
	}
}

func TestEnvironmentSignalNamesAreStable(t *testing.T) {
	want := []string{"CONT", "HUP", "INT", "KILL", "STOP", "TERM", "USR1", "USR2"}
	if got := EnvironmentSignalNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment signal names: got %v, want %v", got, want)
	}
}
