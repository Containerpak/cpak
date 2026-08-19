/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestFindIconPrefersScalableIcon(t *testing.T) {
	layerDir := t.TempDir()
	pngPath := filepath.Join(layerDir, "usr/share/icons/hicolor/128x128/apps/example.png")
	svgPath := filepath.Join(layerDir, "usr/share/icons/hicolor/scalable/apps/example.svg")
	for _, path := range []string{pngPath, svgPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("icon"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if got := findIcon(layerDir, "example"); got != svgPath {
		t.Fatalf("expected scalable icon %q, got %q", svgPath, got)
	}
}

func TestFindIconIgnoresFilesThatAreNotIcons(t *testing.T) {
	layerDir := t.TempDir()
	sourcesPath := filepath.Join(layerDir, "etc/apt/sources.list.d/vscode.sources")
	iconPath := filepath.Join(layerDir, "usr/share/pixmaps/vscode.png")
	for _, path := range []string{sourcesPath, iconPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if got := findIcon(layerDir, "vscode"); got != iconPath {
		t.Fatalf("expected image %q, got %q", iconPath, got)
	}
}

func TestExportBinaryForwardsFlagArguments(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{Origin: "github.com/containerpak/umu"}
	if err := c.exportBinary(app, "/usr/local/bin/umu-run"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(
		c.Options.ExportsPath,
		"github.com",
		"containerpak",
		"umu",
		"umu-run",
	)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "@/usr/local/bin/umu-run -- \"$@\"") {
		t.Fatalf("export does not preserve child flags: %q", content)
	}
}

func TestExportDesktopEntryUsesDiscoverableApplicationID(t *testing.T) {
	c := newTestCpak(t)
	layer := "desktop-layer"
	layerDir := c.GetInStoreDir("layers", layer)
	entry := "/usr/share/applications/example.desktop"
	entryPath := filepath.Join(layerDir, strings.TrimLeft(entry, "/"))
	if err := os.MkdirAll(filepath.Dir(entryPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("[Desktop Entry]\nName=Example\nExec=/usr/bin/example --test %U\nTryExec=/usr/bin/example\n"), 0644); err != nil {
		t.Fatal(err)
	}

	app := types.Application{
		CpakId:               "unsafe/base64=id",
		Origin:               "github.com/containerpak/example",
		ParsedDesktopEntries: []string{entry},
		ParsedLayers:         []string{layer},
	}
	legacyDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications", app.CpakId)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyIcon := filepath.Join(os.Getenv("HOME"), ".local", "share", "icons", app.CpakId+".png")
	if err := os.MkdirAll(filepath.Dir(legacyIcon), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyIcon, []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.createExports(app); err != nil {
		t.Fatal(err)
	}

	destination := desktopEntryExportPath(app, entry)
	if !regexp.MustCompile(`^cpak-[a-f0-9]{64}-example\.desktop$`).MatchString(filepath.Base(destination)) {
		t.Fatalf("invalid desktop entry ID: %s", filepath.Base(destination))
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := desktopLauncherPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Exec="+desktopExecArgument(launcher)+" run --desktop-launch github.com/containerpak/example --desktop-file-span 1,0 @/usr/bin/example -- --test %U") {
		t.Fatalf("desktop entry does not launch through cpak: %q", content)
	}
	if !strings.Contains(string(content), "TryExec="+launcher) {
		t.Fatalf("desktop entry checks guest binary availability: %q", content)
	}
	alias := originalDesktopEntryExportPath(entry)
	aliasContent, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"NoDisplay=true",
		"X-cpak-Origin=github.com/containerpak/example",
		"X-cpak-ID=unsafe/base64=id",
	} {
		if !strings.Contains(string(aliasContent), value) {
			t.Fatalf("desktop alias is missing %q: %s", value, aliasContent)
		}
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy desktop entry directory still exists: %s", legacyDir)
	}
	if _, err := os.Stat(legacyIcon); !os.IsNotExist(err) {
		t.Fatalf("legacy icon still exists: %s", legacyIcon)
	}
}

func TestExportDesktopAliasDoesNotReplaceUserEntry(t *testing.T) {
	c := newTestCpak(t)
	layer := "desktop-layer"
	layerDir := c.GetInStoreDir("layers", layer)
	entry := "/usr/share/applications/example.desktop"
	entryPath := filepath.Join(layerDir, strings.TrimLeft(entry, "/"))
	if err := os.MkdirAll(filepath.Dir(entryPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("[Desktop Entry]\nName=Example\nExec=/usr/bin/example\n"), 0644); err != nil {
		t.Fatal(err)
	}
	alias := originalDesktopEntryExportPath(entry)
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}
	const existing = "[Desktop Entry]\nName=User application\n"
	if err := os.WriteFile(alias, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	app := types.Application{
		CpakId:               "application-id",
		Origin:               "github.com/containerpak/example",
		ParsedDesktopEntries: []string{entry},
		ParsedLayers:         []string{layer},
	}
	if err := c.createExports(app); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != existing {
		t.Fatalf("user desktop entry was replaced: %q", content)
	}
}

func TestRemoveDesktopAliasChecksPackageIdentity(t *testing.T) {
	newTestCpak(t)
	entry := "example.desktop"
	path := originalDesktopEntryExportPath(entry)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := "[Desktop Entry]\nX-cpak-Origin=github.com/containerpak/example\nX-cpak-ID=new-id\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	old := types.Application{CpakId: "old-id", Origin: "github.com/containerpak/example"}
	if err := removeDesktopAlias(old, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("new alias was removed with the old package: %v", err)
	}
	updated := types.Application{CpakId: "new-id", Origin: old.Origin}
	if err := removeDesktopAlias(updated, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned alias was not removed: %v", err)
	}
}

func TestRewriteDesktopExecPreservesQuotedBinary(t *testing.T) {
	got := rewriteDesktopExec("/home/user/.local/bin/cpak", "github.com/containerpak/example", `"/opt/Example App/example" --new-window %U`)
	want := `Exec=/home/user/.local/bin/cpak run --desktop-launch github.com/containerpak/example --desktop-file-span 1,0 "@/opt/Example App/example" -- --new-window %U`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// A shim that says only "cpak" is resolved through PATH, which anyone who can
// write the home can rearrange.
func TestExportBinaryNamesTheLauncherOutright(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{Origin: "github.com/containerpak/umu"}
	if err := c.exportBinary(app, "/usr/local/bin/umu-run"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Options.ExportsPath, "github.com", "containerpak", "umu", "umu-run")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.Contains(line, " run ") {
			continue
		}
		command := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "exec "))
		if len(command) == 0 || !filepath.IsAbs(command[0]) {
			t.Fatalf("the shim resolves its launcher through PATH: %q", line)
		}
	}
}

// TestInstallRefusesAGrantOnTheStoreItActuallyUses drives the real install
// entry point with the tree relocated, which is what CPAK_INSTALLATION_PATH and
// the per-path variables do and what cpak itself does to install a local
// package. A refusal written against the default layout under the home would
// let this grant through and hand the application every other container, every
// policy and every broker token in the store.
func TestInstallRefusesAGrantOnTheStoreItActuallyUses(t *testing.T) {
	c := newTestCpak(t)
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{
		{Path: filepath.Join(c.Options.StorePath, "containers"), Access: "read-write"},
	}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err == nil {
		t.Fatal("accepted a grant on the store this installation uses")
	}
	if !strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("the install failed for another reason: %v", err)
	}
}

// TestInstallRefusesASessionGrantOnCpakState covers the same rule for a session,
// which is launched with grants of its own.
func TestInstallRefusesASessionGrantOnCpakState(t *testing.T) {
	c := newTestCpak(t)
	manifest := validManifestForTest()
	manifest.Sessions = []types.Session{{
		ID:         "desk",
		Name:       "Desk",
		Kind:       "desktop",
		Entrypoint: "/usr/bin/test",
		Override: types.Override{
			Filesystem: []types.FilesystemPermission{{Path: c.Options.ExportsPath, Access: "read-only"}},
		},
	}}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err == nil {
		t.Fatal("accepted a session grant on the exported launchers")
	}
	if !strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("the install failed for another reason: %v", err)
	}
}

// TestInstallAcceptsAGrantThatMerelyContainsCpakState keeps the refusal narrow:
// the home holds the state on a default installation, and it is hidden again
// when it is mounted rather than refused here.
func TestInstallAcceptsAGrantThatMerelyContainsCpakState(t *testing.T) {
	c := newTestCpak(t)
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{{Path: "home", Access: "read-write"}}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err != nil && strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("a grant that only contains the state was refused: %v", err)
	}
}

