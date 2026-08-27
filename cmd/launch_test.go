/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"reflect"
	"strings"
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

func TestLaunchRefusesAnUnavailableSandbox(t *testing.T) {
	err := sandboxOutcome(sandbox.ErrUnavailable, "Landlock", "enable it")
	if err == nil {
		t.Fatal("an application started without Landlock")
	}
	for _, expected := range []string{"Landlock", "not started", "enable it"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("the refusal does not say %q: %v", expected, err)
		}
	}
}

// A failure that is not the kernel declining the feature is a failure. Warning
// it away would hide a sandbox that could have been established and was not.
func TestARealSandboxFailureIsNotWarnedAway(t *testing.T) {
	refused := errors.New("install seccomp filter: operation not permitted")
	err := sandboxOutcome(refused, "Seccomp", "enable it")
	if !errors.Is(err, refused) {
		t.Fatalf("outcome: got %v, want %v", err, refused)
	}
}
