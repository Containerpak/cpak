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

func TestSafeCgroupName(t *testing.T) {
	if got := safeCgroupName("app:instance/one"); got != "app_instance_one" {
		t.Fatalf("unexpected cgroup name %q", got)
	}
	if got := safeCgroupName(""); got != "application" {
		t.Fatalf("unexpected empty cgroup name %q", got)
	}
}

func TestRequestedControllers(t *testing.T) {
	got := requestedControllers(types.Override{MemoryMaxMB: 512, PidsMax: 256})
	if !reflect.DeepEqual(got, []string{"memory", "pids"}) {
		t.Fatalf("unexpected controllers: %v", got)
	}
}
