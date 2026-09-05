/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/sandbox"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestCreateCpakFileSkipsContainersWithoutNestedDependencies(t *testing.T) {
	root := t.TempDir()
	if err := (&SpawnCmd{}).createCpakFile("", root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "tmp", ".cpak")); !os.IsNotExist(err) {
		t.Fatalf("empty capability left a nested marker: %v", err)
	}
}

func TestAbsoluteOverlayLowerDirsPreservesTheHostPaths(t *testing.T) {
	workingDir := filepath.Join(t.TempDir(), "layers")
	absolute := filepath.Join(t.TempDir(), "other", "rootfs")
	lowerDirs := strings.Join([]string{"first/rootfs", "second/rootfs", absolute}, ":")

	got := absoluteOverlayLowerDirs(workingDir, lowerDirs)
	want := strings.Join([]string{
		filepath.Join(workingDir, "first", "rootfs"),
		filepath.Join(workingDir, "second", "rootfs"),
		absolute,
	}, ":")
	if got != want {
		t.Fatalf("absolute lower directories: got %q, want %q", got, want)
	}
}

func TestWriteNvidiaLoaderConfigurationUsesSoname(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	destination := filepath.Join(root, "usr/share/vulkan/icd.d/nvidia.json")
	data := []byte(`{"ICD":{"library_path":"/usr/lib64/libGLX_nvidia.so.0"}}`)
	if err := os.WriteFile(source, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeNvidiaLoaderConfiguration(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ICD":{"library_path":"libGLX_nvidia.so.0"}}`
	if string(got) != want {
		t.Fatalf("rewritten loader config: got %s, want %s", got, want)
	}
}

func TestBaseSandboxGrantsLimitProcWritesToNestedSandboxes(t *testing.T) {
	for _, test := range []struct {
		name           string
		userNamespaces bool
		want           sandbox.PathGrant
	}{
		{name: "disabled", want: sandbox.PathGrant{Path: "/proc", ReadOnly: true}},
		{name: "enabled", userNamespaces: true, want: sandbox.PathGrant{Path: "/proc", ReadOnly: true, WriteFiles: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var proc sandbox.PathGrant
			for _, grant := range baseSandboxGrants(test.userNamespaces) {
				if grant.Path == "/proc" {
					proc = grant
					break
				}
			}
			if proc != test.want {
				t.Fatalf("proc grant: got %+v, want %+v", proc, test.want)
			}
		})
	}
}

func TestLoginSessionStateMountsIncludeAvailableSystemdState(t *testing.T) {
	hostRoot := t.TempDir()
	for _, path := range []string{"run/systemd/sessions", "run/systemd/users"} {
		if err := os.MkdirAll(filepath.Join(hostRoot, path), 0755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := loginSessionStateMounts(hostRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := []loginSessionStateMount{
		{source: filepath.Join(hostRoot, "run/systemd/sessions"), target: "/run/systemd/sessions"},
		{source: filepath.Join(hostRoot, "run/systemd/users"), target: "/run/systemd/users"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("login session state mounts: got %+v, want %+v", got, want)
	}
}

func TestLoginSessionStateMountsRejectSymlinks(t *testing.T) {
	hostRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hostRoot, "run/systemd"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(hostRoot, "run/systemd/sessions")); err != nil {
		t.Fatal(err)
	}
	if _, err := loginSessionStateMounts(hostRoot); err == nil {
		t.Fatal("symbolic logind state directory was accepted")
	}
}

func TestPrepareRootfsBindTargetReusesAnExistingMount(t *testing.T) {
	rootfs := t.TempDir()
	source := filepath.Join(t.TempDir(), "service.sock")
	if err := os.WriteFile(source, []byte("socket"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(rootfs, "run/cpak/service.sock")
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, destination); err != nil {
		t.Fatal(err)
	}
	actual, needsMount, err := prepareRootfsBindTarget(rootfs, "/run/cpak/service.sock", source)
	if err != nil {
		t.Fatal(err)
	}
	if actual != destination || needsMount {
		t.Fatalf("existing mount: path %s, needs mount %t", actual, needsMount)
	}
}

func TestSetEnvironmentVariablesIdentifiesContainer(t *testing.T) {
	env := setEnvironmentVariables("container-id", "/rootfs", []string{"LANG=C"}, "/state", "/layers", "base|")
	want := []string{
		"LANG=C",
		"CPAK_CONTAINER_ID=container-id",
		"CPAK_ROOTFS=/rootfs",
		"CPAK_STATE_DIR=/state",
		"CPAK_LAYERS_DIR=/layers",
		"CPAK_LAYERS=base|",
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("environment: got %v, want %v", env, want)
	}
}

func TestRefreshDynamicLinkerCacheDoesNotExecuteTheImageHelper(t *testing.T) {
	if _, err := os.Stat("/sbin/ldconfig"); os.IsNotExist(err) {
		t.Skip("host ldconfig is unavailable")
	}
	root := t.TempDir()
	for _, directory := range []string{"etc", "sbin", "var/cache"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "etc/ld.so.conf"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "image-helper-ran")
	payload := []byte("#!/bin/sh\ntouch " + marker + "\n")
	if err := os.WriteFile(filepath.Join(root, "sbin/ldconfig"), payload, 0755); err != nil {
		t.Fatal(err)
	}
	if err := refreshDynamicLinkerCache(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the image helper ran: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/ld.so.cache")); err != nil {
		t.Fatalf("the trusted helper did not refresh the cache: %v", err)
	}
}

func TestContainerHostnameIsStableAndPrivate(t *testing.T) {
	if os.Getenv("CPAK_HOSTNAME_TEST") == "1" {
		if err := setContainerHostname(); err != nil {
			t.Fatal(err)
		}
		if hostname, err := os.Hostname(); err != nil || hostname != "cpak" {
			t.Fatalf("container hostname: got %q, error %v", hostname, err)
		}
		return
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("unshare", "--user", "--map-root-user", "--uts", os.Args[0], "-test.run=^TestContainerHostnameIsStableAndPrivate$")
	command.Env = append(os.Environ(), "CPAK_HOSTNAME_TEST=1")
	if output, err := command.CombinedOutput(); err != nil {
		if bytes.Contains(output, []byte("Operation not permitted")) {
			t.Skip("user namespaces are unavailable")
		}
		t.Fatalf("hostname subprocess: %v\n%s", err, output)
	}
	if current, err := os.Hostname(); err != nil || current != hostname {
		t.Fatalf("host hostname changed: got %q, want %q, error %v", current, hostname, err)
	}
}

func TestLandlockArgumentsKeepAccessModes(t *testing.T) {
	got := landlockArguments([]sandbox.PathGrant{
		{Path: "/", ReadOnly: true},
		{Path: "/proc", ReadOnly: true, WriteFiles: true},
		{Path: "/tmp"},
	})
	want := []string{
		"--landlock-read-only", "/",
		"--landlock-write-files", "/proc",
		"--landlock-read-write", "/tmp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("landlock arguments: got %v, want %v", got, want)
	}
}

func TestGenerateMachineIDUsesAPrivateContainerIdentifier(t *testing.T) {
	machineID, err := generateMachineID(bytes.NewReader([]byte("0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	if machineID != "30313233343536373839616263646566" {
		t.Fatalf("machine ID: %s", machineID)
	}
}

func TestValidateMachineIDRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "ABCDEF0123456789ABCDEF0123456789", "00000000000000000000000000000000", "not-a-machine-id"} {
		if err := validateMachineID(value); err == nil {
			t.Fatalf("machine ID %q was accepted", value)
		}
	}
	if err := validateMachineID("30313233343536373839616263646566"); err != nil {
		t.Fatalf("valid machine ID was rejected: %v", err)
	}
}

func TestCreateSystemBrokerShimIsExecutable(t *testing.T) {
	rootfs := t.TempDir()
	command := &SpawnCmd{}
	if err := command.createSystemBrokerShimAndLinks(rootfs, []string{"notify-send", "xdg-open", "gio", "cpak-launch-app"}); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(rootfs, systemBrokerShimPath)
	info, err := os.Stat(shim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("shim mode: got %o, want 755", info.Mode().Perm())
	}
	for _, name := range []string{"notify-send", "xdg-open", "gio", "cpak-launch-app"} {
		link := filepath.Join(rootfs, "usr/local/bin", name)
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("system broker link %s: %v", name, err)
		}
	}
}

func TestInstallOpenURIHandlerCreatesPrivateDesktopEntry(t *testing.T) {
	rootfs := t.TempDir()
	command := &SpawnCmd{}
	if err := command.installOpenURIHandler(rootfs); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(rootfs, openURIHandlerDesktopPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"Exec=/usr/local/bin/xdg-open %u",
		"x-scheme-handler/http",
		"x-scheme-handler/https",
		"x-scheme-handler/mailto",
	} {
		if !strings.Contains(string(content), value) {
			t.Fatalf("desktop entry is missing %q: %s", value, content)
		}
	}
	defaults, err := os.ReadFile(filepath.Join(rootfs, openURIHandlerDefaultsPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(defaults) != openURIHandlerDefaults {
		t.Fatalf("desktop defaults: got %q", defaults)
	}
}

func TestRuntimePackageCommandUsesGuestDpkg(t *testing.T) {
	command := runtimePackageCommand([]string{"/run/cpak/runtime/demo.deb"})
	if command.Path != "/usr/bin/dpkg" {
		t.Fatalf("runtime installer path: %s", command.Path)
	}
	wantArgs := []string{"/usr/bin/dpkg", "--install", "/run/cpak/runtime/demo.deb"}
	if !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("runtime installer arguments: %v", command.Args)
	}
	if !slices.Contains(command.Env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin") {
		t.Fatalf("guest PATH is missing: %v", command.Env)
	}
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "CPAK_") {
			t.Fatalf("host cpak variable leaked into runtime installer: %s", entry)
		}
	}
}

func TestRuntimeDebExtractCommandUsesGuestDpkgDeb(t *testing.T) {
	command := runtimeDebExtractCommand("/run/cpak/runtime/demo.deb")
	if command.Path != "/usr/bin/dpkg-deb" {
		t.Fatalf("runtime extractor path: %s", command.Path)
	}
	wantArgs := []string{"/usr/bin/dpkg-deb", "--extract", "/run/cpak/runtime/demo.deb", "/"}
	if !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("runtime extractor arguments: %v", command.Args)
	}
	if !slices.Contains(command.Env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin") {
		t.Fatalf("guest PATH is missing: %v", command.Env)
	}
}

func TestRuntimeRPMPackageCommandUsesGuestRPM(t *testing.T) {
	command := runtimeRPMPackageCommand([]string{"/run/cpak/runtime/demo.rpm"})
	if command.Path != "/usr/bin/rpm" {
		t.Fatalf("runtime installer path: %s", command.Path)
	}
	wantArgs := []string{"/usr/bin/rpm", "--install", "--replacepkgs", "/run/cpak/runtime/demo.rpm"}
	if !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("runtime installer arguments: %v", command.Args)
	}
}

func TestInstallRuntimeArchive(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(t.TempDir(), "runtime.tar.gz")
	writeRuntimeArchive(t, archive, []*tar.Header{
		{Name: "usr/share/applications/engine.desktop", Mode: 0644, Size: int64(len("desktop entry")), Typeflag: tar.TypeReg},
	}, [][]byte{[]byte("desktop entry")})

	if err := installRuntimeArchive(root, archive); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "usr/share/applications/engine.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "desktop entry" {
		t.Fatalf("archive content: %q", content)
	}
}

func TestInstallRuntimeArchiveRejectsEscapes(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []*tar.Header
		content [][]byte
	}{
		{
			name:    "parent path",
			headers: []*tar.Header{{Name: "../escape", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}},
			content: [][]byte{[]byte("x")},
		},
		{
			name: "symbolic link parent",
			headers: []*tar.Header{
				{Name: "usr", Linkname: "..", Typeflag: tar.TypeSymlink},
				{Name: "usr/escape", Mode: 0644, Size: 1, Typeflag: tar.TypeReg},
			},
			content: [][]byte{nil, []byte("x")},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(t.TempDir(), "runtime.tar.gz")
			writeRuntimeArchive(t, archive, test.headers, test.content)
			if err := installRuntimeArchive(root, archive); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

func TestInstallRuntimePackagesRejectsInstallerMismatch(t *testing.T) {
	command := &SpawnCmd{}
	if err := command.installRuntimePackages([]string{"one", "two"}, []string{"tar"}, nil); err == nil {
		t.Fatal("runtime package and installer count mismatch was accepted")
	}
	if err := command.installRuntimePackages([]string{"one"}, []string{"unknown"}, nil); err == nil {
		t.Fatal("unknown runtime installer was accepted")
	}
	if err := command.installRuntimePackages([]string{"one"}, []string{"file"}, []string{"/opt/one", "/opt/two"}); err == nil {
		t.Fatal("runtime package and destination count mismatch was accepted")
	}
}

func TestBuildLayerAppliesSeccompBeforeRunningAnInstaller(t *testing.T) {
	original := applyBuildLayerSeccomp
	t.Cleanup(func() { applyBuildLayerSeccomp = original })
	want := errors.New("seccomp refused")
	called := false
	applyBuildLayerSeccomp = func() error {
		called = true
		return want
	}

	err := (&SpawnCmd{}).installRuntimePackagesInSandbox([]string{"package.deb"}, []string{"dpkg"}, nil)
	if !called || !errors.Is(err, want) {
		t.Fatalf("build layer seccomp: called=%v, err=%v", called, err)
	}
}

func TestApplicationCommandsAlwaysUseANestedUserNamespace(t *testing.T) {
	for _, allowRoot := range []bool{false, true} {
		command := (&SpawnCmd{AllowRoot: allowRoot}).applicationCommand([]string{"launch", "--", "/bin/true"}, []string{"LANG=C"})
		if command.SysProcAttr.Cloneflags&syscall.CLONE_NEWUSER == 0 {
			t.Fatalf("allow root %t: application shares the container init user namespace", allowRoot)
		}
		if len(command.SysProcAttr.UidMappings) == 0 || len(command.SysProcAttr.GidMappings) == 0 {
			t.Fatalf("allow root %t: incomplete identity mapping: %+v", allowRoot, command.SysProcAttr)
		}
	}
}

func TestNestedSandboxPermissionDoesNotGrantMountCapabilities(t *testing.T) {
	command := (&SpawnCmd{UserNamespaces: true}).applicationCommand([]string{"launch", "--", "/bin/true"}, []string{"LANG=C"})
	if command.SysProcAttr.Cloneflags&syscall.CLONE_NEWNS != 0 {
		t.Fatal("nested sandbox permission gave the application a mount namespace")
	}
	if len(command.SysProcAttr.AmbientCaps) != 0 {
		t.Fatalf("nested sandbox permission granted mount capabilities: %v", command.SysProcAttr.AmbientCaps)
	}
}

func TestApplicationCommandsUseTheMountedRuntimeExecutable(t *testing.T) {
	command := (&SpawnCmd{}).applicationCommand([]string{"launch", "--", "/bin/true"}, []string{"LANG=C"})
	if command.Path != cpakInContainerPath {
		t.Fatalf("application runtime: got %q, want %q", command.Path, cpakInContainerPath)
	}
}

func TestRootApplicationCommandsCanUseSystemIdentities(t *testing.T) {
	command := (&SpawnCmd{AllowRoot: true, MapSystemIDs: true}).applicationCommand([]string{"launch", "--", "/bin/true"}, []string{"LANG=C"})
	want := []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: 0, Size: 1},
		{ContainerID: 1, HostID: 1, Size: (1 << 16) - 1},
	}
	if !reflect.DeepEqual(command.SysProcAttr.UidMappings, want) {
		t.Fatalf("root application UID map: %v", command.SysProcAttr.UidMappings)
	}
	if !reflect.DeepEqual(command.SysProcAttr.GidMappings, want) {
		t.Fatalf("root application GID map: %v", command.SysProcAttr.GidMappings)
	}
	if !command.SysProcAttr.GidMappingsEnableSetgroups {
		t.Fatal("root application cannot select a mapped supplementary group")
	}
}

func TestRootApplicationCommandsUseTheAvailableIdentityByDefault(t *testing.T) {
	command := (&SpawnCmd{AllowRoot: true}).applicationCommand([]string{"launch", "--", "/bin/true"}, []string{"LANG=C"})
	want := []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: 1}}
	if !reflect.DeepEqual(command.SysProcAttr.UidMappings, want) {
		t.Fatalf("root application UID map: %v", command.SysProcAttr.UidMappings)
	}
	if !reflect.DeepEqual(command.SysProcAttr.GidMappings, want) {
		t.Fatalf("root application GID map: %v", command.SysProcAttr.GidMappings)
	}
	if command.SysProcAttr.GidMappingsEnableSetgroups {
		t.Fatal("root application enabled setgroups without subordinate IDs")
	}
}

