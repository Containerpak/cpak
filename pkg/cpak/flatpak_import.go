/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/ostree"
	"github.com/schollz/progressbar/v3"
)

// flatpakPlacement says where a path of a commit lands in a layer, or that it
// is left out. Flatpak keeps an application under files/ and mounts it at
// /app, and a runtime under files/ mounted at /usr; cpak composes layers onto
// one root instead, so the paths are moved there as they are imported.
type flatpakPlacement struct {
	// id names the placement inside the layer identity: the same commit placed
	// two ways is two layers.
	id    string
	place func(name string) (string, bool)
}

func flatpakApplicationPlacement() flatpakPlacement {
	return flatpakPlacement{id: "app", place: func(name string) (string, bool) {
		rest, ok := filesRelative(name)
		if !ok {
			return "", false
		}
		return path.Join("app", rest), true
	}}
}

// flatpakRuntimePlacement puts a runtime under /usr, except for its etc,
// which Flatpak serves as /etc and cpak's own configuration files are
// written into.
func flatpakRuntimePlacement() flatpakPlacement {
	return flatpakPlacement{id: "runtime", place: func(name string) (string, bool) {
		rest, ok := filesRelative(name)
		if !ok {
			return "", false
		}
		if rest == "etc" || strings.HasPrefix(rest, "etc/") {
			return rest, true
		}
		return path.Join("usr", rest), true
	}}
}

// flatpakExtensionPlacement puts an extension at its mount point, optionally
// keeping only some top-level directories of it, which is how the locale
// extensions are reduced to the languages the user reads.
func flatpakExtensionPlacement(mountPoint string, keep []string) flatpakPlacement {
	id := "extension:" + mountPoint
	if len(keep) > 0 {
		id += ":" + strings.Join(keep, ",")
	}
	return flatpakPlacement{id: id, place: func(name string) (string, bool) {
		rest, ok := filesRelative(name)
		if !ok {
			return "", false
		}
		if len(keep) > 0 && rest != "." {
			top, _, _ := strings.Cut(rest, "/")
			kept := false
			for _, value := range keep {
				kept = kept || top == value
			}
			if !kept {
				return "", false
			}
		}
		return path.Join(mountPoint, rest), true
	}}
}

func filesRelative(name string) (string, bool) {
	if name == "files" {
		return ".", true
	}
	rest, ok := strings.CutPrefix(name, "files/")
	return rest, ok
}

// flatpakLayerDigest is what an imported commit is stored and bound under.
// It is derived from the signed commit and the placement, never from the
// content as fetched, so asking for the same import twice finds the first one.
func flatpakLayerDigest(ref string, commit ostree.Checksum, placement flatpakPlacement) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "cpak.flatpak.layer.v%d\n%s\n%s\n%s\n", integrity.ABIVersion, ref, commit, placement.id)
	return hex.EncodeToString(hash.Sum(nil))
}

// flatpakObjectIndexPath is where the import of a layer records which OSTree
// object each of its files was, so that the next import of a commit sharing
// them can take them from the store instead of downloading them again.
func (c *Cpak) flatpakObjectIndexPath(digest string) string {
	return c.GetInStoreDir("flatpak", "objects", digest+".json")
}

type flatpakReusable struct {
	layer string
	path  string
}

