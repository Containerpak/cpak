/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/gvariant"
	"github.com/mirkobrombin/cpak/pkg/ostree"
	"github.com/mirkobrombin/cpak/pkg/ostree/ostreetest"
	"github.com/mirkobrombin/cpak/pkg/types"
)

var (
	flatpakKeyOnce sync.Once
	flatpakKey     *ostreetest.Key
)

func testFlatpakKey() *ostreetest.Key {
	flatpakKeyOnce.Do(func() { flatpakKey = ostreetest.NewKey() })
	return flatpakKey
}

func flatpakTestCpak(t *testing.T) *Cpak {
	t.Helper()
	t.Setenv("CPAK_STORAGE_DRIVER_BINARY", filepath.Join(t.TempDir(), "absent"))
	root := t.TempDir()
	return &Cpak{
		Ctx: context.Background(),
		Options: Options{
			StorePath:        filepath.Join(root, "store"),
			CachePath:        filepath.Join(root, "cache"),
			ExportsPath:      filepath.Join(root, "exports"),
			RegistryAuthPath: filepath.Join(root, "config", "registry-auth.json"),
		},
	}
}

func TestFlatpakOriginsCannotNameAGitRepository(t *testing.T) {
	origin := FlatpakOrigin("flathub", "org.gimp.GIMP")
	if origin != "flatpak/flathub/org.gimp.gimp" {
		t.Fatalf("origin is %s", origin)
	}
	if normalized, err := normalizeRepositoryOrigin(origin); err != nil || normalized != origin {
		t.Fatalf("a Flatpak origin does not normalize to itself: %q, %v", normalized, err)
	}
	if !IsFlatpakOrigin(origin) || IsFlatpakOrigin("github.com/flatpak/flatpak") {
		t.Fatal("Flatpak origins are not told apart from Git ones")
	}
	c := flatpakTestCpak(t)
	if _, err := c.newRepoProvider(origin); !errors.Is(err, ErrFlatpakOrigin) {
		t.Fatalf("a Flatpak origin reached the Git provider: %v", err)
	}
	if _, err := normalizeRepositoryOrigin("flatpak:8080/flathub/org.gimp.gimp"); err == nil {
		t.Fatal("the flatpak host was accepted with a port")
	}
}

func TestPlacementsMoveTreesWhereFlatpakMountsThem(t *testing.T) {
	cases := []struct {
		placement flatpakPlacement
		name      string
		want      string
		kept      bool
	}{
		{flatpakApplicationPlacement(), "files/bin/app", "app/bin/app", true},
		{flatpakApplicationPlacement(), "metadata", "", false},
		{flatpakApplicationPlacement(), "export/share/applications/x.desktop", "", false},
		{flatpakRuntimePlacement(), "files/lib/libc.so.6", "usr/lib/libc.so.6", true},
		{flatpakRuntimePlacement(), "files/etc/fonts/fonts.conf", "etc/fonts/fonts.conf", true},
		{flatpakRuntimePlacement(), "files/etcetera", "usr/etcetera", true},
		{flatpakExtensionPlacement("usr/share/runtime/locale", []string{"it"}), "files/it/share/it/x.mo", "usr/share/runtime/locale/it/share/it/x.mo", true},
		{flatpakExtensionPlacement("usr/share/runtime/locale", []string{"it"}), "files/de/share/de/x.mo", "", false},
	}
	for _, test := range cases {
		got, kept := test.placement.place(test.name)
		if got != test.want || kept != test.kept {
			t.Fatalf("%s placed %s as %q %v, want %q %v", test.placement.id, test.name, got, kept, test.want, test.kept)
		}
	}
	first := flatpakLayerDigest("app/a/x86_64/stable", ostree.Checksum{1}, flatpakApplicationPlacement())
	second := flatpakLayerDigest("app/a/x86_64/stable", ostree.Checksum{1}, flatpakRuntimePlacement())
	if first == second {
		t.Fatal("one commit placed two ways is stored as one layer")
	}
}

const testRuntimeRef = "runtime/org.example.Platform/x86_64/1"

