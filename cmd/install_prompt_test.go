/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

func TestVerifySignedInstallerMetadataRejectsPermissionChanges(t *testing.T) {
	manifest := &types.CpakManifest{
		ManifestVersion: "2.0",
		Name:            "Demo",
		Description:     "Demo application",
		Image:           "ghcr.io/containerpak/demo@sha256:" + strings.Repeat("a", 64),
		Binaries:        []string{"/usr/bin/demo"},
		Override:        types.Override{Network: true},
	}
	digest, err := cpak.ManifestIdentityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	metadata := bootstrap.Metadata{
		Origin:         "github.com/containerpak/demo",
		RefType:        "commit",
		Ref:            strings.Repeat("b", 40),
		ManifestDigest: digest,
		Permissions:    bootstrap.SummarizePermissions(manifest.Override),
	}
	if err = verifySignedInstallerMetadata(metadata, metadata.Origin, metadata.Ref, manifest); err != nil {
		t.Fatal(err)
	}
	metadata.Permissions = []bootstrap.Permission{{Name: "Network", Detail: "cosmetic only"}}
	if err = verifySignedInstallerMetadata(metadata, metadata.Origin, metadata.Ref, manifest); err == nil {
		t.Fatal("a cosmetic permission list was accepted")
	}
	metadata.Permissions = bootstrap.SummarizePermissions(manifest.Override)
	manifest.Override.Filesystem = []types.FilesystemPermission{{Path: "host", Access: "read-write"}}
	if err = verifySignedInstallerMetadata(metadata, metadata.Origin, metadata.Ref, manifest); err == nil {
		t.Fatal("a broader fetched manifest matched the signed installer")
	}
}

func TestVerifySignedInstallerMetadataRequiresPinnedStandalonePackage(t *testing.T) {
	manifest := &types.CpakManifest{
		ManifestVersion: "2.0",
		Name:            "Demo",
		Description:     "Demo application",
		Image:           "ghcr.io/containerpak/demo:latest",
		Binaries:        []string{"/usr/bin/demo"},
	}
	metadata := bootstrap.Metadata{
		Origin:      "github.com/containerpak/demo",
		RefType:     "commit",
		Ref:         strings.Repeat("b", 40),
		Permissions: bootstrap.SummarizePermissions(manifest.Override),
	}
	digest, err := cpak.ManifestIdentityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	metadata.ManifestDigest = digest
	if err = verifySignedInstallerMetadata(metadata, metadata.Origin, metadata.Ref, manifest); err == nil {
		t.Fatal("a mutable image tag was accepted")
	}

	manifest.Image = "ghcr.io/containerpak/demo@sha256:" + strings.Repeat("a", 64)
	manifest.Dependencies = []types.Dependency{{Origin: "github.com/containerpak/runtime"}}
	digest, err = cpak.ManifestIdentityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	metadata.ManifestDigest = digest
	if err = verifySignedInstallerMetadata(metadata, metadata.Origin, metadata.Ref, manifest); err == nil {
		t.Fatal("an unbound dependency graph was accepted")
	}
}

func TestSignedInstallerConsentRejectsAnOrdinaryParentProcess(t *testing.T) {
	manifest := &types.CpakManifest{
		ManifestVersion: "2.0",
		Name:            "Demo",
		Description:     "Demo application",
		Image:           "ghcr.io/containerpak/demo@sha256:" + strings.Repeat("a", 64),
		Binaries:        []string{"/usr/bin/demo"},
	}
	if err := verifySignedInstaller("github.com/containerpak/demo", strings.Repeat("b", 40), manifest); err == nil {
		t.Fatal("an ordinary parent process supplied signed installer consent")
	}
}

// carriesTerminalControl spells the rule out here rather than asking the code
// under test what a control character is. A test that borrows the definition it
// is checking passes whatever the definition says.
func carriesTerminalControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f || (character >= 0x80 && character <= 0x9f) {
			return true
		}
	}
	return false
}