// flatpakReusableObjects reads every object index the store holds whose layer
// is still there.
func (c *Cpak) flatpakReusableObjects() map[ostree.Checksum]flatpakReusable {
	reusable := map[ostree.Checksum]flatpakReusable{}
	directory := c.GetInStoreDir("flatpak", "objects")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return reusable
	}
	for _, entry := range entries {
		digest, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok {
			continue
		}
		if available, err := c.fvsLayerAvailable(digest); err != nil || !available {
			if err == nil {
				os.Remove(filepath.Join(directory, entry.Name()))
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		var index map[string]string
		if json.Unmarshal(data, &index) != nil {
			continue
		}
		for checksum, name := range index {
			sum, err := ostree.ParseChecksum(checksum)
			if err == nil {
				reusable[sum] = flatpakReusable{layer: digest, path: name}
			}
		}
	}
	return reusable
}

type fetchedObject struct {
	checksum ostree.Checksum
	spool    string
	size     int64
	err      error
}

// importFlatpakCommit imports a signed commit into the layer store and answers
// with the layer digest. A layer already in the store is not imported again.
//
// Every file is read through the checksum it is addressed by and the snapshot
// is committed only after the last one verified, so a repository that serves
// a forged object leaves nothing behind but unreferenced blocks for the
// garbage collector.
func (c *Cpak) importFlatpakCommit(repository *ostree.Repository, ref string, commit ostree.Commit, placement flatpakPlacement) (string, error) {
	digest := flatpakLayerDigest(ref, commit.Checksum, placement)
	if available, err := c.ensureFVSLayer(digest); err != nil {
		return "", err
	} else if available {
		logger.Printf("Layer for %s already present in the store, skipping..", ref)
		return digest, nil
	}

	entries, err := repository.Tree(c.Ctx, commit)
	if err != nil {
		return "", err
	}

	layerDir, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{
		Message:       "flatpak " + ref + " " + commit.Checksum.String(),
		ComputeSHA256: true,
	})
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = writer.Abort()
		}
		_ = os.RemoveAll(layerDir)
	}()

	// Files are grouped by object: Flatpak deduplicates identical files into
	// one object, and an object listed at several paths is fetched once and
	// linked to the others.
	paths := map[ostree.Checksum][]string{}
	order := []ostree.Checksum{}
	for _, entry := range entries {
		target, ok := placement.place(entry.Path)
		if !ok {
			continue
		}
		if entry.Kind == ostree.EntryDir {
			if err := writer.Add(fvsrepo.Entry{Path: target, Kind: fvsrepo.EntryDir, Mode: entry.Mode & 0o1777}, nil); err != nil {
				return "", fmt.Errorf("import %s: %w", ref, err)
			}
			continue
		}
		if _, seen := paths[entry.Checksum]; !seen {
			order = append(order, entry.Checksum)
		}
		paths[entry.Checksum] = append(paths[entry.Checksum], target)
	}

	index := map[string]string{}
	reusable := c.flatpakReusableObjects()
	states := map[string]map[string]fvsrepo.FileEntry{}
	pending := order[:0:0]
	for _, checksum := range order {
		if reused, err := c.reuseFlatpakObject(writer, reusable, states, checksum, paths[checksum]); err != nil {
			return "", fmt.Errorf("import %s: %w", ref, err)
		} else if reused {
			index[checksum.String()] = paths[checksum][0]
			continue
		}
		pending = append(pending, checksum)
	}
	if reusedCount := len(order) - len(pending); reusedCount > 0 {
		logger.Printf("Reusing %d files of %s already in the store", reusedCount, ref)
	}

	spoolDir, err := c.GetInStoreDirMkdir("flatpak", "spool")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(c.Ctx)
	defer cancel()
	results := c.fetchFlatpakObjects(ctx, repository, spoolDir, pending)
	bar := progressbar.NewOptions(len(pending),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSetDescription("Downloading "+ref),
		progressbar.OptionShowCount(),
		progressbar.OptionFullWidth(),
		progressbar.OptionOnCompletion(func() { fmt.Fprint(os.Stderr, "\n") }),
	)
	var importErr error
	for result := range results {
		if importErr != nil {
			if result.spool != "" {
				os.Remove(result.spool)
			}
			continue
		}
		if result.err != nil {
			importErr = result.err
			cancel()
			continue
		}
		err := addFlatpakObject(writer, result, paths[result.checksum])
		os.Remove(result.spool)
		if err != nil {
			importErr = fmt.Errorf("import %s: %w", ref, err)
			cancel()
			continue
		}
		index[result.checksum.String()] = paths[result.checksum][0]
		_ = bar.Add(1)
	}
	if importErr != nil {
		return "", importErr
	}
	if _, err := writer.Commit(); err != nil {
		return "", err
	}
	committed = true
	if err := publishFVSLayer(layerDir, c.fvsLayerPath(digest)); err != nil {
		return "", err
	}
	if err := c.recordLayerBinding(digest); err != nil {
		return "", err
	}
	if err := c.writeFlatpakObjectIndex(digest, index); err != nil {
		logger.Printf("Warning: could not record the objects of %s for reuse: %v", ref, err)
	}
	return digest, nil
}

func (c *Cpak) writeFlatpakObjectIndex(digest string, index map[string]string) error {
	file := c.flatpakObjectIndexPath(digest)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	temporary := file + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, file)
}

