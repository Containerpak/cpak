/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestBuildContainerPath(t *testing.T) {
	cases := []struct {
		name     string
		imageEnv []string
		contains []string
	}{
		{
			name:     "no image path",
			imageEnv: []string{"LANG=C"},
			contains: []string{"/usr/local/bin", "/usr/bin", "/bin"},
		},
		{
			name:     "image path is kept",
			imageEnv: []string{"LANG=C", "PATH=/opt/app/bin:/usr/bin"},
			contains: []string{"/usr/local/bin", "/opt/app/bin", "/usr/bin"},
		},
		{
			name:     "the last image path wins",
			imageEnv: []string{"PATH=/first", "PATH=/second"},
			contains: []string{"/second"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := buildContainerPath(tc.imageEnv)
			entries := strings.Split(path, ":")

			if entries[0] != "/usr/local/bin" {
				t.Fatalf("the path does not start with /usr/local/bin: %q", path)
			}
			for _, want := range tc.contains {
				if !slicesContain(entries, want) {
					t.Fatalf("the path is missing %s: %q", want, path)
				}
			}

			seen := map[string]bool{}
			for _, entry := range entries {
				if entry == "" {
					t.Fatalf("the path has an empty entry: %q", path)
				}
				if seen[entry] {
					t.Fatalf("the path repeats %s: %q", entry, path)
				}
				seen[entry] = true
			}
		})
	}
}

func TestForcedRuntimeTerminalSurvivesProxiedIO(t *testing.T) {
	t.Setenv(systembroker.TerminalRowsEnvironment, "41")
	t.Setenv(systembroker.TerminalColumnsEnvironment, "132")
	requested, _, rows, columns := runtimeTerminalSize(true)
	if !requested {
		t.Fatal("a forced terminal session was not requested")
	}
	if rows != 41 || columns != 132 {
		t.Fatalf("forced terminal size: %dx%d", columns, rows)
	}
}

func TestForcedRuntimeTerminalRejectsIncompleteSize(t *testing.T) {
	t.Setenv(systembroker.TerminalRowsEnvironment, "41")
	t.Setenv(systembroker.TerminalColumnsEnvironment, "")
	rows, columns := proxiedTerminalSize()
	if rows != 24 || columns != 80 {
		t.Fatalf("fallback terminal size: %dx%d", columns, rows)
	}
}

func TestRuntimeTerminalEnvironmentReplacesDumbTerminal(t *testing.T) {
	values := runtimeTerminalEnvironment(
		[]string{"PATH=/usr/bin", "TERM=dumb", "TERM=linux"},
		"dumb",
		"truecolor",
	)
	if got := environmentValue(values, "TERM"); got != "xterm-256color" {
		t.Fatalf("terminal type: %q", got)
	}
	if got := environmentValue(values, "COLORTERM"); got != "truecolor" {
		t.Fatalf("color terminal type: %q", got)
	}
}

func TestContainerScopeLockSerializesTheSameApplication(t *testing.T) {
	cp := Cpak{Options: Options{StorePath: t.TempDir()}}
	firstUnlock, err := cp.lockContainerScope("application")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { firstUnlock() })

	acquired := make(chan func(), 1)
	go func() {
		unlock, lockErr := cp.lockContainerScope("application")
		if lockErr != nil {
			acquired <- nil
			return
		}
		acquired <- unlock
	}()

	select {
	case unlock := <-acquired:
		if unlock != nil {
			unlock()
		}
		t.Fatal("the second lock acquired the same application scope")
	case <-time.After(100 * time.Millisecond):
	}

	firstUnlock()
	firstUnlock = func() {}
	select {
	case unlock := <-acquired:
		if unlock == nil {
			t.Fatal("the second lock failed")
		}
		unlock()
	case <-time.After(time.Second):
		t.Fatal("the second lock did not acquire the released scope")
	}
}

