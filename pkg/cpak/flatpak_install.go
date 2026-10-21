/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/ostree"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const flatpakUserAgent = "cpak (+https://github.com/containerpak/cpak)"

// FlatpakRequest names a Flatpak application to install. A .flatpakref, when
// given, supplies the remote, the id and the branch the request leaves out.
type FlatpakRequest struct {
	ID      string
	Branch  string
	Remote  string
	Arch    string
	RefFile []byte

	// newRemote is a remote cpak does not know yet, taken from a host
	// installation the application is migrated from.
	newRemote *flatpak.Remote
}

// FlatpakPart is one signed commit an installation is made of.
type FlatpakPart struct {
	Ref      flatpak.Ref
	Commit   ostree.Commit
	Metadata flatpak.Metadata

	// Directory is where an extension is mounted, absolute in the
	// application's root.
	Directory string
	Point     flatpak.ExtensionPoint

	placement flatpakPlacement
}

// FlatpakPlan is everything an installation will fetch, resolved and verified
// before anything is downloaded, so it can be put to the user first.
type FlatpakPlan struct {
	Remote    flatpak.Remote
	AddRemote bool

	App               FlatpakPart
	Runtime           FlatpakPart
	AppExtensions     []FlatpakPart
	RuntimeExtensions []FlatpakPart

	Translation flatpak.Translation
	Origin      string

	repository *ostree.Repository
	summary    ostree.Summary
}

func (c *Cpak) flatpakRemotesPath() string {
	return filepath.Join(filepath.Dir(c.Options.RegistryAuthPath), "flatpak-remotes.json")
}

// FlatpakRemotes answers with the configured Flatpak remotes.
func (c *Cpak) FlatpakRemotes() (*flatpak.Remotes, error) {
	return flatpak.LoadRemotes(c.flatpakRemotesPath())
}

// ResolveFlatpak resolves an application, its runtime and the extensions
// both need, verifying every signature on the way, and translates what the
// application asks for into a cpak policy. Nothing is downloaded but metadata.
func (c *Cpak) ResolveFlatpak(request FlatpakRequest) (*FlatpakPlan, error) {
	remotes, err := c.FlatpakRemotes()
	if err != nil {
		return nil, err
	}
	plan := &FlatpakPlan{}
	if len(request.RefFile) > 0 {
		refFile, err := flatpak.ParseRefFile(request.RefFile)
		if err != nil {
			return nil, err
		}
		if refFile.Runtime {
			return nil, errors.New("the .flatpakref describes a runtime, which is installed with the application that needs it")
		}
		if request.ID == "" {
			request.ID = refFile.Name
		}
		if request.Branch == "" {
			request.Branch = refFile.Branch
		}
		remote, known := remotes.ByURL(refFile.URL)
		switch {
		case known && !bytes.Equal(remote.Key, refFile.Key):
			return nil, fmt.Errorf("the .flatpakref names the repository of remote %s with a different key; refusing it", remote.Name)
		case known:
		default:
			name := refFile.SuggestRemoteName
			if flatpak.ValidateRemoteName(name) != nil {
				return nil, errors.New("the .flatpakref names an unknown repository without a usable remote name; add the remote first")
			}
			if _, err := remotes.Get(name); err == nil {
				return nil, fmt.Errorf("a different remote named %s is already configured", name)
			}
			remote = flatpak.Remote{Name: name, Title: refFile.Title, URL: refFile.URL, Key: refFile.Key}
			plan.AddRemote = true
		}
		if request.Remote != "" && request.Remote != remote.Name {
			return nil, fmt.Errorf("the .flatpakref belongs to remote %s, not %s", remote.Name, request.Remote)
		}
		plan.Remote = remote
	} else if request.newRemote != nil {
		plan.Remote = *request.newRemote
		plan.AddRemote = true
	} else {
		name := request.Remote
		if name == "" {
			name = flatpak.DefaultRemote
		}
		if plan.Remote, err = remotes.Get(name); err != nil {
			return nil, err
		}
	}
	if err := flatpak.ValidateID(request.ID); err != nil {
		return nil, err
	}
	if request.Branch == "" {
		request.Branch = "stable"
	}
	if request.Arch == "" {
		request.Arch = flatpak.HostArch()
	}

	if plan.repository, err = plan.Remote.Repository(flatpakUserAgent); err != nil {
		return nil, err
	}
	if plan.summary, err = plan.repository.FetchSummary(c.Ctx, request.Arch); err != nil {
		return nil, err
	}

	appRef := flatpak.Ref{Kind: flatpak.KindApp, ID: request.ID, Arch: request.Arch, Branch: request.Branch}
	if plan.App, err = c.resolveFlatpakPart(plan, appRef, true); err != nil {
		return nil, err
	}
	if plan.App.Metadata.Runtime || plan.App.Metadata.Name != request.ID {
		return nil, fmt.Errorf("the metadata of %s describes %s", appRef, plan.App.Metadata.Name)
	}
	if plan.App.Metadata.Command == "" {
		return nil, fmt.Errorf("%s declares no command to start", request.ID)
	}
	runtimeRef := plan.App.Metadata.RuntimeRef
	if runtimeRef.Arch != request.Arch {
		return nil, fmt.Errorf("%s needs a runtime for %s", request.ID, runtimeRef.Arch)
	}
	if plan.Runtime, err = c.resolveFlatpakPart(plan, runtimeRef, true); err != nil {
		return nil, err
	}
	if !plan.Runtime.Metadata.Runtime {
		return nil, fmt.Errorf("%s is not a runtime", runtimeRef)
	}
	plan.App.placement = flatpakApplicationPlacement()
	plan.Runtime.placement = flatpakRuntimePlacement()

	if plan.RuntimeExtensions, err = c.resolveFlatpakExtensions(plan, plan.Runtime, "/usr"); err != nil {
		return nil, err
	}
	if plan.AppExtensions, err = c.resolveFlatpakExtensions(plan, plan.App, "/app"); err != nil {
		return nil, err
	}
	plan.Translation = flatpak.Translate(plan.App.Metadata)
	plan.Origin = FlatpakOrigin(plan.Remote.Name, request.ID)
	if _, err := normalizeRepositoryOrigin(plan.Origin); err != nil {
		return nil, err
	}
	return plan, nil
}

func (c *Cpak) resolveFlatpakPart(plan *FlatpakPlan, ref flatpak.Ref, withMetadata bool) (FlatpakPart, error) {
	listed, ok := plan.summary.Refs[ref.String()]
	if !ok {
		return FlatpakPart{}, c.missingFlatpakRef(plan, ref)
	}
	commit, err := plan.repository.FetchCommit(c.Ctx, ref.String(), listed.Checksum)
	if err != nil {
		return FlatpakPart{}, err
	}
	part := FlatpakPart{Ref: ref, Commit: commit}
	if withMetadata {
		raw := commit.String("xa.metadata")
		if raw == "" {
			return FlatpakPart{}, fmt.Errorf("%s carries no metadata", ref)
		}
		if part.Metadata, err = flatpak.ParseMetadata([]byte(raw)); err != nil {
			return FlatpakPart{}, fmt.Errorf("%s: %w", ref, err)
		}
	}
	if eol := commit.String("ostree.endoflife"); eol != "" {
		logger.Printf("Warning: %s has reached its end of life: %s", ref, sanitizedText(eol))
	}
	return part, nil
}

func (c *Cpak) missingFlatpakRef(plan *FlatpakPlan, ref flatpak.Ref) error {
	branches := []string{}
	prefix := string(ref.Kind) + "/" + ref.ID + "/" + ref.Arch + "/"
	for name := range plan.summary.Refs {
		if branch, ok := strings.CutPrefix(name, prefix); ok {
			branches = append(branches, branch)
		}
	}
	sort.Strings(branches)
	if len(branches) == 0 {
		return fmt.Errorf("remote %s does not publish %s for %s", plan.Remote.Name, ref.ID, ref.Arch)
	}
	return fmt.Errorf("remote %s publishes %s only on: %s", plan.Remote.Name, ref.ID, strings.Join(branches, ", "))
}

// Extensions that hold debugging symbols, sources or documentation are never
// part of what an application needs to run.
func skippedExtension(id string) bool {
	return strings.HasSuffix(id, ".Debug") || strings.HasSuffix(id, ".Sources") || strings.HasSuffix(id, ".Docs")
}

// glExtensionPoint reports the point a runtime takes its OpenGL driver from.
// Flatpak picks a driver extension at run time; cpak always installs the
// Mesa one and leaves NVIDIA to its own driver support, which uses the
// libraries of the host driver and so always matches the kernel module.
func glExtensionPoint(point flatpak.ExtensionPoint) bool {
	return strings.HasSuffix(point.Name, ".GL") && point.Subdirectories
}

// resolveFlatpakExtensions picks the extensions Flatpak would download for a
// fresh installation: every point that does not opt out of automatic
// downloads, the Mesa driver for the GL point, and of each locale extension
// only the languages the user reads.
func (c *Cpak) resolveFlatpakExtensions(plan *FlatpakPlan, parent FlatpakPart, base string) ([]FlatpakPart, error) {
	languages := flatpakLanguages()
	var parts []FlatpakPart
	for _, point := range parent.Metadata.Extensions {
		if skippedExtension(point.Name) {
			continue
		}
		var candidates []string
		switch {
		case glExtensionPoint(point):
			candidates = []string{point.Name + ".default"}
		case point.NoAutoDownload:
			continue
		case point.Subdirectories:
			prefix := "runtime/" + point.Name + "."
			seen := map[string]bool{}
			for name := range plan.summary.Refs {
				rest, ok := strings.CutPrefix(name, prefix)
				if !ok {
					continue
				}
				id := point.Name + "." + strings.Split(rest, "/")[0]
				if point.Matches(id) && !skippedExtension(id) && !seen[id] {
					seen[id] = true
					candidates = append(candidates, id)
				}
			}
			sort.Strings(candidates)
		default:
			candidates = []string{point.Name}
		}
		for _, id := range candidates {
			part, found, err := c.resolveFlatpakExtension(plan, parent, point, id)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			part.Directory = path.Join(base, point.MountPoint(id))
			var keep []string
			if strings.HasSuffix(id, ".Locale") || point.LocaleSubset {
				keep = languages
			}
			part.placement = flatpakExtensionPlacement(strings.TrimPrefix(part.Directory, "/"), keep)
			parts = append(parts, part)
		}
	}
	return parts, nil
}

func (c *Cpak) resolveFlatpakExtension(plan *FlatpakPlan, parent FlatpakPart, point flatpak.ExtensionPoint, id string) (FlatpakPart, bool, error) {
	if flatpak.ValidateID(id) != nil {
		return FlatpakPart{}, false, nil
	}
	for _, branch := range point.Branches(parent.Ref.Branch) {
		ref := flatpak.Ref{Kind: flatpak.KindRuntime, ID: id, Arch: parent.Ref.Arch, Branch: branch}
		if _, listed := plan.summary.Refs[ref.String()]; !listed {
			continue
		}
		part, err := c.resolveFlatpakPart(plan, ref, false)
		if err != nil {
			return FlatpakPart{}, false, err
		}
		part.Point = point
		return part, true, nil
	}
	return FlatpakPart{}, false, nil
}

// flatpakLanguages answers with the languages a locale extension is reduced
// to, read from the user's own locale settings.
func flatpakLanguages() []string {
	seen := map[string]bool{}
	var languages []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == "C" || value == "POSIX" || strings.HasPrefix(value, "C.") {
			return
		}
		for _, separator := range []string{".", "@", "_"} {
			value, _, _ = strings.Cut(value, separator)
		}
		value = strings.ToLower(value)
		if value == "" || len(value) > 8 || seen[value] {
			return
		}
		for _, char := range value {
			if char < 'a' || char > 'z' {
				return
			}
		}
		seen[value] = true
		languages = append(languages, value)
	}
	for _, value := range strings.Split(os.Getenv("LANGUAGE"), ":") {
		add(value)
	}
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		add(os.Getenv(name))
	}
	add("en")
	return languages
}

