/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package flatpak

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestKeyFileReadsListsAndEscapes(t *testing.T) {
	file, err := ParseKeyFile([]byte("# comment\n[Context]\nsockets=x11;wayland;\nfilesystems=~/a\\;b;xdg-download:ro\n[Environment]\nVALUE = with\\sspace\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := file.List("Context", "sockets"); !reflect.DeepEqual(got, []string{"x11", "wayland"}) {
		t.Fatalf("sockets read as %q", got)
	}
	if got := file.List("Context", "filesystems"); !reflect.DeepEqual(got, []string{"~/a;b", "xdg-download:ro"}) {
		t.Fatalf("an escaped separator split a value: %q", got)
	}
	if got := file.String("Environment", "VALUE"); got != "with space" {
		t.Fatalf("an escaped space read as %q", got)
	}
}

func TestKeyFileMergesARepeatedGroup(t *testing.T) {
	file, err := ParseKeyFile([]byte("[Runtime]\nname=org.kde.Platform\n[Build]\nbuilt-extensions=a;\n[Extension org.kde.Platform.GL]\ndirectory=lib/GL\n[Build]\nbuilt-extensions=b;\nsdk=org.kde.Sdk\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Groups(); !reflect.DeepEqual(got, []string{"Runtime", "Build", "Extension org.kde.Platform.GL"}) {
		t.Fatalf("groups read as %q", got)
	}
	if got := file.List("Build", "built-extensions"); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("a repeated key kept %q instead of its last value", got)
	}
	if got := file.String("Build", "sdk"); got != "org.kde.Sdk" {
		t.Fatalf("a key of the repeated group read as %q", got)
	}
}

func TestKeyFileRefusesMalformedInput(t *testing.T) {
	for _, input := range []string{"key=value\n", "[Group\n", "[A]\nnovalue\n", "[A]\n=value\n"} {
		if _, err := ParseKeyFile([]byte(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestRefsAreValidated(t *testing.T) {
	ref, err := ParseRef("app/org.gimp.GIMP/x86_64/stable")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != KindApp || ref.ID != "org.gimp.GIMP" || ref.Arch != "x86_64" || ref.Branch != "stable" {
		t.Fatalf("parsed %+v", ref)
	}
	for _, value := range []string{
		"app/org.gimp/x86_64/stable",
		"app/org.gimp.GIMP/x86_64",
		"app/org.gimp.GIMP/../stable",
		"repo/org.gimp.GIMP/x86_64/stable",
		"app/org.1gimp.GIMP/x86_64/stable",
		"app/org.gimp.GIMP/x86 64/stable",
		"app/org.gimp..GIMP/x86_64/stable",
	} {
		if _, err := ParseRef(value); err == nil {
			t.Fatalf("accepted the ref %q", value)
		}
	}
}

func readMetadata(t *testing.T, name string) Metadata {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "metadata", name))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseMetadata(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return meta
}

func TestFlathubMetadataParses(t *testing.T) {
	gimp := readMetadata(t, "org.gimp.GIMP")
	if gimp.Name != "org.gimp.GIMP" || gimp.Command != "gimp" || gimp.RuntimeRef.String() != "runtime/org.gnome.Platform/x86_64/51" {
		t.Fatalf("GIMP metadata read as %+v", gimp)
	}
	var plugin ExtensionPoint
	for _, point := range gimp.Extensions {
		if point.Name == "org.gimp.GIMP.Plugin" {
			plugin = point
		}
	}
	if plugin.Directory != "extensions" || !plugin.Subdirectories || plugin.AddLdPath != "lib" {
		t.Fatalf("the plug-in extension point read as %+v", plugin)
	}
	if !plugin.Matches("org.gimp.GIMP.Plugin.GMic") || plugin.Matches("org.gimp.GIMP.Plugin.GMic.Extra") {
		t.Fatal("a plug-in extension matched one level too deep or not at all")
	}
	if plugin.MountPoint("org.gimp.GIMP.Plugin.GMic") != "extensions/GMic" {
		t.Fatalf("a plug-in mounts at %s", plugin.MountPoint("org.gimp.GIMP.Plugin.GMic"))
	}

	platform := readMetadata(t, "org.gnome.Platform")
	if !platform.Runtime || platform.Name != "org.gnome.Platform" {
		t.Fatalf("the runtime metadata read as %+v", platform)
	}
	var gl ExtensionPoint
	for _, point := range platform.Extensions {
		if point.Name == "org.freedesktop.Platform.GL" {
			gl = point
		}
	}
	if gl.Directory != "lib/x86_64-linux-gnu/GL" || !reflect.DeepEqual(gl.Versions, []string{"25.08", "25.08-extra", "1.4"}) {
		t.Fatalf("the GL extension point read as %+v", gl)
	}
}

func TestMetadataRefusesPathsOutOfItsTree(t *testing.T) {
	for _, input := range []string{
		"[Application]\nname=org.example.App\nruntime=org.example.Platform/x86_64/1\ncommand=../../bin/sh\n",
		"[Application]\nname=org.example.App\nruntime=org.example.Platform/x86_64/1\n[Extension org.example.App.Plugin]\ndirectory=../../etc\n",
		"[Application]\nname=org.example.App\nruntime=org.example.Platform/x86_64/1\n[Extension org.example.App.Plugin]\ndirectory=/etc\n",
		"[Application]\nname=org.example.App\nruntime=../../x86_64/1\n",
	} {
		if _, err := ParseMetadata([]byte(input)); err == nil {
			t.Fatalf("accepted:\n%s", input)
		}
	}
}

func decision(translation Translation, request string) (Decision, bool) {
	for _, finding := range translation.Findings {
		if finding.Request == request {
			return finding.Decision, true
		}
	}
	return "", false
}

func TestTranslationOfCalculator(t *testing.T) {
	translation := Translate(readMetadata(t, "org.gnome.Calculator"))
	override := translation.Override
	if !override.SocketWayland || !override.Network || !override.DeviceDri {
		t.Fatalf("Calculator lost what it asked for: %+v", override)
	}
	if override.DisplayX11 {
		t.Fatal("fallback-x11 started an X server for an application that runs on Wayland")
	}
	if !override.FilePicker.OpenFile || !override.OpenURI {
		t.Fatal("the cpak file chooser and link opening were not provided")
	}
	if got, _ := decision(translation, "shared=ipc"); got != Narrowed {
		t.Fatalf("shared=ipc was %s", got)
	}
}

// GIMP asks for the host root, shared memory through /tmp, two other
// services' runtime sockets and whole session bus services. None of it may
// survive the translation.
func TestTranslationOfGIMPRefusesTheHoles(t *testing.T) {
	translation := Translate(readMetadata(t, "org.gimp.GIMP"))
	override := translation.Override
	for _, request := range []string{
		"filesystems=host",
		"filesystems=/tmp",
		"filesystems=xdg-run/gvfsd",
		"filesystems=xdg-run/gvfs",
		"session-bus talk org.gtk.vfs.*",
		"session-bus talk org.gnome.Shell.Screenshot",
	} {
		if got, ok := decision(translation, request); !ok || got != Refused {
			t.Fatalf("%s was %q", request, got)
		}
	}
	if override.DeviceAll {
		t.Fatal("devices=all became the whole of /dev")
	}
	if len(override.SessionBus.Talk) != 0 {
		t.Fatalf("a whole session bus service was opened: %+v", override.SessionBus)
	}
	want := []types.FilesystemPermission{
		{Path: "home/.config/GIMP", Access: "read-write"},
		{Path: "home/.config/gtk-3.0", Access: "read-only"},
	}
	if !reflect.DeepEqual(override.Filesystem, want) {
		t.Fatalf("GIMP was given %+v, want %+v", override.Filesystem, want)
	}
	for _, entry := range override.Env {
		if strings.HasPrefix(entry, "GTK_MODULES=") {
			t.Fatal("a loader variable came through from the package")
		}
	}
}

func TestTranslationOfFirefox(t *testing.T) {
	translation := Translate(readMetadata(t, "org.mozilla.firefox"))
	override := translation.Override
	if override.DisplayX11 {
		t.Fatal("Firefox got an X server although it runs on Wayland")
	}
	if !override.SocketPulseAudio || !override.SocketCups {
		t.Fatalf("Firefox lost audio or printing: %+v", override)
	}
	if got, _ := decision(translation, "features=devel"); got != Refused {
		t.Fatalf("features=devel was %s", got)
	}
	if got, _ := decision(translation, "filesystems=/run/udev:ro"); got != Refused {
		t.Fatalf("/run/udev was %s", got)
	}
	if !slices.Contains(override.SessionBus.Own, "org.mpris.MediaPlayer2.firefox.*") {
		t.Fatalf("Firefox cannot own its media player name: %+v", override.SessionBus.Own)
	}
	if !slices.Contains(override.Filesystem, types.FilesystemPermission{Path: "xdg-download", Access: "read-write"}) {
		t.Fatalf("Firefox lost its download folder: %+v", override.Filesystem)
	}
}

func TestTranslationOfVLCUsesAnIsolatedX11(t *testing.T) {
	translation := Translate(readMetadata(t, "org.videolan.VLC"))
	override := translation.Override
	if !override.DisplayX11 || override.SocketX11 {
		t.Fatalf("VLC was not given an isolated X server: %+v", override)
	}
	if !override.Clipboard.HostToApp || !override.Clipboard.AppToHost {
		t.Fatal("the isolated X server has no clipboard")
	}
	if got, _ := decision(translation, "session-bus talk org.freedesktop.secrets"); got != Refused {
		t.Fatalf("the secret service was %s", got)
	}
}

func TestTranslationOfCodeAndOBS(t *testing.T) {
	code := Translate(readMetadata(t, "com.visualstudio.code"))
	if got, _ := decision(code, "session-bus talk org.freedesktop.Flatpak"); got != Provided {
		t.Fatalf("the Flatpak service was %s", got)
	}
	if !code.Override.Notification || !code.Override.SocketSshAgent {
		t.Fatalf("Code lost notifications or the SSH agent: %+v", code.Override)
	}
	obs := Translate(readMetadata(t, "com.obsproject.Studio"))
	if got, _ := decision(obs, "filesystems=xdg-run/pipewire-0"); got != Refused {
		t.Fatalf("the PipeWire socket was %s", got)
	}
}

// Every translated policy must be one the cpak validator accepts, or a
// Flatpak install would fail on cpak's own rules instead of being narrowed by
// them.
func TestTranslationsPassTheCpakValidators(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		meta := readMetadata(t, entry.Name())
		if meta.Runtime {
			continue
		}
		override := Translate(meta).Override
		if err := types.ValidateDBusPolicy(override.SessionBus); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if err := types.ValidateFilePickerGrant(override.FilePicker); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if err := types.ValidateClipboardGrant(override.Clipboard, override.DisplayX11); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if err := types.ValidateNetworkPermissions(override); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
	}
}

func TestRepoAndRefFilesNeedAKey(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "flathub.flatpakrepo"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := ParseRepoFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if repo.URL != "https://dl.flathub.org/repo/" || len(repo.Key) == 0 {
		t.Fatalf("the Flathub description read as %+v", repo)
	}
	if string(repo.Key) != string(flathubKey) {
		t.Fatal("the key Flathub publishes is not the key cpak pins")
	}

	data, err = os.ReadFile(filepath.Join("testdata", "org.gimp.GIMP.flatpakref"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := ParseRefFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "org.gimp.GIMP" || ref.Branch != "stable" || ref.SuggestRemoteName != "flathub" {
		t.Fatalf("the GIMP ref read as %+v", ref)
	}

	if _, err := ParseRepoFile([]byte("[Flatpak Repo]\nUrl=https://example.org/repo/\n")); err == nil {
		t.Fatal("a repository description without a key was accepted")
	}
	if _, err := ParseRepoFile([]byte("[Flatpak Repo]\nUrl=http://example.org/repo/\nGPGKey=" + "AAAA" + "\n")); err == nil {
		t.Fatal("a repository over plain HTTP was accepted")
	}
}

func TestFlathubCannotBeRedefined(t *testing.T) {
	file := filepath.Join(t.TempDir(), "flatpak-remotes.json")
	if err := os.WriteFile(file, []byte(`[{"name":"flathub","url":"https://evil.example/repo/","key":"AAAA"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRemotes(file); err == nil {
		t.Fatal("a remotes file replaced the built-in Flathub remote")
	}
}

func TestRemotesRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "flatpak-remotes.json")
	remotes, err := LoadRemotes(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remotes.Get(DefaultRemote); err != nil {
		t.Fatal(err)
	}
	extra := Remote{Name: "example", URL: "https://example.org/repo", Key: flathubKey}
	if err := remotes.Add(extra); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRemotes(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Get("example")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.org/repo/" {
		t.Fatalf("the remote URL was stored as %s", got.URL)
	}
	if err := loaded.Add(Remote{Name: "example", URL: "https://other.example/repo/", Key: flathubKey}); err == nil {
		t.Fatal("a second remote replaced an existing one under the same name")
	}
}

func TestParseMetadataReadsExtraData(t *testing.T) {
	meta, err := ParseMetadata([]byte("[Application]\nname=com.example.App\nruntime=org.freedesktop.Platform/x86_64/24.08\ncommand=app\n\n[Extra Data]\nname=app.deb\nchecksum=" + strings.Repeat("a", 64) + "\nsize=42\nuri=https://example.com/app.deb\nname1=more.bin\nchecksum1=" + strings.Repeat("b", 64) + "\nsize1=7\nuri1=https://example.com/more.bin\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.ExtraData) != 2 || meta.ExtraData[0].Name != "app.deb" || meta.ExtraData[0].Size != 42 || meta.ExtraData[1].Name != "more.bin" {
		t.Fatalf("extra data = %+v", meta.ExtraData)
	}
	if _, err := ParseMetadata([]byte("[Application]\nname=com.example.App\nruntime=org.freedesktop.Platform/x86_64/24.08\n\n[Extra Data]\nname=../escape\nchecksum=" + strings.Repeat("a", 64) + "\nsize=1\nuri=https://example.com/x\n")); err == nil {
		t.Fatal("a path in an extra data name was accepted")
	}
}
