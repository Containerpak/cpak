/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dbus-daemon on a systemd host is started with --systemd-activation, and in
// that mode it never runs Exec= itself: it asks systemd for the unit the
// service file names. A service file without that key is listed as activatable
// and then refuses to activate, which is worse than not being installed at
// all, because everything looks configured.
func TestTheBusServiceNamesAUnitSystemdCanActivate(t *testing.T) {
	rendered := string(renderAsset(serviceFile, "@BINARY@", "/opt/cpak/bin/cpak"))
	for _, required := range []string{
		"Name=it.cpak.SystemAuthority1",
		"SystemdService=cpak-system-authority.service",
		"Exec=/opt/cpak/bin/cpak system-authority",
		"User=root",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("the bus service file does not carry %q:\n%s", required, rendered)
		}
	}
}

// Exec stays for hosts whose bus activates services itself, which is every
// init that is not systemd. Dropping it would tie the authority to systemd
// through the back door.
func TestTheBusServiceStillWorksWhereThereIsNoSystemd(t *testing.T) {
	rendered := string(renderAsset(serviceFile, "@BINARY@", "/usr/local/bin/cpak"))
	if !strings.Contains(rendered, "Exec=/usr/local/bin/cpak system-authority") {
		t.Fatal("the bus service file cannot be activated without systemd")
	}
}

// The unit and the service file have to name the same program, or the bus
// activates something other than what was installed.
func TestTheUnitRunsTheBinaryThatWasInstalled(t *testing.T) {
	binary := "/opt/cpak/bin/cpak"
	unit := string(renderAsset(systemdUnit, "@BINARY@", binary))
	for _, required := range []string{
		"Type=dbus",
		"BusName=it.cpak.SystemAuthority1",
		"ExecStart=" + binary + " system-authority",
	} {
		if !strings.Contains(unit, required) {
			t.Fatalf("the unit does not carry %q:\n%s", required, unit)
		}
	}
	service := string(renderAsset(serviceFile, "@BINARY@", binary))
	if !strings.Contains(service, "SystemdService=cpak-system-authority.service") {
		t.Fatal("the service file names no unit")
	}
	if !strings.Contains(unit, "BusName=it.cpak.SystemAuthority1") ||
		!strings.Contains(service, "Name=it.cpak.SystemAuthority1") {
		t.Fatal("the unit and the service file disagree about the bus name")
	}
}

// A host that is not running systemd must not have a unit written into it.
func TestNoUnitIsWrittenWhereSystemdIsNotInCharge(t *testing.T) {
	if systemdIsRunning() {
		t.Skip("systemd is in charge here")
	}
	called := false
	saved := reloadSystemd
	t.Cleanup(func() { reloadSystemd = saved })
	reloadSystemd = func() error { called = true; return nil }
	if err := installSystemdUnit(layoutFor("/opt/cpak")); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("systemd was asked to reload on a host it does not run")
	}
}

// Removing something that is not installed must not reach for a prefix nobody
// used. It reported a read-only /usr/local on a host whose installation had
// gone to /opt/cpak and had already been removed, which reads as a broken
// uninstall rather than as nothing to do.
func TestRemovingWhatIsNotInstalledSaysSoInsteadOfFailingElsewhere(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Uninstall refuses before it decides anything when it is not root")
	}
	if _, found := installedLayout(); found {
		t.Skip("this host has an installation, so there is something to remove")
	}
	err := Uninstall()
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("removing nothing answered %v", err)
	}
}

// Purging is the removal path, and a removal that creates the directory it is
// about to empty cannot run on a host whose prefix is read-only.
func TestPurgingCreatesNothing(t *testing.T) {
	root := t.TempDir()
	registry := Registry{
		RegistryDirectory: filepath.Join(root, "registry"),
		SessionDirectory:  filepath.Join(root, "sessions"),
		SystemSessions:    filepath.Join(root, "system"),
		LauncherPath:      filepath.Join(root, "cpak"),
		OwnerUID:          uint32(os.Getuid()),
	}
	if err := registry.Purge(); err != nil {
		t.Fatalf("purging a registry that was never created failed: %v", err)
	}
	for _, path := range []string{registry.RegistryDirectory, registry.SessionDirectory} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("the removal created %s", path)
		}
	}
}