// TestInstallRefusesAGrantOnTheDeduplicatedStore covers the root nothing names.
// The file content of every installed package lives in the deduplication store,
// and the option that would name it is empty unless a configuration file said
// so: everything that opens that store resolves it through daBaDeeStoreOptions
// and takes the layout beside the store path when the option is blank. A
// refusal that read the option instead would leave the one place the bytes
// actually are as the one place a grant is still taken.
func TestInstallRefusesAGrantOnTheDeduplicatedStore(t *testing.T) {
	c := newTestCpak(t)
	if c.Options.DaBaDeeStoreOptions.Root != "" {
		t.Fatal("this test is about the root nothing named, and something named it")
	}
	resolved := c.daBaDeeStoreOptions().Root
	if resolved == "" {
		t.Fatal("the deduplication store resolved to nowhere")
	}
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{
		{Path: filepath.Join(resolved, "blobs"), Access: "read-write"},
	}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err == nil {
		t.Fatal("accepted a grant on the deduplicated content of every installed package")
	}
	if !strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("the install failed for another reason: %v", err)
	}
}

// dependencyLock puts a package and the dependency it declares in a lock, so
// that an installation of the first resolves the second without reaching a
// repository host.
func dependencyLock(origin, image string, manifest *types.CpakManifest, dependencyOrigin string, dependency *types.CpakManifest) *types.ManifestLock {
	return &types.ManifestLock{
		LockVersion: types.ManifestLockVersion,
		Root: types.LockedPackage{
			Origin: origin, Branch: "main", ResolvedImage: image, Manifest: manifest,
		},
		Dependencies: []types.LockedPackage{{
			Origin: dependencyOrigin, Branch: "main", ResolvedImage: image, Manifest: dependency,
		}},
	}
}