func TestMountPersistentFileGrantsRestoresStoredGrant(t *testing.T) {
	storePath := t.TempDir()
	selected := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(selected, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	grant, err := filegrant.Resolve("github.com/example/viewer", selected, filegrant.AccessReadOnly, filegrant.LifetimePersistent, false)
	if err != nil {
		t.Fatal(err)
	}
	store := filegrant.Store{Directory: filepath.Join(storePath, "grants")}
	if err = store.Add(grant); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "grant.sock")
	listener, err := net.Listen("unixpacket", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	restored := make(chan filegrant.Grant, 1)
	go func() {
		accepted, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		connection := accepted.(*net.UnixConn)
		defer connection.Close()
		request, sources, receiveErr := grantproto.Receive(connection)
		if receiveErr != nil {
			return
		}
		sources.Close()
		restored <- request.Grant
		_ = grantproto.Reply(connection, grantproto.Response{Target: request.Grant.Target})
	}()
	cp := Cpak{Options: Options{StorePath: storePath}}
	container := types.Container{GrantSocketPath: socket, StatePath: t.TempDir()}
	if err = cp.mountPersistentFileGrants(grant.Origin, container); err != nil {
		t.Fatal(err)
	}
	if got := <-restored; got != grant {
		t.Fatalf("restored grant: %+v", got)
	}
}

// Nothing expands variables in an argv, a literal $PATH is only a directory
// that does not exist taking the place of the real one.
func TestBuildContainerPathDropsUnexpandedVariables(t *testing.T) {
	path := buildContainerPath([]string{"PATH=/opt/bin:$PATH"})

	if strings.Contains(path, "$") {
		t.Fatalf("the path carries an unexpanded variable: %q", path)
	}
	if !slicesContain(strings.Split(path, ":"), "/opt/bin") {
		t.Fatalf("the path lost the directory of the image: %q", path)
	}
	if !slicesContain(strings.Split(path, ":"), "/usr/bin") {
		t.Fatalf("the path lost the standard directories: %q", path)
	}
}

func TestGetNested(t *testing.T) {
	original := nestedMarkerPath
	t.Cleanup(func() { nestedMarkerPath = original })

	marker := filepath.Join(t.TempDir(), ".cpak")
	nestedMarkerPath = marker

	if _, nested := getNested(); nested {
		t.Fatal("a container was detected without a marker")
	}

	cases := []struct {
		content string
		want    string
	}{
		{"cafe0123", "cafe0123"},
		{"cafe0123\n", "cafe0123"},
		{"  cafe0123  \n", "cafe0123"},
	}

	for _, tc := range cases {
		if err := os.WriteFile(marker, []byte(tc.content), 0644); err != nil {
			t.Fatalf("write the marker: %v", err)
		}
		token, nested := getNested()
		if !nested {
			t.Fatalf("the marker %q was not detected", tc.content)
		}
		if token != tc.want {
			t.Fatalf("capability: got %q, want %q", token, tc.want)
		}
	}

	// An empty marker is not a capability. It used to read as a container with
	// an empty parent identifier, which the service then looked up.
	if err := os.WriteFile(marker, nil, 0644); err != nil {
		t.Fatalf("write the marker: %v", err)
	}
	if _, nested := getNested(); nested {
		t.Fatal("an empty marker was read as a capability")
	}
}

func TestContainerEnvironmentKeepsImageAndOverrideValues(t *testing.T) {
	app := types.Application{
		Config: `{"config":{"Env":["IMAGE_VALUE=1"]}}`,
		ParsedOverride: types.Override{
			Env: []string{"OVERRIDE_VALUE=1"},
		},
	}
	container := types.Container{CpakId: "container-id"}

	env, err := containerEnvironment(app, resolvedOverride(app), container)
	if err != nil {
		t.Fatalf("container environment: %v", err)
	}

	for _, value := range []string{
		"IMAGE_VALUE=1",
		"OVERRIDE_VALUE=1",
		"CPAK_CONTAINER_ID=container-id",
	} {
		if !slicesContain(env, value) {
			t.Fatalf("missing %q in %v", value, env)
		}
	}
}

func TestOpenURIEnvironmentUsesPrivateDesktopDefaults(t *testing.T) {
	environment := openURIEnvironment([]string{
		"XDG_CURRENT_DESKTOP=GNOME",
		"XDG_DATA_DIRS=/opt/share",
		"XDG_CONFIG_DIRS=/opt/config",
	})
	if !slicesContain(environment, "XDG_CURRENT_DESKTOP=cpak:GNOME") {
		t.Fatalf("cpak desktop is missing: %v", environment)
	}
	if !slicesContain(environment, "XDG_DATA_DIRS=/usr/local/share:/opt/share") {
		t.Fatalf("URI handler data directory is missing: %v", environment)
	}
	if !slicesContain(environment, "XDG_CONFIG_DIRS=/usr/local/etc/xdg:/opt/config") {
		t.Fatalf("URI handler config directory is missing: %v", environment)
	}
}

func TestOpenURIEnvironmentKeepsTheHostDesktop(t *testing.T) {
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	environment := openURIEnvironment(nil)
	if !slicesContain(environment, "XDG_CURRENT_DESKTOP=cpak:GNOME") {
		t.Fatalf("host desktop is missing: %v", environment)
	}
}

func TestApplicationPtraceRequiresPrivateNestedNamespaces(t *testing.T) {
	if applicationPtraceAllowed(types.Override{}) {
		t.Fatal("ptrace is allowed without nested user namespaces")
	}
	if applicationPtraceAllowed(types.Override{UserNamespaces: true, Process: true}) {
		t.Fatal("ptrace is allowed with the host process namespace")
	}
	if !applicationPtraceAllowed(types.Override{UserNamespaces: true}) {
		t.Fatal("ptrace is blocked in a private nested sandbox")
	}
}

func TestApplicationMachineIDIsStableAndPrivate(t *testing.T) {
	cp := Cpak{Options: Options{StorePath: t.TempDir()}}
	first, err := cp.applicationMachineID("application-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cp.applicationMachineID("application-one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := cp.applicationMachineID("application-two")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("machine ID changed: %s != %s", first, second)
	}
	if first == other {
		t.Fatalf("applications share machine ID %s", first)
	}
	if len(first) != 32 {
		t.Fatalf("machine ID length: got %d", len(first))
	}
}

func TestEnsureOpenURIMimeAppsWritesDesktopSpecificDefaults(t *testing.T) {
	config := t.TempDir()
	if err := ensureOpenURIMimeApps([]string{"XDG_CONFIG_HOME=" + config}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config, "cpak-mimeapps.list")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != openURIMimeApps {
		t.Fatalf("mime defaults: got %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("mime defaults mode: got %o, want 644", info.Mode().Perm())
	}
}

func TestGetCpakBinaryUsesTheRunningExecutable(t *testing.T) {
	originalArg := os.Args[0]
	spoofed := filepath.Join(t.TempDir(), "cpak")
	if err := os.WriteFile(spoofed, []byte("not the running executable"), 0755); err != nil {
		t.Fatal(err)
	}
	os.Args[0] = spoofed
	t.Cleanup(func() { os.Args[0] = originalArg })
	t.Setenv("PATH", filepath.Dir(spoofed))

	got, err := getCpakBinary()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cpak binary: got %q, want %q", got, want)
	}
}

func TestTerminateContainerProcessRejectsAnUnrelatedPersistedPID(t *testing.T) {
	command := exec.Command("sleep", "30")
	command.Env = append(os.Environ(), "CPAK_CONTAINER_ID=other-container")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})

	started, err := processStartTime(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	terminateContainerProcess(types.Container{
		CpakId:           "expected-container",
		Pid:              command.Process.Pid,
		ProcessStartTime: started,
	})
	if err = syscall.Kill(command.Process.Pid, 0); err != nil {
		t.Fatalf("an unrelated process was signalled: %v", err)
	}
}