func TestRootApplicationIdentityMap(t *testing.T) {
	if os.Getenv("CPAK_TEST_SYSTEM_ID_MAP") != "1" {
		t.Skip("requires a parent namespace with system IDs mapped")
	}
	mappings := []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: 0, Size: 1},
		{ContainerID: 1, HostID: 1, Size: (1 << 16) - 1},
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRootApplicationIdentityMapHelper$")
	command.Env = append(os.Environ(), "CPAK_TEST_SYSTEM_ID_MAP_HELPER=1")
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER,
		UidMappings:                mappings,
		GidMappings:                mappings,
		GidMappingsEnableSetgroups: true,
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("system identity namespace: %v: %s", err, output)
	}
}

func TestRootApplicationIdentityMapHelper(t *testing.T) {
	if os.Getenv("CPAK_TEST_SYSTEM_ID_MAP_HELPER") != "1" {
		return
	}
	if err := syscall.Setgroups([]int{65534}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setegid(65534); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Seteuid(42); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationFilesRejectAnInvalidNameserverBeforeMounting(t *testing.T) {
	_, _, err := (&SpawnCmd{}).injectConfigurationFiles(t.TempDir(), false, "not-an-address", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid nameserver") {
		t.Fatalf("got %v, want an invalid nameserver error", err)
	}
}

func TestConfigurationFilesLeaveExplicitHostEtcGrantUntouched(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.Mkdir(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/static/resolv.conf", filepath.Join(etc, "resolv.conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(etc, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(etc, 0o755) })

	permissions := []types.FilesystemPermission{{Path: "/etc", Access: "read-write"}}
	if _, _, err := (&SpawnCmd{}).injectConfigurationFiles(root, false, "", permissions); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(etc, "resolv.conf"))
	if err != nil || target != "/etc/static/resolv.conf" {
		t.Fatalf("resolver link changed: target=%q err=%v", target, err)
	}
}

func TestFilesystemPermissionCoversPath(t *testing.T) {
	tests := []struct {
		name       string
		permission string
		path       string
		want       bool
	}{
		{name: "exact", permission: "/etc/resolv.conf", path: "/etc/resolv.conf", want: true},
		{name: "parent", permission: "/etc", path: "/etc/resolv.conf", want: true},
		{name: "prefix only", permission: "/etc2", path: "/etc/resolv.conf", want: false},
		{name: "unrelated", permission: "/var", path: "/etc/resolv.conf", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			permissions := []types.FilesystemPermission{{Path: test.permission, Access: "read-only"}}
			if got := filesystemPermissionCoversPath(permissions, test.path); got != test.want {
				t.Fatalf("filesystemPermissionCoversPath(%q, %q) = %t, want %t", test.permission, test.path, got, test.want)
			}
		})
	}
}