// InstallFlatpak installs a resolved plan: it imports every commit into the
// layer store, records the runtime as a package of its own, and records the
// application on top of it with the translated policy.
func (c *Cpak) InstallFlatpak(plan *FlatpakPlan) error {
	if plan.AddRemote {
		remotes, err := c.FlatpakRemotes()
		if err != nil {
			return err
		}
		if err := remotes.Add(plan.Remote); err != nil {
			return err
		}
	}
	if existing, _ := c.getStoredApplication(plan.Origin, plan.App.Ref.Branch, plan.App.Ref.Branch, "", ""); existing.CpakId != "" {
		logger.Printf("%s is already installed from %s; run cpak update to move it to a newer build", plan.App.Ref.ID, plan.Remote.Name)
		return nil
	}

	runtime, err := c.installFlatpakRuntime(plan)
	if err != nil {
		return err
	}
	app, err := c.flatpakApplicationRecord(plan, runtime)
	if err != nil {
		return err
	}
	c.reportFlatpak(FlatpakProgress{Phase: FlatpakPhaseInstall, Ref: plan.App.Ref.String()})
	if err := c.PrepareApplicationStorage(app); err != nil {
		return err
	}
	if err := c.createExports(app); err != nil {
		return err
	}
	if err := c.storeApplication(app); err != nil {
		return err
	}
	c.EnrolPublishedApplication(app, PublishedPackage{})
	c.aliasFlatpakApplication(plan.App.Ref.ID, app.Origin)
	return nil
}

