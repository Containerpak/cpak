/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/core"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestLearnCoreManifestValidationMatchesRuntime(t *testing.T) {
	cases := map[string]func(*types.CpakManifest){
		"valid": func(*types.CpakManifest) {},
		"service": func(manifest *types.CpakManifest) {
			manifest.Services = map[string]types.ApplicationService{
				"server": {Binary: manifest.Binaries[0], Arguments: []string{"serve", "--port", "3000"}},
			}
		},
		"service binary": func(manifest *types.CpakManifest) {
			manifest.Services = map[string]types.ApplicationService{"server": {Binary: "/usr/bin/missing"}}
		},
		"service name": func(manifest *types.CpakManifest) {
			manifest.Services = map[string]types.ApplicationService{"Bad Name": {Binary: manifest.Binaries[0]}}
		},
		"service argument": func(manifest *types.CpakManifest) {
			manifest.Services = map[string]types.ApplicationService{
				"server": {Binary: manifest.Binaries[0], Arguments: []string{"serve\x1b[2J"}},
			}
		},
		"host network": func(manifest *types.CpakManifest) {
			manifest.Override.HostNetwork = true
			manifest.Override.Network = false
		},
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			runtimeManifest := validManifestForTest()
			change(runtimeManifest)
			learnManifest := cloneManifestForParity(t, runtimeManifest)

			runtimeErr := (&Cpak{}).ValidateManifest(runtimeManifest)
			learnErr := core.ValidateManifest(learnManifest)
			if errorText(runtimeErr) != errorText(learnErr) {
				t.Fatalf("validation differs: runtime %q, Learn core %q", errorText(runtimeErr), errorText(learnErr))
			}
		})
	}
}

func TestLearnCoreManifestDecodingAndMigrationMatchRuntime(t *testing.T) {
	contents := []string{
		`{"manifest_version":"2.0","name":"Example","description":"Example","image":"ghcr.io/example/app:latest","binaries":["/usr/bin/example"]}`,
		`{"manifest_version":"2.0","name":"Example","description":"Example","image":"ghcr.io/example/app:latest","binaries":["/usr/bin/example"],"unknown":true}`,
		`{"manifest_version":"2.0","name":"Example","description":"Example","image":"ghcr.io/example/app:latest","binaries":["/usr/bin/example"]} {}`,
		`{"name":"Example","description":"Example","image":"ghcr.io/example/app:latest","binaries":["/usr/bin/example"],"override":{"fsHostHome":false}}`,
	}
	for _, content := range contents {
		runtimeManifest, runtimeErr := DecodeManifest([]byte(content))
		learnManifest, learnErr := core.DecodeManifest([]byte(content))
		if errorText(runtimeErr) != errorText(learnErr) {
			t.Fatalf("manifest decode differs: runtime %q, Learn core %q", errorText(runtimeErr), errorText(learnErr))
		}
		if runtimeErr == nil && manifestSnapshot(t, runtimeManifest) != manifestSnapshot(t, learnManifest) {
			t.Fatalf("decoded manifests differ:\n runtime: %s\nLearn core: %s", manifestSnapshot(t, runtimeManifest), manifestSnapshot(t, learnManifest))
		}
	}

	legacy := []byte(`{"name":"Example","description":"Example","image":"ghcr.io/example/app:latest","binaries":["/usr/bin/example"],"override":{"fsHostHome":true,"allowedHostCommands":["xdg-open"]}}`)
	runtimeManifest, err := DecodeManifest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	learnManifest, err := core.DecodeManifest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	runtimeErr := MigrateManifest(runtimeManifest)
	learnErr := core.MigrateManifest(learnManifest)
	if errorText(runtimeErr) != errorText(learnErr) || manifestSnapshot(t, runtimeManifest) != manifestSnapshot(t, learnManifest) {
		t.Fatalf("manifest migration differs:\n runtime: %s (%v)\nLearn core: %s (%v)", manifestSnapshot(t, runtimeManifest), runtimeErr, manifestSnapshot(t, learnManifest), learnErr)
	}
}

func TestLearnCoreRuntimeSourceValidationMatchesRuntime(t *testing.T) {
	valid := types.RuntimeSource{
		URL:       "https://example.com/runtime.tar",
		SHA256:    strings.Repeat("a", 64),
		Size:      1024,
		Installer: "tar",
	}
	cases := []types.RuntimeSource{
		valid,
		{URL: "http://example.com/runtime.tar", SHA256: valid.SHA256, Size: valid.Size, Installer: valid.Installer},
		{URL: valid.URL, SHA256: "invalid", Size: valid.Size, Installer: valid.Installer},
		{URL: valid.URL, SHA256: valid.SHA256, Size: valid.Size, Installer: "invalid"},
		{URL: valid.URL, SHA256: valid.SHA256, Size: valid.Size, Installer: "file", Destination: "/tmp/runtime"},
	}

	for _, source := range cases {
		if got, want := core.RuntimeSourceFileName(source), RuntimeSourceFileName(source); got != want {
			t.Fatalf("runtime source file name differs: runtime %q, Learn core %q", want, got)
		}
		runtimeErr := ValidateRuntimeSource(source)
		learnErr := core.ValidateRuntimeSource(source)
		if errorText(runtimeErr) != errorText(learnErr) {
			t.Fatalf("runtime source validation differs: runtime %q, Learn core %q", errorText(runtimeErr), errorText(learnErr))
		}
	}
}