// The defect this test exists for. A dependency is a package the publisher of
// what the user asked for chose, and it used to land in the menu with its own
// launchers, so a user could start a package they were never shown under the
// permissions its own manifest asked for.
func TestADependencyIsNotExportedToTheHost(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	publishImage(t, registry, "main")
	image := ref.ContextName() + ":main"

	dependencyOrigin := "github.com/user/library"
	dependency := newTestManifest()
	dependency.Name = "library"
	dependency.Image = image
	dependency.Binaries = []string{"/usr/bin/library"}

	manifest := newTestManifest()
	manifest.Image = image
	manifest.Dependencies = []types.Dependency{{Origin: dependencyOrigin, Branch: "main"}}

	options := InstallOptions{
		CreateExports:   true,
		ResolveImageRef: true,
		ManifestLock:    dependencyLock(testOrigin, image, manifest, dependencyOrigin, dependency),
	}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("the install failed: %v", err)
	}

	installed := storedApplications(t, cp)
	if len(installed) != 2 {
		t.Fatalf("got %d installed packages, want the package and the dependency it pulls in: %+v", len(installed), installed)
	}
	for _, app := range installed {
		if app.Origin == dependencyOrigin && !app.PulledIn {
			t.Fatal("the store cannot tell the dependency apart from a package the user named")
		}
		if app.Origin == testOrigin && app.PulledIn {
			t.Fatal("the package the user named was recorded as one it never named")
		}
	}

	named := filepath.Join(cp.Options.ExportsPath, "github.com", "user", "demo", "demo")
	if _, err := os.Stat(named); err != nil {
		t.Fatalf("the package the user named was not exported: %v", err)
	}
	pulledIn := filepath.Join(cp.Options.ExportsPath, "github.com", "user", "library", "library")
	if _, err := os.Stat(pulledIn); !os.IsNotExist(err) {
		t.Fatalf("a dependency the user never named was exported to %s (%v)", pulledIn, err)
	}
}

