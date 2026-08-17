/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func usePrefixes(t *testing.T, prefixes ...string) {
	t.Helper()
	original := installPrefixes
	installPrefixes = prefixes
	t.Cleanup(func() { installPrefixes = original })
}

func TestWritableLayoutSkipsUnusablePrefix(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	fallback := filepath.Join(root, "fallback")
	if err := os.WriteFile(blocked, nil, 0644); err != nil {
		t.Fatal(err)
	}
	usePrefixes(t, blocked, fallback)
	target, err := writableLayout()
	if err != nil {
		t.Fatal(err)
	}
	if target.prefix != fallback {
		t.Fatalf("got prefix %s, want the usable one %s", target.prefix, fallback)
	}
	if _, err := os.Stat(filepath.Join(fallback, "bin")); err != nil {
		t.Fatalf("the chosen prefix was not prepared: %v", err)
	}
}

func TestWritableLayoutSkipsReadOnlyPrefix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	fallback := filepath.Join(root, "fallback")
	if err := os.MkdirAll(filepath.Join(blocked, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(blocked, "bin"), 0555); err != nil {
		t.Fatal(err)
	}
	usePrefixes(t, blocked, fallback)
	target, err := writableLayout()
	if err != nil {
		t.Fatal(err)
	}
	if target.prefix != fallback {
		t.Fatalf("got prefix %s, want the writable one %s", target.prefix, fallback)
	}
}

func TestWritableLayoutReportsWhenNoPrefixAccepts(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0644); err != nil {
		t.Fatal(err)
	}
	usePrefixes(t, blocked)
	if _, err := writableLayout(); err == nil {
		t.Fatal("an unusable prefix was reported as an install target")
	}
}

func TestRelocatedLayoutStaysInScannedDirectories(t *testing.T) {
	relocated := layoutFor("/opt/cpak")
	if relocated.polkit != filepath.Join(polkitActionsPath, polkitPolicyName) {
		t.Fatalf("the relocated polkit action is not where polkitd looks: %s", relocated.polkit)
	}
	if relocated.serviceDirectory() != "/opt/cpak/share/dbus-1/system-services" {
		t.Fatalf("the relocated service directory is not published: %s", relocated.serviceDirectory())
	}
	standard := layoutFor(standardPrefix)
	if standard.polkit != "/usr/local/share/polkit-1/actions/it.cpak.system.policy" {
		t.Fatalf("the standard install moved its polkit action: %s", standard.polkit)
	}
	if standard.serviceDirectory() != "" {
		t.Fatalf("the standard install declares a redundant service directory: %s", standard.serviceDirectory())
	}
}

func TestRenderedIntegrationFollowsTheResolvedPrefix(t *testing.T) {
	relocated := layoutFor("/opt/cpak")
	service := string(renderAsset(serviceFile, "@BINARY@", relocated.binary))
	if !strings.Contains(service, "Exec=/opt/cpak/bin/cpak system-authority\n") {
		t.Fatalf("the bus service does not activate the installed binary: %s", service)
	}
	policy := string(renderAsset(busPolicy, "@SERVICEDIR@", servicedirElement(relocated)))
	if !strings.Contains(policy, "<servicedir>/opt/cpak/share/dbus-1/system-services</servicedir>\n") {
		t.Fatalf("the bus policy does not publish the relocated service directory: %s", policy)
	}
	standard := string(renderAsset(busPolicy, "@SERVICEDIR@", servicedirElement(layoutFor(standardPrefix))))
	if strings.Contains(standard, "<servicedir>") {
		t.Fatalf("the standard install adds a service directory it does not need: %s", standard)
	}
	for name, rendered := range map[string]string{"service": service, "policy": policy, "standard policy": standard} {
		if strings.Contains(rendered, "@") {
			t.Fatalf("the %s kept an unrendered placeholder: %s", name, rendered)
		}
	}
}

func TestSessionSearchPathKeepsSystemDirectories(t *testing.T) {
	relocated := layoutFor("/opt/cpak").sessionSearchPath(sddmSessions)
	want := "/opt/cpak/share/wayland-sessions:/usr/local/share/wayland-sessions:" + DefaultSystemSessions
	if relocated != want {
		t.Fatalf("got %s, want %s", relocated, want)
	}
	standard := layoutFor(standardPrefix).sessionSearchPath(sddmSessions)
	if standard != "/usr/local/share/wayland-sessions:"+DefaultSystemSessions {
		t.Fatalf("the standard search path repeats or drops a directory: %s", standard)
	}
}