func (c *Cpak) installFlatpakRuntime(plan *FlatpakPlan) (types.Application, error) {
	ref := plan.Runtime.Ref
	origin := FlatpakOrigin(plan.Remote.Name, ref.ID)
	if existing, _ := c.getStoredApplication(origin, ref.Branch, ref.Branch, "", ""); existing.CpakId != "" {
		return existing, nil
	}
	layers, extensions, err := c.importFlatpakParts(plan, plan.Runtime, plan.RuntimeExtensions)
	if err != nil {
		return types.Application{}, err
	}
	runtime := types.Application{
		CpakId:           flatpakCpakID(ref, origin),
		Name:             ref.ID,
		Version:          ref.Branch,
		Branch:           ref.Branch,
		Origin:           origin,
		InstallTimestamp: time.Now(),
		ImageDigest:      "sha256:" + plan.Runtime.Commit.Checksum.String(),
		ParsedLayers:     layers,
		ParsedOverride:   types.NewOverride(),
		Config:           "{}",
		PulledIn:         true,
		PulledInBy:       plan.Origin,
		Flatpak: &types.FlatpakSource{
			Remote:     plan.Remote.Name,
			URL:        plan.Remote.URL,
			Ref:        ref.String(),
			Commit:     plan.Runtime.Commit.Checksum.String(),
			Extensions: extensions,
		},
	}
	if err := c.storeApplication(runtime); err != nil {
		return types.Application{}, err
	}
	c.EnrolPublishedApplication(runtime, PublishedPackage{})
	return runtime, nil
}