func TestInstallRuntimeFile(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "demo.jar")
	if err := os.WriteFile(artifact, []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installRuntimeFile(root, artifact, "/opt/demo/demo.jar"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "opt", "demo", "demo.jar"))
	if err != nil || string(content) != "payload" {
		t.Fatalf("installed file = %q, err = %v", content, err)
	}
	if err := installRuntimeFile(root, artifact, "/usr/bin/demo"); err == nil {
		t.Fatal("destination outside /opt was accepted")
	}
	if err := os.Symlink("demo.jar", filepath.Join(root, "opt", "demo", "link.jar")); err != nil {
		t.Fatal(err)
	}
	if err := installRuntimeFile(root, artifact, "/opt/demo/link.jar"); err == nil {
		t.Fatal("symbolic link destination was accepted")
	}
}

func writeRuntimeArchive(t *testing.T, destination string, headers []*tar.Header, content [][]byte) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for i, header := range headers {
		if err = archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(content[i]) > 0 {
			if _, err = archive.Write(content[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err = compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeFilesystemPermissionsRejectsDuplicates(t *testing.T) {
	permission := types.FilesystemPermission{Path: "/home/user", Access: "read-write"}
	encoded, err := types.EncodeFilesystemPermission(permission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeFilesystemPermissions([]string{encoded, encoded}); err == nil {
		t.Fatal("accepted duplicate filesystem permission")
	}
}

func TestDecodeFilesystemPermissionsKeepsPortableScope(t *testing.T) {
	encoded, err := types.EncodeFilesystemPermission(types.FilesystemPermission{Path: "host", Access: "read-only"})
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := decodeFilesystemPermissions([]string{encoded})
	if err != nil {
		t.Fatal(err)
	}
	if len(permissions) != 1 || permissions[0] != (types.FilesystemPermission{Path: "host", Access: "read-only"}) {
		t.Fatalf("got %v", permissions)
	}
}

func TestResolveOverrideMountSourceUsesHostRootFallback(t *testing.T) {
	hostRoot := t.TempDir()
	target := filepath.Join(t.TempDir(), "run/dbus/system_bus_socket")
	source := filepath.Join(hostRoot, strings.TrimPrefix(target, "/"))
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("socket"), 0600); err != nil {
		t.Fatal(err)
	}
	got, found, err := resolveOverrideMountSource(target, hostRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != source {
		t.Fatalf("override source: got %q, found %t, want %q", got, found, source)
	}
}

func TestX11SocketPathsKeepsDisplaySockets(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"X0", "X12", "wayland-0"} {
		listener, err := net.Listen("unix", filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
	}
	if err := os.WriteFile(filepath.Join(directory, "X1"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	sockets, err := x11SocketPaths(directory)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(directory, "X0"), filepath.Join(directory, "X12")}
	if !reflect.DeepEqual(sockets, want) {
		t.Fatalf("X11 sockets: got %v, want %v", sockets, want)
	}
}

func TestPathWithinBoundsAGrant(t *testing.T) {
	for _, test := range []struct {
		parent    string
		candidate string
		want      bool
	}{
		{"/home/user", "/home/user/.config/cpak", true},
		{"/home/user", "/home/user", true},
		{"/home/user/", "/home/user/.local/bin/cpak", true},
		{"/home/user", "/home/username/.config/cpak", false},
		{"/home/user/.local/share/bottles", "/home/user/.config/cpak", false},
	} {
		if got := pathWithin(test.parent, test.candidate); got != test.want {
			t.Fatalf("pathWithin(%q, %q) = %v, want %v", test.parent, test.candidate, got, test.want)
		}
	}
}

// TestCpakStateMasksFollowTheGrantIntoTheContainer covers the case the mask was
// blind to. The host scope binds the whole host at /run/host, so the store of
// every other application is a subdirectory away from a grant that names none
// of it, and comparing the grant against where the state lives on the host
// answered the wrong question and matched nothing.
func TestCpakStateMasksFollowTheGrantIntoTheContainer(t *testing.T) {
	home := t.TempDir()
	// The tree is where this installation put it, which is what
	// CPAK_INSTALLATION_PATH and the per-path variables decide.
	store := filepath.Join(t.TempDir(), "store")
	directories := types.CpakStateDirectories(home, store)
	binary := filepath.Join(home, ".local/bin/cpak")
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, nil, 0755); err != nil {
		t.Fatal(err)
	}

	got := []string{}
	for _, masked := range cpakStateMasks(directories, home, "/", "/run/host") {
		got = append(got, masked.container)
	}
	want := []string{
		filepath.Join("/run/host", store),
		filepath.Join("/run/host", home, ".config/cpak"),
		filepath.Join("/run/host", home, ".local/share/applications"),
		filepath.Join("/run/host", binary),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("host scope masks: got %v, want %v", got, want)
	}

	got = got[:0]
	for _, masked := range cpakStateMasks(directories, home, home, home) {
		got = append(got, masked.container)
	}
	want = []string{
		filepath.Join(home, ".config/cpak"),
		filepath.Join(home, ".local/share/applications"),
		binary,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("home grant masks: got %v, want %v", got, want)
	}

	if masks := cpakStateMasks(directories, home, "/etc", "/etc"); len(masks) != 0 {
		t.Fatalf("a grant that holds no cpak state was masked: %v", masks)
	}
}

// TestAGrantOnCpakStateIsLeftOutOfTheContainer is how a launch answers a grant
// that is refused at install time but was installed before the refusal existed.
// It is left unmounted, with a line saying so: the application starts and
// reaches nothing it should not have been given.
func TestAGrantOnCpakStateIsLeftOutOfTheContainer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(filepath.Join(store, "containers"), 0755); err != nil {
		t.Fatal(err)
	}
	c := &SpawnCmd{MaskState: types.CpakStateDirectories(home, store)}
	rootFs := t.TempDir()

	grant, mounted, err := c.mountFilesystemPermission(rootFs, types.FilesystemPermission{
		Path:   filepath.Join(store, "containers"),
		Access: "read-write",
	})
	if err != nil {
		t.Fatalf("a stale grant stopped the container from starting: %v", err)
	}
	if mounted || grant.Path != "" {
		t.Fatalf("a grant on cpak's own state was mounted: %+v", grant)
	}
	if _, err := os.Stat(filepath.Join(rootFs, store)); !os.IsNotExist(err) {
		t.Fatalf("the grant left a target behind in the rootfs: %v", err)
	}
}

func TestAMissingHostGrantDoesNotStopTheContainer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &SpawnCmd{}
	grant, mounted, err := c.mountFilesystemPermission(t.TempDir(), types.FilesystemPermission{
		Path:   "home/.local/share/example",
		Access: "read-write",
	})
	if err != nil {
		t.Fatalf("a missing optional host path stopped the container: %v", err)
	}
	if mounted || grant.Path != "" {
		t.Fatalf("a missing host path was mounted: %+v", grant)
	}
}

// TestAStaleCpakStateGrantStillDecodesAtLaunch walks the launch path itself.
// Every grant an application was installed with is decoded and validated again
// here, at every launch, so a refusal placed in the shared validator would not
// narrow an application installed before the rule existed: it would stop it
// from starting, and nothing short of uninstalling would clear it.
func TestAStaleCpakStateGrantStillDecodesAtLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	encoded := []string{}
	for _, permission := range []types.FilesystemPermission{
		{Path: "home/.local/share/cpak/store", Access: "read-write"},
		{Path: filepath.Join(home, ".local/share/applications"), Access: "read-write"},
	} {
		value, err := types.EncodeFilesystemPermission(permission)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, value)
	}
	permissions, err := decodeFilesystemPermissions(encoded)
	if err != nil {
		t.Fatalf("an installed application stopped launching: %v", err)
	}
	if len(permissions) != 2 {
		t.Fatalf("decoded grants: %v", permissions)
	}
}

func TestOpenRuntimeSecretFileRejectsLinksAndPublicFiles(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openRuntimeSecretFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if file, err = openRuntimeSecretFile(link); err == nil {
		file.Close()
		t.Fatal("a symbolic link was accepted as a runtime secret")
	}
	if err = os.Chmod(secret, 0644); err != nil {
		t.Fatal(err)
	}
	if file, err = openRuntimeSecretFile(secret); err == nil {
		file.Close()
		t.Fatal("a public file was accepted as a runtime secret")
	}
}