func TestRecordedProcessRequiresTheSameStartTime(t *testing.T) {
	started, err := processStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !sameRecordedProcess(os.Getpid(), started) {
		t.Fatal("the current process did not match its recorded start time")
	}
	if sameRecordedProcess(os.Getpid(), started+1) {
		t.Fatal("a stale process start time was accepted")
	}
}

func TestContainerPolicyHashChangesWithPermissions(t *testing.T) {
	first := types.NewOverride()
	second := first
	// Toggling rather than assigning, so the case keeps testing the hash and
	// not whatever the default for the field happens to be.
	second.Network = !first.Network
	firstHash, err := containerPolicyHash(first, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := containerPolicyHash(second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("different permission sets produced the same policy hash")
	}
}

func TestContainerPolicyHashChangesWithRuntime(t *testing.T) {
	override := types.NewOverride()
	first, err := containerPolicyHashVersion(1, override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := containerPolicyHashVersion(2, override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("different runtime versions produced the same policy hash")
	}
}

func TestContainerPolicyHashChangesWithWaylandEndpoint(t *testing.T) {
	override := types.Override{SocketWayland: true}
	first, err := containerLaunchPolicyHashWithWayland(containerRuntimePolicyVersion, "", override, nil, nil, waylandEndpoint{
		Display:    "wayland-0",
		SocketPath: "/run/user/1000/wayland-0",
		Device:     1,
		Inode:      2,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := containerLaunchPolicyHashWithWayland(containerRuntimePolicyVersion, "", override, nil, nil, waylandEndpoint{
		Display:    "wayland-0",
		SocketPath: "/run/user/1000/wayland-0",
		Device:     1,
		Inode:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("replaced Wayland sockets produced the same policy hash")
	}
	third, err := containerLaunchPolicyHashWithWayland(containerRuntimePolicyVersion, "", override, nil, nil, waylandEndpoint{
		Display:    "wayland-1",
		SocketPath: "/run/user/1000/wayland-1",
		Device:     1,
		Inode:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatal("different Wayland displays produced the same policy hash")
	}
}

func TestContainerEnvironmentIncludesSystemBrokerOnlyWhenAvailable(t *testing.T) {
	app := types.Application{Config: `{"config":{}}`}
	container := types.Container{
		CpakId:                 "container-id",
		SystemBrokerSocketPath: "/tmp/system-broker.sock",
	}
	env, err := containerEnvironment(app, resolvedOverride(app), container)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"CPAK_SYSTEM_BROKER_SOCKET=" + systemBrokerSocketTarget,
		"CPAK_SYSTEM_BROKER_TOKEN_FILE=" + systemBrokerTokenTarget,
	} {
		if !slicesContain(env, value) {
			t.Fatalf("missing %q in %v", value, env)
		}
	}
}

func TestContainerEnvironmentUsesThePrivateDesktopBusForFileSelection(t *testing.T) {
	app := types.Application{Config: `{"config":{}}`}
	container := types.Container{
		CpakId:               "container-id",
		DesktopBusSocketPath: "/tmp/desktop-bus.sock",
	}
	environment, err := containerEnvironment(app, resolvedOverride(app), container)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContain(environment, "DBUS_SESSION_BUS_ADDRESS=unix:path="+hostSessionBusPath()) {
		t.Fatalf("private desktop bus address is missing from %v", environment)
	}
	if !slicesContain(environment, "GTK_USE_PORTAL=1") {
		t.Fatalf("GTK file chooser integration is missing from %v", environment)
	}
}

func TestContainerEnvironmentUsesThePortalNetworkMonitor(t *testing.T) {
	app := types.Application{Config: `{"config":{}}`}
	container := types.Container{
		CpakId:               "container-id",
		DesktopBusSocketPath: "/tmp/desktop-bus.sock",
	}
	environment, err := containerEnvironment(app, types.Override{Network: true}, container)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + hostSessionBusPath(),
		"GIO_USE_PORTALS=1",
		"GIO_USE_NETWORK_MONITOR=portal",
	} {
		if !slicesContain(environment, value) {
			t.Fatalf("network monitor setting %q is missing from %v", value, environment)
		}
	}
}

func TestConfigureWaylandDisplayRequiresALiveSocket(t *testing.T) {
	environment := []string{"LANG=C", "WAYLAND_DISPLAY=stale"}
	configured := configureWaylandDisplay(environment, "wayland-7", filepath.Join(t.TempDir(), "missing"))
	if value := environmentValue(configured, "WAYLAND_DISPLAY"); value != "" {
		t.Fatalf("missing Wayland socket left display %q", value)
	}
	if !slicesContain(configured, "WAYLAND_DISPLAY=") {
		t.Fatalf("missing Wayland socket did not override the inherited display: %v", configured)
	}

	socket := filepath.Join(t.TempDir(), "wayland-7")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	configured = configureWaylandDisplay(environment, "wayland-7", socket)
	if value := environmentValue(configured, "WAYLAND_DISPLAY"); value != "wayland-7" {
		t.Fatalf("live Wayland display: got %q, want wayland-7", value)
	}
	configured = configureWaylandDisplay(environment, "", socket)
	if value := environmentValue(configured, "WAYLAND_DISPLAY"); value != "" {
		t.Fatalf("inactive Wayland session left display %q", value)
	}
	if !slicesContain(configured, "WAYLAND_DISPLAY=") {
		t.Fatalf("inactive Wayland session did not override the inherited display: %v", configured)
	}
}

func TestContainerEnvironmentKeepsTheMountedWaylandEndpoint(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "wayland-stored")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("WAYLAND_DISPLAY", "wayland-current")

	app := types.Application{Config: `{"config":{}}`}
	container := types.Container{
		CpakId:            "container-id",
		WaylandDisplay:    "wayland-stored",
		WaylandSocketPath: socket,
	}
	environment, err := containerEnvironment(app, types.Override{SocketWayland: true}, container)
	if err != nil {
		t.Fatal(err)
	}
	if got := environmentValue(environment, "WAYLAND_DISPLAY"); got != "wayland-stored" {
		t.Fatalf("Wayland display: got %q, want wayland-stored", got)
	}
}

func TestContainerEnvironmentUsesPrivateX11AndBluetoothEndpoints(t *testing.T) {
	app := types.Application{Config: `{"config":{"Env":["DISPLAY=:0","XAUTHORITY=/home/user/.Xauthority","DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket"]}}`}
	container := types.Container{
		CpakId:                 "container-id",
		BluetoothBusSocketPath: "/tmp/cpak/bluetooth.sock",
		X11SocketPath:          "/tmp/cpak/x11.sock",
		X11Display:             ":42",
	}
	environment, err := containerEnvironment(app, resolvedOverride(app), container)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"DBUS_SYSTEM_BUS_ADDRESS=unix:path=" + hostSystemBusPath(),
		"DISPLAY=:42",
		"XAUTHORITY=" + x11AuthorityTarget,
	} {
		if !slicesContain(environment, value) {
			t.Fatalf("private desktop endpoint %q is missing from %v", value, environment)
		}
	}
	for _, value := range []string{"DISPLAY=:0", "XAUTHORITY=/home/user/.Xauthority"} {
		if slicesContain(environment, value) {
			t.Fatalf("host desktop endpoint %q survived in %v", value, environment)
		}
	}
}

func TestLegacyBluetoothPermissionUsesThePrivateProxy(t *testing.T) {
	if !bluetoothProxyRequested(types.Override{SocketBluetooth: true}) {
		t.Fatal("the legacy Bluetooth permission did not request the private proxy")
	}
}

func TestDesktopBusProxyRequiresADeclaredCapability(t *testing.T) {
	if desktopBusProxyRequested(types.Override{}) {
		t.Fatal("an application without a desktop bus capability requested a proxy")
	}
	if !desktopBusProxyRequested(types.Override{FilePicker: types.FilePickerGrant{OpenFile: true}}) {
		t.Fatal("file selection did not request the desktop bus proxy")
	}
	if !desktopBusProxyRequested(types.Override{SessionBus: types.DBusPolicy{Own: []string{"org.example.App"}}}) {
		t.Fatal("a session bus policy did not request the desktop bus proxy")
	}
	if !desktopBusProxyRequested(types.Override{Network: true}) {
		t.Fatal("network access did not request the desktop bus proxy")
	}
}

func TestPrivateDesktopLinksMountOnlyProxyEndpoints(t *testing.T) {
	container := types.Container{
		BluetoothBusSocketPath: "/var/lib/cpak/state/bluetooth.sock",
		X11SocketPath:          "/var/lib/cpak/state/x11.sock",
		X11SocketTarget:        "/tmp/.X11-unix/X42",
		X11AuthorityPath:       "/var/lib/cpak/state/xauthority",
	}
	want := []string{
		"/var/lib/cpak/state/bluetooth.sock:/run/dbus/system_bus_socket",
		"/var/lib/cpak/state/x11.sock:/tmp/.X11-unix/X42",
		"/var/lib/cpak/state/xauthority:/run/cpak/xauthority",
	}
	if got := privateDesktopLinks(container); !reflect.DeepEqual(got, want) {
		t.Fatalf("private desktop links: got %v, want %v", got, want)
	}
}

func TestPrivateApplicationHomeIsPersistentAndRestricted(t *testing.T) {
	cp := Cpak{Options: Options{StorePath: t.TempDir()}}
	path, err := cp.privateApplicationHome("application-id")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cp.Options.StorePath, "application-data", "application-id", "home")
	if path != want {
		t.Fatalf("private application home: got %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("private application home mode: %v", info.Mode())
	}
	if filesystemIncludesHostHome(types.Override{}) || !filesystemIncludesHostHome(types.Override{Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-only"}}}) {
		t.Fatal("host home permission detection is invalid")
	}
}

func TestApplicationDataPathRejectsParentDirectory(t *testing.T) {
	cp := Cpak{Options: Options{StorePath: t.TempDir()}}
	if _, err := cp.applicationDataPath(".."); err == nil {
		t.Fatal("parent directory accepted as an application ID")
	}
}

// The fallback exists because an application told to use the accessibility bus
// hangs looking for one that is not there. Whether this machine runs a bus is
// not something the test can decide, so it asserts the implication in both
// directions instead of assuming the answer.
func TestContainerEnvironmentDisablesMissingAtSpiBridge(t *testing.T) {
	app := types.Application{
		Config:         `{"config":{}}`,
		ParsedOverride: types.Override{SocketAtSpiBus: true},
	}
	environment, err := containerEnvironment(app, resolvedOverride(app), types.Container{CpakId: "container-id"})
	if err != nil {
		t.Fatal(err)
	}
	hostHasBus := len(atSpiSocketPaths(fmt.Sprintf("%d", os.Getuid()))) > 0
	disabled := slicesContain(environment, "NO_AT_BRIDGE=1")
	if hostHasBus && disabled {
		t.Fatal("the bridge was disabled although this host runs an accessibility bus")
	}
	if !hostHasBus && !disabled {
		t.Fatalf("this host runs no accessibility bus and the bridge was left on: %v", environment)
	}
}

func TestContainerEnvironmentIncludesHostApplicationCatalog(t *testing.T) {
	app := types.Application{
		Config:         `{"config":{"Env":["XDG_DATA_DIRS=/opt/desktop/share:/usr/share"]}}`,
		ParsedOverride: types.Override{HostApplications: true},
	}
	environment, err := containerEnvironment(app, resolvedOverride(app), types.Container{CpakId: "container-id"})
	if err != nil {
		t.Fatal(err)
	}
	want := "XDG_DATA_DIRS=/run/cpak/host-applications/share:/opt/desktop/share:/usr/share"
	if !slicesContain(environment, want) {
		t.Fatalf("host application catalog is missing from %v", environment)
	}
	if !slicesContain(environment, "CPAK_HOST_OS_RELEASE="+hostOSReleaseTarget) {
		t.Fatalf("host OS release is missing from %v", environment)
	}
}

func TestHostApplicationCatalogPrecedesImageDataDirectories(t *testing.T) {
	environment := []string{"LANG=en_US.UTF-8", "XDG_DATA_DIRS=/opt/desktop/share:/usr/share"}
	got := prependEnvironmentPath(environment, "XDG_DATA_DIRS", "/run/cpak/host-applications/share", "/usr/local/share:/usr/share")
	if !slicesContain(got, "XDG_DATA_DIRS=/run/cpak/host-applications/share:/opt/desktop/share:/usr/share") {
		t.Fatalf("host application catalog is missing from %v", got)
	}
}

func TestInheritHostTimezoneRemovesImageTimezone(t *testing.T) {
	environment := []string{"LANG=en_US.UTF-8", "TZ=UTC", "PATH=/usr/bin"}
	got := inheritHostTimezone(environment)
	if slicesContain(got, "TZ=UTC") {
		t.Fatalf("image timezone was kept in %v", got)
	}
	if !slicesContain(got, "LANG=en_US.UTF-8") || !slicesContain(got, "PATH=/usr/bin") {
		t.Fatalf("unrelated environment was removed from %v", got)
	}
}

func TestInheritHostCursorKeepsApplicationValues(t *testing.T) {
	t.Setenv("XCURSOR_THEME", "HostTheme")
	t.Setenv("XCURSOR_SIZE", "48")
	environment := inheritHostCursor([]string{"XCURSOR_THEME=ApplicationTheme", "XCURSOR_SIZE=32"})
	if !slicesContain(environment, "XCURSOR_THEME=ApplicationTheme") || !slicesContain(environment, "XCURSOR_SIZE=32") {
		t.Fatalf("application cursor settings were replaced: %v", environment)
	}
	if slicesContain(environment, "XCURSOR_THEME=HostTheme") || slicesContain(environment, "XCURSOR_SIZE=48") {
		t.Fatalf("host cursor settings replaced application settings: %v", environment)
	}
}

func TestInheritHostCursorAddsHostValues(t *testing.T) {
	t.Setenv("XCURSOR_THEME", "Adwaita")
	t.Setenv("XCURSOR_SIZE", "24")
	environment := inheritHostCursor(nil)
	if !slicesContain(environment, "XCURSOR_THEME=Adwaita") || !slicesContain(environment, "XCURSOR_SIZE=24") {
		t.Fatalf("host cursor settings are missing: %v", environment)
	}
}

func TestSystemBrokerRuntimeUsesPrivateDirectory(t *testing.T) {
	runtimeDirectory := t.TempDir()
	stateDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	socketPath, tokenPath, err := createSystemBrokerRuntime(stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	container := types.Container{SystemBrokerSocketPath: socketPath, SystemBrokerTokenPath: tokenPath}
	t.Cleanup(func() { cleanupSystemBrokerRuntime(container) })
	if filepath.Dir(socketPath) != filepath.Join(runtimeDirectory, "cpak") || filepath.Dir(tokenPath) != stateDirectory {
		t.Fatalf("system broker paths escaped the runtime directory: %s %s", socketPath, tokenPath)
	}
	info, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("system broker runtime permissions: %o", info.Mode().Perm())
	}
}

func TestSystemBrokerRuntimeUsesOneSharedSocket(t *testing.T) {
	runtimeDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	firstSocket, firstToken, err := createSystemBrokerRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secondSocket, secondToken, err := createSystemBrokerRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if firstSocket != secondSocket {
		t.Fatalf("broker sockets differ: %s %s", firstSocket, secondSocket)
	}
	if firstToken == secondToken {
		t.Fatalf("container tokens share a path: %s", firstToken)
	}
}

func TestSystemBrokerPoliciesStayOutsideThePersistentStore(t *testing.T) {
	runtimeDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	directory, err := systemBrokerPolicyDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if directory != filepath.Join(runtimeDirectory, "cpak", "policies") {
		t.Fatalf("policy directory: %s", directory)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("policy directory permissions: %o", info.Mode().Perm())
	}
}

func TestSystemBrokerTokenCannotBeReplaced(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	if err := writeSystemBrokerToken(path); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemBrokerToken(path); err == nil {
		t.Fatal("existing system broker token was replaced")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("system broker token permissions: %o", info.Mode().Perm())
	}
}

func slicesContain(entries []string, want string) bool {
	for _, entry := range entries {
		if entry == want {
			return true
		}
	}
	return false
}

func TestContainerEnvironmentAppliesTheHostLocaleOverTheManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LANG", "pt_BR.UTF-8")
	t.Setenv("LC_ALL", "pt_BR.UTF-8")
	app := types.Application{
		Origin:         "github.com/containerpak/bottles",
		Version:        "66.1",
		Config:         `{"config":{"Env":["PATH=/usr/bin"]}}`,
		ParsedOverride: types.Override{Env: []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}},
	}
	environment, err := containerEnvironment(app, resolvedOverride(app), types.Container{CpakId: "container-id"})
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContain(environment, "LANG=pt_BR.UTF-8") {
		t.Fatalf("the host locale did not reach the application: %v", environment)
	}
	if slicesContain(environment, "LANG=C.UTF-8") || slicesContain(environment, "LC_ALL=C.UTF-8") {
		t.Fatalf("the locale pinned by the manifest survived: %v", environment)
	}
}