func flatpakCpakID(ref flatpak.Ref, origin string) string {
	return base64.StdEncoding.EncodeToString([]byte(ref.ID + ":branch:" + ref.Branch + ":" + origin))
}

func (c *Cpak) importFlatpakParts(plan *FlatpakPlan, main FlatpakPart, extensions []FlatpakPart) ([]string, []types.FlatpakExtension, error) {
	layer, err := c.importFlatpakCommit(plan.repository, main.Ref.String(), main.Commit, main.placement)
	if err != nil {
		return nil, nil, err
	}
	layers := []string{layer}
	records := make([]types.FlatpakExtension, 0, len(extensions))
	for _, extension := range extensions {
		digest, err := c.importFlatpakCommit(plan.repository, extension.Ref.String(), extension.Commit, extension.placement)
		if err != nil {
			return nil, nil, err
		}
		layers = append(layers, digest)
		records = append(records, types.FlatpakExtension{
			Ref:       extension.Ref.String(),
			Commit:    extension.Commit.Checksum.String(),
			Directory: extension.Directory,
			Layer:     digest,
		})
	}
	return layers, records, nil
}

func (c *Cpak) flatpakApplicationRecord(plan *FlatpakPlan, runtime types.Application) (types.Application, error) {
	layers, extensions, err := c.importFlatpakParts(plan, plan.App, plan.AppExtensions)
	if err != nil {
		return types.Application{}, err
	}
	appFiles, err := c.flatpakLayerFiles(layers[0])
	if err != nil {
		return types.Application{}, err
	}
	compat, err := c.buildFlatpakCompatLayer(plan, runtime)
	if err != nil {
		return types.Application{}, err
	}
	layers = append(layers, compat)

	override := plan.Translation.Override
	if err := c.refuseCpakStateGrants(override, nil); err != nil {
		return types.Application{}, err
	}
	config, err := flatpakConfig(plan)
	if err != nil {
		return types.Application{}, err
	}
	ref := plan.App.Ref
	refused := make([]string, 0, len(plan.Translation.Refusals()))
	for _, finding := range plan.Translation.Refusals() {
		refused = append(refused, finding.Request)
	}
	app := types.Application{
		CpakId:               flatpakCpakID(ref, plan.Origin),
		Name:                 ref.ID,
		Version:              ref.Branch,
		Branch:               ref.Branch,
		Origin:               plan.Origin,
		InstallTimestamp:     time.Now(),
		ParsedBinaries:       []string{flatpakCommandPath(plan.App.Metadata.Command)},
		ParsedDesktopEntries: flatpakDesktopEntries(appFiles, ref.ID),
		ParsedDependencies: []types.Dependency{{
			Id:     runtime.CpakId,
			Origin: runtime.Origin,
			Branch: runtime.Branch,
			Mode:   "layer",
		}},
		ImageDigest:    "sha256:" + plan.App.Commit.Checksum.String(),
		ParsedLayers:   layers,
		ParsedOverride: override,
		Config:         config,
		Flatpak: &types.FlatpakSource{
			Remote:     plan.Remote.Name,
			URL:        plan.Remote.URL,
			Ref:        ref.String(),
			Commit:     plan.App.Commit.Checksum.String(),
			Runtime:    plan.Runtime.Ref.String(),
			Extensions: extensions,
			Refused:    refused,
		},
	}
	return app, nil
}