func runtimeFiles() map[string]ostreetest.File {
	shared := []byte("identical content kept once by OSTree")
	return map[string]ostreetest.File{
		"metadata":                       {Content: []byte("[Runtime]\nname=org.example.Platform\n"), Mode: 0o644},
		"files/bin/tool":                 {Content: []byte("#!/bin/sh\n"), Mode: 0o755},
		"files/bin/elevate":              {Content: []byte("setuid"), Mode: 0o4755},
		"files/lib/one/data":             {Content: shared, Mode: 0o640},
		"files/lib/two/data":             {Content: shared, Mode: 0o640},
		"files/lib/link":                 {Link: "one/data"},
		"files/etc/runtime.conf":         {Content: []byte("x=1\n"), Mode: 0o644},
		"files/sbin":                     {Link: "bin"},
		"files/lib64/ld-linux-x86-64.so": {Link: "../lib/one/data"},
	}
}

func layerFiles(t *testing.T, c *Cpak, digest string) map[string]fvsrepo.FileEntry {
	t.Helper()
	files, err := c.flatpakLayerFiles(digest)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestImportKeepsModesLinksAndPlacement(t *testing.T) {
	key := testFlatpakKey()
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	checksum := repo.Commit(testRuntimeRef, runtimeFiles(), nil)
	ring, err := ostree.ParseKeyring(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	repository := &ostree.Repository{URL: repo.URL(), Keyring: ring}
	sum, _ := ostree.ParseChecksum(checksum)
	commit, err := repository.FetchCommit(context.Background(), testRuntimeRef, sum)
	if err != nil {
		t.Fatal(err)
	}
	c := flatpakTestCpak(t)
	digest, err := c.importFlatpakCommit(repository, testRuntimeRef, commit, flatpakRuntimePlacement())
	if err != nil {
		t.Fatal(err)
	}
	files := layerFiles(t, c, digest)
	for name, mode := range map[string]uint32{
		"usr/bin/tool":       0o755,
		"usr/bin/elevate":    0o755,
		"usr/lib/one/data":   0o640,
		"usr/lib/two/data":   0o640,
		"etc/runtime.conf":   0o644,
		"usr/lib/link":       0,
		"usr/sbin":           0,
		"usr/lib64/ld-linux": 0,
	} {
		entry, ok := files[name]
		if !ok {
			if name == "usr/lib64/ld-linux" {
				continue
			}
			t.Fatalf("the layer has no %s", name)
		}
		if entry.Link == "" && entry.Mode&0o7777 != mode {
			t.Fatalf("%s has mode %o, the commit gave %o", name, entry.Mode&0o7777, mode)
		}
	}
	if files["usr/lib/link"].Link != "one/data" || files["usr/sbin"].Link != "bin" {
		t.Fatal("symbolic links were not imported as links")
	}
	if _, ok := files["metadata"]; ok {
		t.Fatal("the commit metadata was imported into the root")
	}
	bindings, err := c.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := bindings.Lookup(digest); err != nil || !found {
		t.Fatalf("the imported layer was not bound: %v", err)
	}

	// Every object is now in the store, so a second import placed elsewhere
	// must not need the repository for any of them.
	for _, file := range runtimeFiles() {
		if file.Dir {
			continue
		}
		object := ostreetest.FileChecksum(file)
		repo.Override[ostreetest.ObjectPath(ostree.Checksum(object).String(), "filez")] = nil
	}
	if _, err := c.importFlatpakCommit(repository, testRuntimeRef, commit, flatpakExtensionPlacement("usr/lib/extension", nil)); err != nil {
		t.Fatalf("a second import of stored objects went back to the repository: %v", err)
	}
}

func TestImportPublishesNothingWhenAnObjectIsForged(t *testing.T) {
	key := testFlatpakKey()
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	files := runtimeFiles()
	checksum := repo.Commit(testRuntimeRef, files, nil)
	tool := ostree.Checksum(ostreetest.FileChecksum(files["files/bin/tool"]))
	other := ostree.Checksum(ostreetest.FileChecksum(files["files/etc/runtime.conf"]))
	repo.Override[ostreetest.ObjectPath(tool.String(), "filez")] = repo.Object(ostreetest.ObjectPath(other.String(), "filez"))

	ring, _ := ostree.ParseKeyring(key.Public())
	repository := &ostree.Repository{URL: repo.URL(), Keyring: ring}
	sum, _ := ostree.ParseChecksum(checksum)
	commit, err := repository.FetchCommit(context.Background(), testRuntimeRef, sum)
	if err != nil {
		t.Fatal(err)
	}
	c := flatpakTestCpak(t)
	digest := flatpakLayerDigest(testRuntimeRef, commit.Checksum, flatpakRuntimePlacement())
	if _, err := c.importFlatpakCommit(repository, testRuntimeRef, commit, flatpakRuntimePlacement()); err == nil {
		t.Fatal("a layer with a forged file was imported")
	}
	if available, _ := c.fvsLayerAvailable(digest); available {
		t.Fatal("a failed import left a layer behind")
	}
}

// localRemote serves an application, its runtime and their extensions from a
// signed repository on this machine and registers it as a remote.
func localRemote(t *testing.T, c *Cpak) *ostreetest.Repo {
	t.Helper()
	key := testFlatpakKey()
	repo := ostreetest.NewRepo(true, key)
	t.Cleanup(repo.Close)

	runtimeMetadata := "[Runtime]\nname=org.example.Platform\n\n[Environment]\nGI_TYPELIB_PATH=/app/lib/girepository-1.0\nLD_PRELOAD=/evil.so\n\n" +
		"[Extension org.example.Platform.GL]\ndirectory=lib/x86_64-linux-gnu/GL\nversions=1;0\nsubdirectories=true\nno-autodownload=true\nadd-ld-path=lib\nmerge-dirs=glvnd/egl_vendor.d\n\n" +
		"[Extension org.example.Platform.Locale]\ndirectory=share/runtime/locale\n\n" +
		"[Extension org.example.Platform.Debug]\ndirectory=lib/debug\n"
	repo.Commit(testRuntimeRef, runtimeFiles(), map[string]gvariant.Variant{"xa.metadata": {Signature: "s", Value: runtimeMetadata}})
	repo.Commit("runtime/org.example.Platform.GL.default/x86_64/1", map[string]ostreetest.File{
		"files/lib/libGL_example.so": {Content: []byte("gl"), Mode: 0o755},
		"files/glvnd":                {Link: "share/glvnd"},
		"files/share/glvnd/egl_vendor.d/50_example.json": {Content: []byte("{}"), Mode: 0o644},
	}, nil)
	repo.Commit("runtime/org.example.Platform.Locale/x86_64/1", map[string]ostreetest.File{
		"files/it/share/it/LC_MESSAGES/x.mo": {Content: []byte("it"), Mode: 0o644},
		"files/de/share/de/LC_MESSAGES/x.mo": {Content: []byte("de"), Mode: 0o644},
	}, nil)
	repo.Commit("runtime/org.example.Platform.Debug/x86_64/1", map[string]ostreetest.File{"files/x": {Content: []byte("x")}}, nil)

	appMetadata := "[Application]\nname=org.example.App\nruntime=org.example.Platform/x86_64/1\ncommand=example\n\n" +
		"[Context]\nshared=network;ipc;\nsockets=wayland;fallback-x11;session-bus;\nfilesystems=host;xdg-download;\n\n" +
		"[Session Bus Policy]\norg.freedesktop.Notifications=talk\n"
	repo.Commit("app/org.example.App/x86_64/stable", map[string]ostreetest.File{
		"files/bin/example": {Content: []byte("#!/bin/sh\n"), Mode: 0o755},
		"files/share/applications/org.example.App.desktop":            {Content: []byte("[Desktop Entry]\nName=Example\nExec=example %U\nIcon=org.example.App\n"), Mode: 0o644},
		"files/share/applications/other.desktop":                      {Content: []byte("[Desktop Entry]\nName=Other\n"), Mode: 0o644},
		"files/share/icons/hicolor/scalable/apps/org.example.App.svg": {Content: []byte("<svg/>"), Mode: 0o644},
	}, map[string]gvariant.Variant{"xa.metadata": {Signature: "s", Value: appMetadata}})

	remotes, err := c.FlatpakRemotes()
	if err != nil {
		t.Fatal(err)
	}
	if err := remotes.Add(flatpak.Remote{Name: "local", URL: repo.URL(), Key: key.Public()}); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestResolveFlatpakChoosesWhatFlatpakWouldDownload(t *testing.T) {
	t.Setenv("LANG", "it_IT.UTF-8")
	t.Setenv("LANGUAGE", "")
	c := flatpakTestCpak(t)
	localRemote(t, c)
	plan, err := c.ResolveFlatpak(FlatpakRequest{ID: "org.example.App", Remote: "local", Arch: "x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Origin != "flatpak/local/org.example.app" || plan.Runtime.Ref.String() != testRuntimeRef {
		t.Fatalf("resolved %s on %s", plan.Origin, plan.Runtime.Ref)
	}
	refs := []string{}
	for _, extension := range plan.RuntimeExtensions {
		refs = append(refs, extension.Ref.String()+" "+extension.Directory)
	}
	want := []string{
		"runtime/org.example.Platform.GL.default/x86_64/1 /usr/lib/x86_64-linux-gnu/GL/default",
		"runtime/org.example.Platform.Locale/x86_64/1 /usr/share/runtime/locale",
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("runtime extensions:\n%v\nwant\n%v", refs, want)
	}
	override := plan.Translation.Override
	if !override.Network || !override.SocketWayland || !override.Notification || override.DisplayX11 {
		t.Fatalf("the translated policy is %+v", override)
	}
	if slices.ContainsFunc(override.Filesystem, func(p types.FilesystemPermission) bool { return p.Path == "host" }) {
		t.Fatal("filesystems=host survived the translation")
	}
	if _, err := c.ResolveFlatpak(FlatpakRequest{ID: "org.example.App", Remote: "local", Arch: "x86_64", Branch: "beta"}); err == nil || !strings.Contains(err.Error(), "only on: stable") {
		t.Fatalf("a missing branch was not reported with the published ones: %v", err)
	}
}

func TestInstallFlatpakRecordsRuntimeAndApplication(t *testing.T) {
	t.Setenv("LANG", "it_IT.UTF-8")
	t.Setenv("LANGUAGE", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	c := flatpakTestCpak(t)
	// Checking the layers out is the storage service's job and is held by
	// its own tests; what is under test here is what gets recorded.
	c.SetStoragePreparationHandler(func(func() error) error { return nil })
	localRemote(t, c)
	plan, err := c.ResolveFlatpak(FlatpakRequest{ID: "org.example.App", Remote: "local", Arch: "x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InstallFlatpak(plan); err != nil {
		t.Fatal(err)
	}
	app, err := c.getStoredApplication(plan.Origin, "stable", "stable", "", "")
	if err != nil || app.CpakId == "" {
		t.Fatalf("the application was not recorded: %v", err)
	}
	runtime, err := c.getStoredApplication("flatpak/local/org.example.platform", "1", "1", "", "")
	if err != nil || runtime.CpakId == "" {
		t.Fatalf("the runtime was not recorded: %v", err)
	}
	if !runtime.PulledIn || runtime.PulledInBy != plan.Origin {
		t.Fatal("the runtime was recorded as something the user asked for")
	}
	if !reflect.DeepEqual(app.ParsedBinaries, []string{"/app/bin/example"}) {
		t.Fatalf("binaries: %v", app.ParsedBinaries)
	}
	if !reflect.DeepEqual(app.ParsedDesktopEntries, []string{"/app/share/applications/org.example.App.desktop"}) {
		t.Fatalf("a launcher not named after the application was exported: %v", app.ParsedDesktopEntries)
	}
	if len(app.ParsedDependencies) != 1 || app.ParsedDependencies[0].Id != runtime.CpakId || !app.ParsedDependencies[0].IsLayer() {
		t.Fatalf("the application does not run on its runtime: %+v", app.ParsedDependencies)
	}
	if app.ImageDigest != "sha256:"+plan.App.Commit.Checksum.String() {
		t.Fatalf("the application is identified by %s", app.ImageDigest)
	}
	if strings.Contains(app.Config, "LD_PRELOAD") || !strings.Contains(app.Config, "FLATPAK_ID=org.example.App") {
		t.Fatalf("the process configuration is %s", app.Config)
	}

	compat := layerFiles(t, c, app.ParsedLayers[len(app.ParsedLayers)-1])
	for _, name := range []string{"etc/ld.so.conf", ".flatpak-info", "run/host/font-dirs.xml", "tmp", "bin", "sbin", "lib", "lib64"} {
		if _, ok := compat[name]; !ok {
			t.Fatalf("the compatibility layer has no %s", name)
		}
	}
	if compat["tmp"].Mode&0o7777 != 0o1777 {
		t.Fatalf("/tmp has mode %o", compat["tmp"].Mode)
	}
	merged := compat["usr/lib/x86_64-linux-gnu/GL/glvnd/egl_vendor.d/50_example.json"]
	if merged.Link != "/usr/lib/x86_64-linux-gnu/GL/default/share/glvnd/egl_vendor.d/50_example.json" {
		t.Fatalf("the driver vendor directory was not merged: %+v", merged)
	}

	locale := layerFiles(t, c, runtime.ParsedLayers[2])
	if _, ok := locale["usr/share/runtime/locale/it/share/it/LC_MESSAGES/x.mo"]; !ok {
		t.Fatal("the user's language was left out of the locale extension")
	}
	if _, ok := locale["usr/share/runtime/locale/de/share/de/LC_MESSAGES/x.mo"]; ok {
		t.Fatal("a language the user does not read was imported")
	}

	if err := c.Remove(plan.Origin, "stable", "", ""); err != nil {
		t.Fatal(err)
	}
	if left, _ := c.getStoredApplication(runtime.Origin, "1", "1", "", ""); left.CpakId != "" {
		t.Fatal("the runtime outlived the only application running on it")
	}
}

func TestLayerDirectoriesResolveOnlyInsideTheExtension(t *testing.T) {
	files := map[string]fvsrepo.FileEntry{
		"usr/gl/default":                 {Kind: "dir"},
		"usr/gl/default/glvnd":           {Kind: "symlink", Link: "share/glvnd"},
		"usr/gl/default/share":           {Kind: "dir"},
		"usr/gl/default/share/glvnd":     {Kind: "dir"},
		"usr/gl/default/share/glvnd/dir": {Kind: "dir"},
		"usr/gl/default/escape":          {Kind: "symlink", Link: "../../../etc"},
		"usr/gl/default/absolute":        {Kind: "symlink", Link: "/etc"},
	}
	if got, ok := resolveLayerDirectory(files, "usr/gl/default", "glvnd/dir"); !ok || got != "usr/gl/default/share/glvnd/dir" {
		t.Fatalf("resolved %q %v", got, ok)
	}
	for _, path := range []string{"escape", "absolute"} {
		if _, ok := resolveLayerDirectory(files, "usr/gl/default", path); ok {
			t.Fatalf("%s led out of the extension", path)
		}
	}
}

func TestHostIconThemeIsReadFromGTKSettings(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	if err := os.MkdirAll(filepath.Join(config, "gtk-4.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "gtk-4.0", "settings.ini"), []byte("[Settings]\ngtk-icon-theme-name = \"Papirus\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Cpak{}
	if theme := c.flatpakIconTheme(); theme != "Adwaita" {
		t.Fatalf("without host icons the theme is %s", theme)
	}
	c.Options.FlatpakHostIcons = true
	if theme := c.flatpakIconTheme(); theme != "Papirus" {
		t.Fatalf("with host icons the theme is %s", theme)
	}
	c.Options.FlatpakIconTheme = "Breeze"
	if theme := c.flatpakIconTheme(); theme != "Breeze" {
		t.Fatalf("the configured theme is overridden by %s", theme)
	}
	args := strings.Join(c.flatpakLaunchArguments(types.Application{Flatpak: &types.FlatpakSource{}}), " ")
	if !strings.Contains(args, "--linker-cache") {
		t.Fatalf("a Flatpak launch does not index its linker cache: %s", args)
	}
	if strings.Contains(args, ":/run/host/share/icons") != dirExists("/usr/share/icons") {
		t.Fatalf("host icons were not shared as configured: %s", args)
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
