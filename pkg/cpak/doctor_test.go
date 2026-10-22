/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/trustpolicy"
)

func TestCleanupLegacyRuntimeTools(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"nsenter", "rootlessctl", "rootlesskit", "rootlesskit-docker-proxy", "keep"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupLegacyRuntimeTools(directory)

	if _, err := os.Stat(filepath.Join(directory, "keep")); err != nil {
		t.Fatalf("unrelated tool was removed: %v", err)
	}
	for _, name := range []string{"nsenter", "rootlessctl", "rootlesskit", "rootlesskit-docker-proxy"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy tool %s remains", name)
		}
	}
}

func TestSecurityPostureRequiresTheLockdownSettingsTogether(t *testing.T) {
	strict := securityPostureCheck(
		systemauthority.EnforcementRefuse,
		systemauthority.SignaturesRequired,
		trustpolicy.Policy{ABI: trustpolicy.ABIVersion, ApprovedOrigins: []string{"github.com/example/app"}},
		[]AnchorState{{Origin: "github.com/example/app", Enrolled: true}},
	)
	if !strict.Available {
		t.Fatalf("strict posture was reported unavailable: %s", strict.Detail)
	}

	for name, check := range map[string]DoctorCheck{
		"enforcement off":     securityPostureCheck(systemauthority.EnforcementOff, systemauthority.SignaturesRequired, trustpolicy.Policy{ABI: trustpolicy.ABIVersion, ApprovedOrigins: []string{"github.com/example/app"}}, nil),
		"signatures optional": securityPostureCheck(systemauthority.EnforcementRefuse, systemauthority.SignaturesOptional, trustpolicy.Policy{ABI: trustpolicy.ABIVersion, ApprovedOrigins: []string{"github.com/example/app"}}, nil),
		"trust unrestricted":  securityPostureCheck(systemauthority.EnforcementRefuse, systemauthority.SignaturesRequired, trustpolicy.Policy{}, nil),
		"revocation only":     securityPostureCheck(systemauthority.EnforcementRefuse, systemauthority.SignaturesRequired, trustpolicy.Policy{ABI: trustpolicy.ABIVersion, Revoked: []trustpolicy.Revocation{{Origin: "github.com/example/app"}}}, nil),
	} {
		if check.Available {
			t.Fatalf("%s was reported as managed host lockdown: %s", name, check.Detail)
		}
	}
}

func TestSecurityPostureReportsUnenrolledApplications(t *testing.T) {
	check := securityPostureCheck(systemauthority.EnforcementOff, systemauthority.SignaturesOptional, trustpolicy.Policy{}, []AnchorState{
		{Origin: "github.com/example/one", Enrolled: true},
		{Origin: "github.com/example/two"},
	})
	if !strings.Contains(check.Detail, "unenrolled=1") {
		t.Fatalf("unenrolled count is missing: %s", check.Detail)
	}
}

func TestDoctorRequiredChecksHaveDetails(t *testing.T) {
	report := Doctor()
	for _, check := range report.Checks {
		if check.Name == "" || check.Detail == "" {
			t.Fatalf("incomplete check: %#v", check)
		}
	}
}

// fakeTools puts scripts named after the tools the overlay check runs ahead
// of the real ones, so both of its paths can be taken on any host.
func fakeTools(t *testing.T, tools map[string]string) {
	t.Helper()
	directory := t.TempDir()
	for name, script := range tools {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestOverlayCheckCountsFuseOverlayfs(t *testing.T) {
	fakeTools(t, map[string]string{
		"mount":          `echo "mount: permission denied" >&2; exit 32`,
		"fuse-overlayfs": `exit 0`,
		"umount":         `exit 0`,
	})
	check := checkOverlay()
	if !check.Available || !strings.Contains(check.Detail, "fuse-overlayfs succeeded") || !strings.Contains(check.Detail, "permission denied") {
		t.Fatalf("a refused kernel overlay with a working fuse-overlayfs reads as %+v", check)
	}
}

func TestOverlayCheckFailsWhenNeitherMountWorks(t *testing.T) {
	fakeTools(t, map[string]string{
		"mount":          `echo "mount: permission denied" >&2; exit 32`,
		"fuse-overlayfs": `echo "fuse: device not found" >&2; exit 1`,
	})
	check := checkOverlay()
	if check.Available || !strings.Contains(check.Detail, "permission denied") || !strings.Contains(check.Detail, "fuse: device not found") {
		t.Fatalf("an overlay neither way reads as %+v", check)
	}
}