func flatpakCommandPath(command string) string {
	if path.IsAbs(command) {
		return path.Clean(command)
	}
	return path.Join("/app/bin", command)
}

// flatpakDesktopEntries are the launchers an application exports: the ones
// named after its id, which is the only kind Flatpak itself lets out.
func flatpakDesktopEntries(files map[string]fvsrepo.FileEntry, id string) []string {
	var entries []string
	for _, name := range sortedKeys(files) {
		base := path.Base(name)
		if path.Dir(name) != "app/share/applications" || !strings.HasSuffix(base, ".desktop") {
			continue
		}
		stem := strings.TrimSuffix(base, ".desktop")
		if stem != id && !strings.HasPrefix(stem, id+".") {
			continue
		}
		if _, err := desktopEntryExportName(base); err != nil {
			continue
		}
		entries = append(entries, "/"+name)
	}
	return entries
}

func (c *Cpak) aliasFlatpakApplication(id, origin string) {
	elements := strings.Split(id, ".")
	name := strings.ToLower(elements[len(elements)-1])
	if _, err := normalizeAliasName(name); err != nil {
		return
	}
	if existing, err := c.ResolveOrigin(name); err == nil && existing != "" {
		return
	}
	if err := c.SetAlias(name, origin); err == nil {
		logger.Printf("%s can be started as: cpak run %s", id, name)
	}
}

func sanitizedText(value string) string {
	return strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, value)
}

// libraryTriplet is the multiarch directory Flatpak runtimes keep their
// libraries in.
func libraryTriplet(arch string) string {
	switch arch {
	case "aarch64":
		return "aarch64-linux-gnu"
	case "i386":
		return "i386-linux-gnu"
	}
	return "x86_64-linux-gnu"
}

