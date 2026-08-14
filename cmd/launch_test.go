/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"reflect"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/sandbox"
)

func TestLaunchGrantsKeepWriteFilesAccess(t *testing.T) {
	command := LaunchCmd{
		LandlockReadOnly:   []string{"/"},
		LandlockWriteFiles: []string{"/proc"},
		LandlockReadWrite:  []string{"/tmp"},
	}
	want := []sandbox.PathGrant{
		{Path: "/", ReadOnly: true},
		{Path: "/proc", ReadOnly: true, WriteFiles: true},
		{Path: "/tmp"},
	}
	if got := command.landlockGrants(); !reflect.DeepEqual(got, want) {
		t.Fatalf("landlock grants: got %+v, want %+v", got, want)
	}
}