func TestTheInstallPromptOnlyShowsRuntimeSourcesForThisArchitecture(t *testing.T) {
	other := "amd64"
	if runtime.GOARCH == other {
		other = "arm64"
	}
	var output bytes.Buffer
	command := &InstallCmd{Base: cli.Base{Logger: clilog.NewWriter(&output, &output)}}
	manifest := &types.CpakManifest{
		RuntimeSources: []types.RuntimeSource{
			{URL: "https://example.com/native", Size: 1, Architecture: runtime.GOARCH},
			{URL: "https://example.com/other", Size: 1, Architecture: other},
		},
	}
	command.describeRuntimeSourcesAndPermissions(manifest, manifest.Override, false, "")
	printed := output.String()
	if !strings.Contains(printed, "/native") || strings.Contains(printed, "/other") {
		t.Fatalf("runtime source prompt: %q", printed)
	}
}

// The prompt is the last thing between a publisher and a granted permission,
// and every string on it is theirs. One cursor movement redraws the lines above
// it, so a package can show one set of permissions and be granted another.
func TestTheInstallPromptLetsNoPublisherValueMoveTheCursor(t *testing.T) {
	// A cursor-up and an erase-line, written twice: once as ESC [ and once as
	// the single characters a UTF-8 terminal reads the same way.
	tainted := "\x1b[1A\x1b[2K\u009b1A\u009b2K"

	var output bytes.Buffer
	command := &InstallCmd{Base: cli.Base{Logger: clilog.NewWriter(&output, &output)}}

	manifest := &types.CpakManifest{
		Name:        "demo" + tainted,
		Description: "a demo application" + tainted,
		Binaries:    []string{"/usr/bin/demo" + tainted},
		DesktopEntries: []string{
			"demo.desktop" + tainted,
		},
		Sessions: []types.Session{{
			Kind: "wayland" + tainted,
			Name: "demo session" + tainted,
		}},
		RuntimeSources: []types.RuntimeSource{{
			URL:  "https://example.invalid/blob" + tainted,
			Size: 42,
		}},
	}

	command.describeRootPackage(manifest)
	command.describeDependencies([]cpak.ResolvedDependency{{
		Origin: "github.com/user/library" + tainted,
		Manifest: &types.CpakManifest{
			Description: "a library nobody asked for" + tainted,
		},
	}})
	command.describeRuntimeSourcesAndPermissions(manifest, manifest.Override, false, "")

	printed := output.String()
	// The line breaks are the prompt's own, so the rule is applied to what is
	// written between them.
	for _, line := range strings.Split(printed, "\n") {
		if carriesTerminalControl(line) {
			t.Fatalf("the prompt handed the terminal a control character: %q", line)
		}
	}
	if !strings.Contains(printed, "demo") || !strings.Contains(printed, "github.com/user/library") {
		t.Fatalf("the prompt no longer says what is being installed: %q", printed)
	}
	// Nine tainted values go in: the name, the description, a binary, a desktop
	// entry, a session kind and name, a runtime source URL, and a dependency's
	// origin and description. Every one of them comes out escaped, so nothing
	// was left unescaped by a print site somebody forgot to wrap.
	if got := strings.Count(printed, `\x1b[1A`); got != 9 {
		t.Fatalf("the escape byte was spelled out %d times, want once per printed value: %q", got, printed)
	}
	if got := strings.Count(printed, `\u009b`); got != 18 {
		t.Fatalf("the single character control sequences were spelled out %d times, want twice per printed value: %q", got, printed)
	}
}

func TestTheInstallPromptShowsTheSavedRuntimeOverride(t *testing.T) {
	var output bytes.Buffer
	command := &InstallCmd{Base: cli.Base{Logger: clilog.NewWriter(&output, &output)}}
	manifest := &types.CpakManifest{Override: types.Override{SessionBus: types.DBusPolicy{
		Own: []string{"com.steampowered.PressureVessel.*"},
	}}}
	saved := types.Override{SessionBus: types.DBusPolicy{
		Own: []string{"com.steampowered.PressureVessel.LaunchAlongsideSteam"},
	}}

	command.describeRuntimeSourcesAndPermissions(manifest, saved, true, "github.com/containerpak/steam")
	printed := output.String()
	if !strings.Contains(printed, "saved user override") {
		t.Fatalf("the saved override was hidden: %q", printed)
	}
	if !strings.Contains(printed, "LaunchAlongsideSteam") || strings.Contains(printed, "PressureVessel.*") {
		t.Fatalf("the prompt did not show the effective permissions: %q", printed)
	}
}