// What the user is asked to agree to has to name every package the installation
// pulls in, however deep, and the permissions each of them asks for.
func TestResolveDependenciesAnswersWithEveryPulledInManifest(t *testing.T) {
	cp := newTestCpak(t)

	library := newTestManifest()
	library.Name = "library"
	library.Override = types.Override{Network: true}
	library.Dependencies = []types.Dependency{{Origin: "runtime", Release: "24.04"}}

	runtime := newTestManifest()
	runtime.Name = "runtime"
	runtime.Override = types.Override{FsHost: true}

	// The runtime declares the library back, which is a loop an answer must
	// come out of.
	runtime.Dependencies = []types.Dependency{{Origin: "github.com/user/library", Branch: "main"}}

	manifest := newTestManifest()
	manifest.Dependencies = []types.Dependency{{Origin: "github.com/user/library", Branch: "main"}}

	fetched := []string{}
	fetch := func(origin, branch, release, commit string) (*types.CpakManifest, error) {
		fetched = append(fetched, origin)
		switch origin {
		case "github.com/user/library":
			return library, nil
		case "github.com/user/runtime":
			return runtime, nil
		}
		return nil, errors.New("no such repository: " + origin)
	}

	resolved, err := cp.resolveDependencies(testOrigin, manifest, fetch, map[string]bool{})
	if err != nil {
		t.Fatalf("the dependencies of the package could not be resolved: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("got %d dependencies, want the library and the runtime it pulls in: %+v", len(resolved), resolved)
	}
	if resolved[0].Origin != "github.com/user/library" || !resolved[0].Manifest.Override.Network {
		t.Fatalf("got %+v, want the library under its own origin with the permissions it asks for", resolved[0])
	}
	if resolved[1].Origin != "github.com/user/runtime" || resolved[1].Release != "24.04" || !resolved[1].Manifest.Override.FsHost {
		t.Fatalf("got %+v, want the runtime the library pulls in, at the release it names", resolved[1])
	}
	if len(fetched) != 2 {
		t.Fatalf("the manifests were fetched %d times, want once each: %v", len(fetched), fetched)
	}
}

// A dependency manifest is put to the user in the consent prompt, above the
// question that grants the permissions in it, so it is held to the same rules
// as every other fetched manifest before it is described rather than when the
// install eventually reaches it.
func TestResolveDependenciesRefusesAManifestNothingWouldAccept(t *testing.T) {
	cp := newTestCpak(t)

	library := newTestManifest()
	library.Name = "library"
	library.Description = ""

	manifest := newTestManifest()
	manifest.Dependencies = []types.Dependency{{Origin: "github.com/user/library", Branch: "main"}}

	fetch := func(origin, branch, release, commit string) (*types.CpakManifest, error) {
		return library, nil
	}

	if _, err := cp.resolveDependencies(testOrigin, manifest, fetch, map[string]bool{}); err == nil {
		t.Fatal("a dependency manifest nothing had checked was handed back to be printed")
	}
}

// The manifests the prompt resolved are the ones the install uses, so a
// dependency is not fetched a second time and the two cannot disagree.
func TestAnInstallTakesTheDependencyManifestTheCallerAlreadyFetched(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	publishImage(t, registry, "main")
	image := ref.ContextName() + ":main"

	dependencyOrigin := "github.com/user/library"
	dependency := newTestManifest()
	dependency.Name = "library"
	dependency.Image = image

	manifest := newTestManifest()
	manifest.Image = image
	manifest.Dependencies = []types.Dependency{{Origin: dependencyOrigin, Branch: "main"}}

	// No lock and no repository host: the only way this install can resolve
	// the dependency is the manifest the caller already has in hand.
	options := InstallOptions{
		CreateExports:   true,
		ResolveImageRef: true,
		ResolvedDependencies: []ResolvedDependency{{
			Origin:   dependencyOrigin,
			Branch:   "main",
			Manifest: dependency,
		}},
	}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("the install fetched the dependency again instead of using the resolved manifest: %v", err)
	}
	if len(storedApplications(t, cp)) != 2 {
		t.Fatal("the dependency was not installed from the manifest the caller resolved")
	}
}

// The record has to say which package brought a dependency here, and not only
// that something did: only that one has a say in how the dependency starts on
// its own, or any publisher could narrow an installation they had nothing to do
// with by declaring it.
func TestTheStoreRecordsWhichPackagePulledADependencyIn(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	publishImage(t, registry, "main")
	image := ref.ContextName() + ":main"

	dependencyOrigin := "github.com/user/library"
	dependency := newTestManifest()
	dependency.Name = "library"
	dependency.Image = image

	manifest := newTestManifest()
	manifest.Image = image
	manifest.Dependencies = []types.Dependency{{Origin: dependencyOrigin, Branch: "main"}}

	options := InstallOptions{
		CreateExports:   true,
		ResolveImageRef: true,
		ManifestLock:    dependencyLock(testOrigin, image, manifest, dependencyOrigin, dependency),
	}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("the install failed: %v", err)
	}

	for _, app := range storedApplications(t, cp) {
		if app.Origin != dependencyOrigin {
			continue
		}
		if app.PulledInBy != testOrigin {
			t.Fatalf("the dependency was recorded as pulled in by %q, want %q", app.PulledInBy, testOrigin)
		}
		return
	}
	t.Fatal("the dependency was not installed")
}