// reuseFlatpakObject places an object the store already holds. The store
// holds it because an earlier import read it through the same checksum, and
// the reference is checked block by block by the writer, so a stale index can
// only make an import download what it could have reused.
func (c *Cpak) reuseFlatpakObject(writer *fvsrepo.SnapshotWriter, reusable map[ostree.Checksum]flatpakReusable, states map[string]map[string]fvsrepo.FileEntry, checksum ostree.Checksum, targets []string) (bool, error) {
	source, ok := reusable[checksum]
	if !ok {
		return false, nil
	}
	files, ok := states[source.layer]
	if !ok {
		files = map[string]fvsrepo.FileEntry{}
		repository := c.fvsLayerPath(source.layer)
		if list, err := fvsrepo.States(repository); err == nil && len(list) > 0 {
			if entries, err := fvsrepo.StateFiles(repository, list[0].ID); err == nil {
				for _, entry := range entries {
					files[entry.Path] = entry
				}
			}
		}
		states[source.layer] = files
	}
	entry, ok := files[source.path]
	if !ok {
		return false, nil
	}
	for index, target := range targets {
		var err error
		switch {
		case entry.Link != "" || entry.Kind == string(fvsrepo.EntrySymlink):
			err = writer.Add(fvsrepo.Entry{Path: target, Kind: fvsrepo.EntrySymlink, Link: entry.Link}, nil)
		case index > 0:
			err = writer.Add(fvsrepo.Entry{Path: target, Kind: fvsrepo.EntryHardlink, Link: targets[0], Mode: entry.Mode}, nil)
		default:
			reference := entry
			reference.Path = target
			err = writer.AddReference(reference)
		}
		if err != nil {
			if index == 0 {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func (c *Cpak) fetchFlatpakObjects(ctx context.Context, repository *ostree.Repository, spoolDir string, checksums []ostree.Checksum) <-chan fetchedObject {
	jobs := make(chan ostree.Checksum)
	results := make(chan fetchedObject)
	workers := min(32, max(1, len(checksums)))
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for checksum := range jobs {
				result := fetchFlatpakObject(ctx, repository, spoolDir, checksum)
				select {
				case results <- result:
				case <-ctx.Done():
					if result.spool != "" {
						os.Remove(result.spool)
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, checksum := range checksums {
			select {
			case jobs <- checksum:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		group.Wait()
		close(results)
	}()
	return results
}

func fetchFlatpakObject(ctx context.Context, repository *ostree.Repository, spoolDir string, checksum ostree.Checksum) fetchedObject {
	file, err := os.CreateTemp(spoolDir, checksum.String()+"-*.filez")
	if err != nil {
		return fetchedObject{checksum: checksum, err: err}
	}
	size, err := repository.FetchFileObject(ctx, checksum, file)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(file.Name())
		return fetchedObject{checksum: checksum, err: err}
	}
	return fetchedObject{checksum: checksum, spool: file.Name(), size: size}
}

func addFlatpakObject(writer *fvsrepo.SnapshotWriter, object fetchedObject, targets []string) error {
	file, err := os.Open(object.spool)
	if err != nil {
		return err
	}
	defer file.Close()
	header, content, err := ostree.OpenFileObject(object.checksum, file, object.size)
	if err != nil {
		return err
	}
	for index, target := range targets {
		switch {
		case header.IsSymlink():
			err = writer.Add(fvsrepo.Entry{Path: target, Kind: fvsrepo.EntrySymlink, Link: header.Target}, nil)
		case index > 0:
			// A link takes its mode from the entry, not from the file it
			// links to, so it is given the object's mode as well.
			err = writer.Add(fvsrepo.Entry{Path: target, Kind: fvsrepo.EntryHardlink, Link: targets[0], Mode: header.Mode & 0o1777}, nil)
		default:
			// Set-id bits never survive the import: nothing in a Flatpak
			// application has a reason to change identity, and Flatpak
			// itself strips them on deploy.
			err = writer.Add(fvsrepo.Entry{
				Path: target,
				Kind: fvsrepo.EntryFile,
				Mode: header.Mode & 0o1777,
				Size: int64(header.Size),
			}, content)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// flatpakLayerFiles answers with what an imported layer holds, keyed by path.
func (c *Cpak) flatpakLayerFiles(digest string) (map[string]fvsrepo.FileEntry, error) {
	repository := c.fvsLayerPath(digest)
	states, err := fvsrepo.States(repository)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, errors.New("layer holds no state")
	}
	entries, err := fvsrepo.StateFiles(repository, states[0].ID)
	if err != nil {
		return nil, err
	}
	files := make(map[string]fvsrepo.FileEntry, len(entries))
	for _, entry := range entries {
		files[entry.Path] = entry
	}
	return files, nil
}

// sortedKeys keeps generated content in one order whatever the map.
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
