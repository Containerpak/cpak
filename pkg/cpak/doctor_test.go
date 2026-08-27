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