// flatpakConfig is the process configuration of a Flatpak application, in
// the shape an OCI image carries it: the runtime's environment, filtered the
// same way the application's is, and what Flatpak itself sets.
func flatpakConfig(plan *FlatpakPlan) (string, error) {
	environment := map[string]string{
		"PATH":          "/app/bin:/usr/bin",
		"FLATPAK_ID":    plan.App.Ref.ID,
		"XDG_DATA_DIRS": "/app/share:/usr/share:/usr/share/runtime/share",
	}
	for name, value := range plan.Runtime.Metadata.Environment {
		if strings.HasPrefix(name, "LD_") {
			continue
		}
		environment[name] = value
	}
	for _, extension := range plan.RuntimeExtensions {
		if glExtensionPoint(extension.Point) {
			gl := "/usr/" + path.Clean(extension.Point.Directory)
			environment["__EGL_VENDOR_LIBRARY_DIRS"] = gl + "/glvnd/egl_vendor.d:/usr/share/glvnd/egl_vendor.d"
			environment["LIBGL_DRIVERS_PATH"] = gl + "/lib/dri"
			break
		}
	}
	env := make([]string, 0, len(environment))
	for _, name := range sortedKeys(environment) {
		env = append(env, name+"="+environment[name])
	}
	encoded, err := json.Marshal(oci.ConfigFile{OS: "linux", Config: oci.ImageConfig{Env: env, WorkingDir: "/"}})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

type compatEntry struct {
	kind    fvsrepo.EntryKind
	mode    uint32
	link    string
	content []byte
}

// buildFlatpakCompatLayer writes the layer that turns an application and its
// runtime into a root cpak can start: the top-level directories and links a
// Flatpak sandbox has, the linker configuration the launch indexes, the
// directories Flatpak merges out of driver extensions, the font directories
// cpak shares, and the /.flatpak-info toolkits look for before they talk to
// portals. It holds nothing from the package that is not its own metadata.
func (c *Cpak) buildFlatpakCompatLayer(plan *FlatpakPlan, runtime types.Application) (string, error) {
	entries := map[string]compatEntry{}
	dir := func(name string, mode uint32) { entries[name] = compatEntry{kind: fvsrepo.EntryDir, mode: mode} }
	link := func(name, target string) { entries[name] = compatEntry{kind: fvsrepo.EntrySymlink, link: target} }
	file := func(name string, content []byte) {
		entries[name] = compatEntry{kind: fvsrepo.EntryFile, mode: 0o644, content: content}
	}

	for _, name := range []string{"proc", "sys", "dev", "run", "run/host", "var", "home", "root", "mnt", "media", "opt", "srv", "etc"} {
		dir(name, 0o755)
	}
	dir("tmp", 0o1777)
	dir("var/tmp", 0o1777)

	if len(runtime.ParsedLayers) == 0 {
		return "", errors.New("the runtime has no layers")
	}
	runtimeFiles, err := c.flatpakLayerFiles(runtime.ParsedLayers[0])
	if err != nil {
		return "", err
	}
	for _, name := range []string{"bin", "sbin", "lib", "lib32", "lib64"} {
		if _, ok := runtimeFiles["usr/"+name]; ok {
			link(name, "usr/"+name)
		}
	}

	triplet := libraryTriplet(plan.App.Ref.Arch)
	ldPaths := []string{"include /etc/ld.so.conf.d/*.conf", "/app/lib"}
	for _, extension := range plan.AppExtensions {
		if extension.Point.AddLdPath != "" {
			ldPaths = append(ldPaths, path.Join(extension.Directory, extension.Point.AddLdPath))
		}
	}
	for _, extension := range plan.RuntimeExtensions {
		if extension.Point.AddLdPath != "" {
			ldPaths = append(ldPaths, path.Join(extension.Directory, extension.Point.AddLdPath))
		}
	}
	ldPaths = append(ldPaths, "/usr/lib/"+triplet, "/usr/lib")
	file("etc/ld.so.conf", []byte(strings.Join(ldPaths, "\n")+"\n"))

	// The runtimes include this file from their fontconfig, which is where
	// Flatpak lists the host's font directories.
	file("run/host/font-dirs.xml", []byte(flatpakHostFontsConfig))
	file(".flatpak-info", flatpakInfo(plan))

	if err := c.mergeFlatpakExtensionDirs(plan.RuntimeExtensions, runtime, entries); err != nil {
		return "", err
	}

	hash := sha256.New()
	fmt.Fprintf(hash, "cpak.flatpak.compat.v%d\n", integrity.ABIVersion)
	names := sortedKeys(entries)
	for _, name := range names {
		entry := entries[name]
		fmt.Fprintf(hash, "%s\x00%s\x00%o\x00%s\x00%x\n", name, entry.kind, entry.mode, entry.link, sha256.Sum256(entry.content))
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if available, err := c.ensureFVSLayer(digest); err != nil {
		return "", err
	} else if available {
		return digest, c.recordLayerBinding(digest)
	}

	layerDir, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{Message: "flatpak compat " + plan.App.Ref.String(), ComputeSHA256: true})
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(layerDir)
	for _, name := range names {
		entry := entries[name]
		var addErr error
		switch entry.kind {
		case fvsrepo.EntryFile:
			addErr = writer.Add(fvsrepo.Entry{Path: name, Kind: entry.kind, Mode: entry.mode, Size: int64(len(entry.content))}, bytes.NewReader(entry.content))
		default:
			addErr = writer.Add(fvsrepo.Entry{Path: name, Kind: entry.kind, Mode: entry.mode, Link: entry.link}, nil)
		}
		if addErr != nil {
			_ = writer.Abort()
			return "", fmt.Errorf("build the compatibility layer: %w", addErr)
		}
	}
	if _, err := writer.Commit(); err != nil {
		return "", err
	}
	if err := publishFVSLayer(layerDir, c.fvsLayerPath(digest)); err != nil {
		return "", err
	}
	return digest, c.recordLayerBinding(digest)
}

const flatpakHostFontsConfig = `<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<fontconfig>
	<dir>/run/host/fonts</dir>
	<dir>/run/host/local-fonts</dir>
	<dir>/run/host/user-fonts</dir>
</fontconfig>
`

func flatpakInfo(plan *FlatpakPlan) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "[Application]\nname=%s\nruntime=%s\n\n", plan.App.Ref.ID, plan.Runtime.Ref.String())
	fmt.Fprintf(&out, "[Instance]\napp-path=/app\nruntime-path=/usr\nbranch=%s\narch=%s\nflatpak-version=1.16.0\nsession-bus-proxy=true\n", plan.App.Ref.Branch, plan.App.Ref.Arch)
	return []byte(out.String())
}