func TestLearnCoreDesktopRewritesMatchRuntime(t *testing.T) {
	launcher := "/home/user/.local/bin/cpak"
	origin := "github.com/containerpak/example"
	commands := []string{
		`"/opt/Example App/example" --new-window %U`,
		"/usr/bin/example",
		"/usr/bin/example %f --and %U",
	}
	for _, command := range commands {
		if got, want := core.RewriteDesktopExec(launcher, origin, command), rewriteDesktopExec(launcher, origin, command); got != want {
			t.Fatalf("desktop rewrite differs:\n runtime: %s\nLearn core: %s", want, got)
		}
	}

	legacy := "Exec=cpak run " + origin + " @/usr/bin/example -- --open @@cpak:file-grant:start@@ %U @@cpak:file-grant:end@@\nTryExec=cpak\n"
	if got, want := core.RepairDesktopLauncher(legacy, launcher), repairDesktopLauncher(legacy, launcher); got != want {
		t.Fatalf("desktop repair differs:\n runtime: %s\nLearn core: %s", want, got)
	}

	entries := []string{
		"[Desktop Entry]\nName=Example\n",
		"\ufeff[Desktop Entry]\r\nName=Example\r\n",
		"[Desktop Entry]\nName=Example\n[Broken\nX-Test=value\n",
		"Name=Example\n",
	}
	for _, entry := range entries {
		if got, want := core.SetDesktopEntryValue(entry, "X-Test", "value"), setDesktopEntryValue(entry, "X-Test", "value"); got != want {
			t.Fatalf("desktop value update differs:\n runtime: %q\nLearn core: %q", want, got)
		}
		if got, want := core.DesktopEntryValue([]byte(entry), "Name"), desktopEntryValue([]byte(entry), "Name"); got != want {
			t.Fatalf("desktop value read differs: runtime %q, Learn core %q", want, got)
		}
	}
}

func TestLearnCorePolicyIntersectionMatchesRuntime(t *testing.T) {
	t.Setenv("HOME", "/home/parity")
	parent := types.Override{}
	child := types.Override{}
	parentValue := reflect.ValueOf(&parent).Elem()
	childValue := reflect.ValueOf(&child).Elem()
	for index := 0; index < parentValue.NumField(); index++ {
		if parentValue.Field(index).Kind() == reflect.Bool {
			parentValue.Field(index).SetBool(true)
			childValue.Field(index).SetBool(true)
		}
	}
	parent.MemoryMaxMB, child.MemoryMaxMB = 1024, 512
	parent.CPUQuota, child.CPUQuota = 80, 40
	parent.PidsMax, child.PidsMax = 256, 128
	parent.Env = []string{"MODE=production", "PORT=3000"}
	child.Env = []string{"PORT=3000"}
	parent.FsExtra = []string{"/opt/data"}
	child.FsExtra = []string{"/opt/data"}
	parent.Filesystem = []types.FilesystemPermission{{Path: "home", Access: "read-write"}}
	child.Filesystem = []types.FilesystemPermission{{Path: "/home/parity/data", Access: "read-write"}}
	parent.HostActions = []types.HostActionGrant{{Provider: types.HostActionProviderCpak, Capabilities: []string{types.HostActionCpakRead}}}
	child.HostActions = parent.HostActions

	runtime := intersectOverrides(parent, child)
	learn := core.Intersect(parent, child, core.Host{Home: "/home/parity"})
	if !reflect.DeepEqual(runtime, learn) {
		t.Fatalf("policy intersection differs:\n runtime: %#v\nLearn core: %#v", runtime, learn)
	}
}

func TestLearnCoreBrokerShimsMatchRuntime(t *testing.T) {
	override := types.Override{
		Notification: true,
		OpenURI:      true,
		HostActions: []types.HostActionGrant{
			{Provider: types.HostActionProviderContainers, Capabilities: []string{types.HostActionContainersExecOwned}},
			{Provider: types.HostActionProviderCpak, Capabilities: []string{types.HostActionCpakRead}},
		},
	}
	if got, want := core.SystemBrokerShims(override), systemBrokerShims(override); !reflect.DeepEqual(got, want) {
		t.Fatalf("broker shims differ: runtime %v, Learn core %v", want, got)
	}
}

func cloneManifestForParity(t *testing.T, manifest *types.CpakManifest) *types.CpakManifest {
	t.Helper()
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	clone := &types.CpakManifest{}
	if err = json.Unmarshal(content, clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func manifestSnapshot(t *testing.T, manifest *types.CpakManifest) string {
	t.Helper()
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return string(content) + "|legacy=" + strings.Join(manifest.LegacyFilesystemFields(), ",") + "|filesystem=" + strconv.FormatBool(manifest.FilesystemDeclared()) + "|removed=" + strings.Join(manifest.ManifestV3RemovedFields(), ",")
}