// The way back out. A package that arrived as somebody else's dependency is
// held to that package and has no launchers of its own; naming it in an install
// is the user asking for it in their own right, and there is nothing else in
// cpak that can say so.
func TestInstallingADependencyByNameMakesItThePackageTheUserNamed(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	publishImage(t, registry, "main")
	image := ref.ContextName() + ":main"

	dependencyOrigin := "github.com/user/library"
	dependency := newTestManifest()
	dependency.Name = "library"
	dependency.Image = image
	dependency.Binaries = []string{"/usr/bin/library"}

	manifest := newTestManifest()
	manifest.Image = image
	manifest.Dependencies = []types.Dependency{{Origin: dependencyOrigin, Branch: "main"}}

	options := InstallOptions{
		CreateExports:   true,
		ResolveImageRef: true,
		ManifestLock:    dependencyLock(testOrigin, image, manifest, dependencyOrigin, dependency),
	}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("the install failed: %v", err)
	}

	// What the user types when they want the library itself. The origin is
	// already installed, so this reaches the same refusal to install it twice
	// that every repeated install does.
	if err := cp.InstallCpakWithOptions(dependencyOrigin, dependency, "main", "", "", InstallOptions{
		CreateExports:   true,
		ResolveImageRef: true,
	}); err != nil {
		t.Fatalf("naming an already installed dependency failed: %v", err)
	}

	found := false
	for _, app := range storedApplications(t, cp) {
		if app.Origin != dependencyOrigin {
			continue
		}
		found = true
		if app.PulledIn || app.PulledInBy != "" {
			t.Fatalf("the user named the package and the record still says it was pulled in: %+v", app)
		}
	}
	if !found {
		t.Fatal("the dependency is no longer installed")
	}

	named := filepath.Join(cp.Options.ExportsPath, "github.com", "user", "library", "library")
	if _, err := os.Stat(named); err != nil {
		t.Fatalf("the package the user named was left without the launcher it asked for: %v", err)
	}
}

// An installation writes two answers about a dependency one line apart in the
// same record, and either of them can be lost while the other still passes: the
// grants of a v1 package are stored in the form the rest of cpak reads, and
// whether the user ever named the package is stored beside them. Dropping the
// first leaves a legacy mount nothing masks or weighs; dropping the second
// hands a package nobody was shown its own launchers and its own policy. This
// holds both at once so neither can go quiet again.
func TestAV1DependencyIsStoredMigratedAndAsOneTheUserNeverNamed(t *testing.T) {
	cp := newSignatureCpak(t)
	useEnrolmentAuthority(t)
	registry := newSignatureRegistry()
	ref := registry.start(t)
	publishImage(t, registry, "main")
	image := ref.ContextName() + ":main"

	dependencyOrigin := "github.com/user/library"
	dependency := newTestManifest()
	dependency.Name = "library"
	dependency.Image = image
	dependency.ManifestVersion = "1.0"
	dependency.Override.FsHostHome = true

	manifest := newTestManifest()
	manifest.Image = image
	manifest.Dependencies = []types.Dependency{{Origin: dependencyOrigin, Branch: "main"}}

	options := InstallOptions{
		CreateExports:   true,
		ResolveImageRef: true,
		ManifestLock:    dependencyLock(testOrigin, image, manifest, dependencyOrigin, dependency),
	}
	if err := cp.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", options); err != nil {
		t.Fatalf("the install failed: %v", err)
	}

	for _, app := range storedApplications(t, cp) {
		if app.Origin != dependencyOrigin {
			continue
		}
		if app.ParsedOverride.FsHostHome {
			t.Fatalf("the legacy home mount was stored as the manifest wrote it: %+v", app.ParsedOverride)
		}
		want := []types.FilesystemPermission{{Path: "home", Access: "read-write"}}
		if !reflect.DeepEqual(app.ParsedOverride.Filesystem, want) {
			t.Fatalf("stored grants: got %v, want %v", app.ParsedOverride.Filesystem, want)
		}
		if !app.PulledIn || app.PulledInBy != testOrigin {
			t.Fatalf("the record does not say the user never named this package: %+v", app)
		}
		return
	}
	t.Fatal("the dependency was not installed")
}