// mergeFlatpakExtensionDirs presents the merge directories of an extension
// point the way Flatpak does, as one directory of links into every installed
// subdirectory, which is where the loaders inside the runtime look.
func (c *Cpak) mergeFlatpakExtensionDirs(extensions []FlatpakPart, runtime types.Application, entries map[string]compatEntry) error {
	for _, extension := range extensions {
		if len(extension.Point.MergeDirs) == 0 || !extension.Point.Subdirectories {
			continue
		}
		layer := ""
		for _, recorded := range runtime.Flatpak.Extensions {
			if recorded.Ref == extension.Ref.String() {
				layer = recorded.Layer
			}
		}
		if layer == "" {
			continue
		}
		files, err := c.flatpakLayerFiles(layer)
		if err != nil {
			return err
		}
		pointDir := strings.TrimPrefix(path.Join("/usr", extension.Point.Directory), "/")
		mount := strings.TrimPrefix(extension.Directory, "/")
		for _, merge := range extension.Point.MergeDirs {
			source, ok := resolveLayerDirectory(files, mount, merge)
			if !ok {
				continue
			}
			for _, name := range sortedKeys(files) {
				if path.Dir(name) != source {
					continue
				}
				target := path.Join(pointDir, merge, path.Base(name))
				if _, taken := entries[target]; taken {
					continue
				}
				for parent := path.Dir(target); parent != pointDir && parent != "." && parent != "/"; parent = path.Dir(parent) {
					if _, ok := entries[parent]; !ok {
						entries[parent] = compatEntry{kind: fvsrepo.EntryDir, mode: 0o755}
					}
				}
				entries[target] = compatEntry{kind: fvsrepo.EntrySymlink, link: "/" + name}
			}
		}
	}
	return nil
}

// resolveLayerDirectory follows the relative links an extension uses inside
// itself, such as glvnd pointing at share/glvnd, without ever leaving it.
func resolveLayerDirectory(files map[string]fvsrepo.FileEntry, base, relative string) (string, bool) {
	current := base
	for _, component := range strings.Split(relative, "/") {
		next := path.Join(current, component)
		for hops := 0; hops < 8; hops++ {
			entry, ok := files[next]
			if !ok {
				return "", false
			}
			if entry.Link == "" {
				break
			}
			if path.IsAbs(entry.Link) {
				return "", false
			}
			next = path.Join(path.Dir(next), entry.Link)
			if next != base && !strings.HasPrefix(next, base+"/") {
				return "", false
			}
		}
		current = next
	}
	return current, true
}
