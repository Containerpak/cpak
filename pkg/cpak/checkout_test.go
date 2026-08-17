/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storage "github.com/containerpak/storage/pkg/driver"
	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/storaged"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
)

// requireCheckoutResolve refuses to guess: the measurement is built on the
// restricted resolution, so where that is unavailable there is nothing to test
// rather than something that passed.
func requireCheckoutResolve(t *testing.T) {
	t.Helper()
	err := tools.CheckResolveSupport()
	if errors.Is(err, tools.ErrResolveUnsupported) {
		t.Skip("openat2 with a restricted resolution is unavailable here")
	}
	if err != nil {
		t.Fatalf("the restricted resolution could not be checked: %v", err)
	}
}

// requireUnprivileged skips the tests that prove something about permissions,
// which root does not experience.
func requireUnprivileged(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("running as root, where the permission bits refuse nothing")
	}
}

// seedCheckout builds a directory in the place a storage driver would put one
// and fills it with the shapes the measurement has to answer for.
func seedCheckout(t *testing.T, cp *Cpak, layer string) string {
	t.Helper()
	directory := filepath.Join(cp.storageDriverRoot("fvs"), "layers", layer, "rootfs")
	if err := os.MkdirAll(filepath.Join(directory, "usr", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "usr", "share", "value"), []byte("value"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("value", filepath.Join(directory, "usr", "share", "link")); err != nil {
		t.Fatal(err)
	}
	return directory
}

// tamperWithCheckout replaces a file the way anything that owns the directory
// would: a new file renamed over the name. Writing through the old name would
// write through the hard link the driver shares with the block store, which
// damages the layer itself rather than the checkout made from it.
func tamperWithCheckout(t *testing.T, path string) {
	t.Helper()
	replacement := path + ".tampered"
	if err := os.WriteFile(replacement, []byte("a longer value"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}

func measured(t *testing.T, directory string, loose map[string]bool) string {
	t.Helper()
	measurement, err := measureCheckout(directory, loose)
	if err != nil {
		t.Fatalf("the prepared checkout could not be measured: %v", err)
	}
	return measurement
}

func TestMeasureCheckoutAnswersTheSameForAnUnchangedTree(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	if first, second := measured(t, directory, nil), measured(t, directory, nil); first != second {
		t.Fatalf("one unchanged tree measured twice as %q and %q", first, second)
	}
}

func TestMeasureCheckoutFollowsTheMetadataItCovers(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	value := filepath.Join(directory, "usr", "share", "value")
	link := filepath.Join(directory, "usr", "share", "link")

	cases := []struct {
		name   string
		change func(t *testing.T)
	}{
		{"a file that changed size", func(t *testing.T) {
			if err := os.WriteFile(value, []byte("a much longer value"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"a file that changed mode", func(t *testing.T) {
			if err := os.Chmod(value, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link that changed target", func(t *testing.T) {
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("elsewhere", link); err != nil {
				t.Fatal(err)
			}
		}},
		{"an entry that appeared", func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(directory, "usr", "share", "added"), []byte("added"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"a file that became a directory", func(t *testing.T) {
			if err := os.Remove(value); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(value, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := measured(t, directory, nil)
			testCase.change(t)
			if after := measured(t, directory, nil); after == before {
				t.Fatalf("%s left the measurement at %q", testCase.name, after)
			}
		})
	}
}

// The measurement is metadata, and this is what that costs. A change that puts
// back the size and the mode is invisible to it, and the only thing that would
// see it is reading every byte of the checkout, which a launch cannot pay for.
// CheckPreparedCheckoutContents is where that is paid on purpose.
func TestMeasureCheckoutIsBlindToAContentSwapThatKeepsTheMetadata(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	value := filepath.Join(directory, "usr", "share", "value")
	var before unix.Stat_t
	if err := unix.Lstat(value, &before); err != nil {
		t.Fatal(err)
	}
	measurement := measured(t, directory, nil)

	if err := os.WriteFile(value, []byte("VALUE"), 0o644); err != nil {
		t.Fatal(err)
	}
	times := []unix.Timespec{before.Atim, before.Mtim}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, value, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	if after := measured(t, directory, nil); after != measurement {
		t.Fatalf("the measurement moved to %q, so this test no longer documents what it was written for", after)
	}
	content, err := os.ReadFile(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "VALUE" {
		t.Fatalf("the swapped content is %q, so nothing was swapped", content)
	}
}

// The cost budget is a stat walk, so a file the walk cannot open must not stop
// it: reading contents is exactly what it does not do.
func TestMeasureCheckoutNeverReadsFileContents(t *testing.T) {
	requireCheckoutResolve(t)
	requireUnprivileged(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	unreadable := filepath.Join(directory, "usr", "share", "sealed")
	if err := os.WriteFile(unreadable, []byte("sealed"), 0o000); err != nil {
		t.Fatal(err)
	}
	if file, err := os.Open(unreadable); err == nil {
		file.Close()
		t.Skip("this filesystem lets the owner read a file with no permission bits")
	}
	measured(t, directory, nil)
}

func TestMeasureCheckoutRefusesAnEntryItCannotWalk(t *testing.T) {
	requireCheckoutResolve(t)
	requireUnprivileged(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	sealed := filepath.Join(directory, "usr", "sealed")
	if err := os.Mkdir(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
	if _, err := measureCheckout(directory, nil); err == nil {
		t.Fatal("a directory the walk could not enter was measured as if it were empty")
	}
}

func TestRecordCheckoutMeasurementFilesBesideTheLayerBindings(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	if err := cp.recordCheckoutMeasurement("layer", directory); err != nil {
		t.Fatalf("the measurement was refused: %v", err)
	}
	if _, err := os.Stat(cp.GetInStoreDir("bindings", "layer.checkout.json")); err != nil {
		t.Fatalf("the measurement was not filed beside the layer bindings: %v", err)
	}
	if err := cp.recordCheckoutMeasurement("layer", directory); err != nil {
		t.Fatalf("recording the same measurement twice was refused: %v", err)
	}
}

// This is the finding this file exists for: the index is user writable and the
// directory it names is user writable, so a checkout that changed under a
// recorded measurement must not be handed out again.
func TestRecordCheckoutMeasurementRefusesAChangedCheckout(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	if err := cp.recordCheckoutMeasurement("layer", directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "usr", "share", "value"), []byte("a longer value"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cp.recordCheckoutMeasurement("layer", directory); !errors.Is(err, errCheckoutChanged) {
		t.Fatalf("a changed checkout was recorded again: %v", err)
	}
}

// A checkout the driver threw away and built again is a different tree, and
// the record that spoke for the old one has nothing left to compare with.
func TestRecordCheckoutMeasurementAcceptsARebuiltCheckout(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	if err := cp.recordCheckoutMeasurement("layer", directory); err != nil {
		t.Fatal(err)
	}
	rebuilt := filepath.Join(cp.storageDriverRoot("fvs"), "layers", "layer", "rootfs.rebuilt")
	if err := os.MkdirAll(filepath.Join(rebuilt, "usr", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rebuilt, "usr", "share", "value"), []byte("rebuilt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rebuilt, directory); err != nil {
		t.Fatal(err)
	}
	if err := cp.recordCheckoutMeasurement("layer", directory); err != nil {
		t.Fatalf("a rebuilt checkout was refused: %v", err)
	}
}

func TestRecordCheckoutMeasurementRefusesADirectoryOutsideTheDriverRoot(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedCheckout(t, cp, "layer")
	outside := t.TempDir()
	if err := cp.recordCheckoutMeasurement("layer", outside); !errors.Is(err, errCheckoutOutsideDriver) {
		t.Fatalf("a directory outside the storage driver root was measured: %v", err)
	}
	escape := filepath.Join(cp.storageDriverRoot("fvs"), "layers", "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if err := cp.recordCheckoutMeasurement("layer", escape); !errors.Is(err, errCheckoutOutsideDriver) {
		t.Fatalf("a symlink out of the storage driver root was measured: %v", err)
	}
}

func TestVerifyPreparedCheckoutReportsALayerNoStateDescribes(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	found, matches, err := cp.verifyPreparedCheckout("layer", directory)
	if err != nil {
		t.Fatalf("a layer no state describes was an error: %v", err)
	}
	if found || matches {
		t.Fatalf("a layer no state describes answered found=%v matches=%v", found, matches)
	}
}

// A record is not a description. Filing one for a layer nothing binds must not
// turn the unrecorded case into a match, or every legacy installation would
// answer for itself by writing its own answer down.
func TestVerifyPreparedCheckoutRefusesToPassOnARecordAlone(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	directory := seedCheckout(t, cp, "layer")
	if err := cp.recordCheckoutMeasurement("layer", directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cp.GetInStoreDir("bindings", "layer.checkout.json")); err != nil {
		t.Fatalf("the record this test is about was not filed: %v", err)
	}
	found, matches, err := cp.verifyPreparedCheckout("layer", directory)
	if err != nil {
		t.Fatalf("a recorded but undescribed layer was an error: %v", err)
	}
	if found || matches {
		t.Fatalf("a record with no state behind it answered found=%v matches=%v", found, matches)
	}
}

func TestVerifyPreparedCheckoutComparesWithTheBoundState(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, shapedLayer, "usr/share/value", []byte("value"))
	directory := preparedLayerCheckout(t, cp, shapedLayer)

	found, matches, err := cp.verifyPreparedCheckout(shapedLayer, directory)
	if err != nil || !found || !matches {
		t.Fatalf("a checkout the driver had just written answered found=%v matches=%v err=%v", found, matches, err)
	}
	if err = os.Chmod(filepath.Join(directory, "usr", "share", "value"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, matches, err = cp.verifyPreparedCheckout(shapedLayer, directory)
	if err != nil {
		t.Fatalf("a changed checkout was an error rather than a mismatch: %v", err)
	}
	if !found || matches {
		t.Fatalf("a changed checkout answered found=%v matches=%v", found, matches)
	}
}

// A repair throws the checkout away and builds it again from the fvs layer,
// which is the one tree a binding answers for. The record taken from the tree
// that was thrown away must give way to a record of the tree that replaced it,
// or the repair would leave the store unable to describe itself.
func TestRecordedMeasurementFollowsADriverRepair(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedFVSLayerFile(t, cp, "layer", "usr/share/value", []byte("value"))
	paths, err := cp.prepareStorageDriver([]string{"layer"})
	if err != nil {
		t.Fatal(err)
	}
	records, err := cp.checkoutRecords()
	if err != nil {
		t.Fatal(err)
	}
	prepared, found, err := records.lookup("layer")
	if err != nil || !found {
		t.Fatalf("the prepared checkout was not recorded: found=%v err=%v", found, err)
	}

	tamperWithCheckout(t, filepath.Join(paths[0], "usr", "share", "value"))
	repaired, err := handler.Verify(cp.Ctx, storage.VerifyRequest{Layers: []string{"layer"}, Repair: true})
	if err != nil || repaired.Repaired != 1 {
		t.Fatalf("the driver did not repair the checkout: %+v %v", repaired, err)
	}
	rebuilt, err := cp.prepareStorageDriver([]string{"layer"})
	if err != nil {
		t.Fatalf("a repaired checkout was refused: %v", err)
	}
	current, found, err := records.lookup("layer")
	if err != nil || !found {
		t.Fatalf("the repaired checkout was not recorded: found=%v err=%v", found, err)
	}
	if current.Inode == prepared.Inode {
		t.Fatalf("the repair left the same object %d, so this test proves nothing", current.Inode)
	}
	if current.Measurement != measured(t, rebuilt[0], nil) {
		t.Fatal("the record does not hold the measurement of the tree the repair built")
	}
}

// The path this closes: prepareLayerMount reaches the mount through
// preparedLayerDirectories, so a checkout changed after it was prepared has to
// stop there and not on the way back from the overlay.
func TestPreparedLayerDirectoriesRefusesAChangedCheckout(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedFVSLayerFile(t, cp, "layer", "usr/share/value", []byte("value"))
	paths, err := cp.prepareStorageDriver([]string{"layer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cp.preparedLayerDirectories([]string{"layer"}); err != nil {
		t.Fatalf("an untouched checkout was refused: %v", err)
	}
	tamperWithCheckout(t, filepath.Join(paths[0], "usr", "share", "value"))
	if _, err = cp.preparedLayerDirectories([]string{"layer"}); !errors.Is(err, errCheckoutChanged) {
		t.Fatalf("a changed checkout was handed out to be mounted: %v", err)
	}
}

// A launch pays this walk, so the cost is part of the contract and is measured
// here rather than assumed.
func BenchmarkMeasureCheckout(b *testing.B) {
	if err := tools.CheckResolveSupport(); err != nil {
		b.Skipf("the restricted resolution is unavailable here: %v", err)
	}
	cp := &Cpak{Options: types.CpakOptions{StorePath: b.TempDir()}}
	directory := filepath.Join(cp.storageDriverRoot("fvs"), "layers", "layer", "rootfs")
	for group := range 200 {
		nested := filepath.Join(directory, "usr", "share", fmt.Sprintf("group-%03d", group))
		if err := os.MkdirAll(nested, 0o755); err != nil {
			b.Fatal(err)
		}
		for entry := range 100 {
			name := filepath.Join(nested, fmt.Sprintf("entry-%03d", entry))
			if err := os.WriteFile(name, []byte("entry"), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ResetTimer()
	for range b.N {
		if _, err := measureCheckout(directory, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// shapedLayer is the layer the derivation is exercised against. It is a
// separate digest from the ones the other files use, so that a test that
// rebuilds it cannot reach into theirs.
const shapedLayer = "5555555555555555555555555555555555555555555555555555555555555555"

// seedFVSLayerState commits one state into a layer repository, the way a pull
// does, so the entries the derivation reads are entries fvs really wrote.
func seedFVSLayerState(t *testing.T, cp *Cpak, digest string, add func(*fvsrepo.SnapshotWriter) error) {
	t.Helper()
	repository, err := fvsrepo.InitWithOptions(cp.fvsLayerPath(digest), fvsrepo.InitOptions{BlocksPath: cp.fvsBlocksPath()})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := fvsrepo.BeginSnapshot(repository.Path, fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = add(writer); err != nil {
		_ = writer.Abort()
		t.Fatal(err)
	}
	if _, err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
}

// addStateFile is the shorthand the state seeds use, because a file entry has
// to carry its own length and repeating that is where a seed goes wrong.
func addStateFile(writer *fvsrepo.SnapshotWriter, name string, mode uint32, content string) error {
	entry := fvsrepo.Entry{Path: name, Kind: fvsrepo.EntryFile, Mode: mode, Size: int64(len(content))}
	return writer.Add(entry, strings.NewReader(content))
}

// prepareLayerCheckout binds a layer already in the store and puts it through
// the real storage driver, so the directory under test is one the driver wrote
// and not one a test invented.
func prepareLayerCheckout(t *testing.T, cp *Cpak, digest string) (string, error) {
	t.Helper()
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	if err = cp.recordLayerBinding(digest); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
	paths, err := cp.prepareStorageDriver([]string{digest})
	if err != nil {
		return "", err
	}
	return paths[0], nil
}

func preparedLayerCheckout(t *testing.T, cp *Cpak, digest string) string {
	t.Helper()
	directory, err := prepareLayerCheckout(t, cp, digest)
	if err != nil {
		t.Fatalf("the layer could not be prepared: %v", err)
	}
	return directory
}

// swapCheckoutContent replaces what a file holds without touching anything the
// shape covers: same length, same mode, and a rename rather than a write, so
// the block store the checkout is hard linked into is left alone.
func swapCheckoutContent(t *testing.T, name, content string) {
	t.Helper()
	replacement := name + ".swapped"
	if err := os.WriteFile(replacement, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, name); err != nil {
		t.Fatal(err)
	}
}

// The comparison is only worth anything if the two sides are built the same
// way, so this pins that a checkout the driver really wrote digests to what the
// state it was made from says it should.
func TestDeriveCheckoutShapeAnswersWhatTheDriverPrepared(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerState(t, cp, shapedLayer, func(writer *fvsrepo.SnapshotWriter) error {
		if err := writer.Add(fvsrepo.Entry{Path: "usr", Kind: fvsrepo.EntryDir, Mode: 0o755}, nil); err != nil {
			return err
		}
		if err := writer.Add(fvsrepo.Entry{Path: "usr/share", Kind: fvsrepo.EntryDir, Mode: 0o750}, nil); err != nil {
			return err
		}
		if err := addStateFile(writer, "usr/share/value", 0o644, "value"); err != nil {
			return err
		}
		// The driver clears the privileged bits, so a setuid entry is the one
		// place the derivation has to answer with something the state does not
		// literally say.
		if err := addStateFile(writer, "usr/privileged", 0o4755, "privileged"); err != nil {
			return err
		}
		if err := writer.Add(fvsrepo.Entry{Path: "usr/share/link", Kind: fvsrepo.EntrySymlink, Mode: 0o777, Link: "value"}, nil); err != nil {
			return err
		}
		// Parents the layer never declared, which fvs creates on the way to
		// the entry that needs them.
		return addStateFile(writer, "opt/vendor/tool", 0o755, "tool")
	})
	directory := preparedLayerCheckout(t, cp, shapedLayer)

	shape, err := cp.boundCheckoutShape(shapedLayer)
	if err != nil {
		t.Fatalf("the shape of a layer the store holds could not be derived: %v", err)
	}
	if walked := measured(t, directory, shape.loose); walked != shape.digest {
		t.Fatalf("the driver prepared %q where the state describes %q", walked, shape.digest)
	}
	if !shape.loose["."] {
		t.Fatal("the checkout root is created through a umask, so its permission bits are not the state's to claim")
	}
	// fvs carries a directory entry for every parent, including the ones the
	// layer never declared, so everything below the root is comparable and the
	// loose set has to stay at exactly the root.
	if len(shape.loose) != 1 {
		t.Fatalf("the derivation gave up on %v, where only the checkout root is underivable", shape.loose)
	}
	if shape.contents["usr/share/value"] == "" {
		t.Fatal("the state carries no content digest for a file it named, so the deep check would have nothing to read for")
	}
	// The derivation clears the privileged bits because the driver is asked to.
	// If that request ever changes, the digests above stop matching, and this
	// says which of the two moved.
	var privileged unix.Stat_t
	if err = unix.Lstat(filepath.Join(directory, "usr", "privileged"), &privileged); err != nil {
		t.Fatal(err)
	}
	if privileged.Mode&0o7777 != 0o755 {
		t.Fatalf("the driver left mode %04o on a setuid entry, where it is asked to clear the privileged bits", privileged.Mode&0o7777)
	}
}

// The directory entries a state carries come from fvs and not from the layer,
// and a state written in a format that does not carry them at all leaves the
// checkout with directories no entry describes. They are still created, so the
// derivation has to know about them, and their permission bits come from the
// umask of whoever prepared the checkout and not from the state.
func TestExpectedCheckoutEntriesAddsTheDirectoriesAStateDoesNotName(t *testing.T) {
	entries := []fvsrepo.FileEntry{{Path: "opt/vendor/tool", Mode: 0o755, Size: 4}}
	expected, contents, err := expectedCheckoutEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	if _, named := contents["opt/vendor/tool"]; !named {
		t.Fatal("a legacy entry with no kind is a regular file and has to be readable by the deep check")
	}
	for _, name := range []string{"opt", "opt/vendor"} {
		entry, found := expected[name]
		if !found {
			t.Fatalf("the checkout holds %s and the derivation left it out", name)
		}
		if entry.kind != "dir" || entry.fixed {
			t.Fatalf("%s was derived as %+v, where it is a directory whose mode the state does not fix", name, entry)
		}
	}
}

// An overlay whiteout is not the entry the state carries: fvs writes a marker
// under the name the entry deletes, and writes nothing at all for the opaque
// marker. A derivation that took the state literally would disagree with every
// checkout that has one.
func TestDeriveCheckoutShapeAnswersForAnOverlayWhiteout(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerState(t, cp, shapedLayer, func(writer *fvsrepo.SnapshotWriter) error {
		if err := writer.Add(fvsrepo.Entry{Path: "app", Kind: fvsrepo.EntryDir, Mode: 0o755}, nil); err != nil {
			return err
		}
		if err := addStateFile(writer, "app/.wh.removed", 0o644, ""); err != nil {
			return err
		}
		if err := addStateFile(writer, "app/.wh..wh..opq", 0o644, ""); err != nil {
			return err
		}
		return addStateFile(writer, "app/value", 0o644, "value")
	})
	directory, err := prepareLayerCheckout(t, cp, shapedLayer)
	if errors.Is(err, fvsrepo.ErrOverlayWhiteoutsUnsupported) {
		t.Skip("this filesystem does not carry the extended attributes an overlay whiteout needs")
	}
	if err != nil {
		t.Fatalf("a layer holding a whiteout could not be prepared: %v", err)
	}

	shape, err := cp.boundCheckoutShape(shapedLayer)
	if err != nil {
		t.Fatal(err)
	}
	if walked := measured(t, directory, shape.loose); walked != shape.digest {
		t.Fatalf("the driver prepared %q where the state describes %q", walked, shape.digest)
	}
	if _, err = os.Lstat(filepath.Join(directory, "app", "removed")); err != nil {
		t.Fatalf("the whiteout this test is about was not written: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(directory, "app", ".wh.removed")); err == nil {
		t.Fatal("the marker was written under its own name, so this test no longer exercises the rewrite")
	}
}

// This is the finding this round closes. Both sides of the old comparison lived
// in a store the launching account owns: tamper the checkout, measure it again,
// write that answer into the record, and the checkout was recognised.
func TestVerifyPreparedCheckoutRefusesARecordRewrittenToAgree(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, shapedLayer, "usr/share/value", []byte("value"))
	directory := preparedLayerCheckout(t, cp, shapedLayer)
	tamperWithCheckout(t, filepath.Join(directory, "usr", "share", "value"))
	rewriteCheckoutRecord(t, cp, shapedLayer, directory)

	found, matches, err := cp.verifyPreparedCheckout(shapedLayer, directory)
	if err != nil {
		t.Fatalf("a rewritten record was an error rather than a mismatch: %v", err)
	}
	if !found || matches {
		t.Fatalf("a checkout whose record was rewritten to agree with it answered found=%v matches=%v", found, matches)
	}
}

// rewriteCheckoutRecord files the measurement of whatever the directory holds
// now, which is what anything owning the store can do: the record is a file in
// it. The record it writes is the one the old comparison would have accepted.
func rewriteCheckoutRecord(t *testing.T, cp *Cpak, layer, directory string) {
	t.Helper()
	shape, err := cp.boundCheckoutShape(layer)
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := measureCheckout(directory, shape.loose)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err = unix.Stat(resolved, &stat); err != nil {
		t.Fatal(err)
	}
	record := checkoutRecord{
		Format: checkoutRecordFormat, Layer: layer, Directory: resolved,
		Device: uint64(stat.Dev), Inode: stat.Ino, State: shape.state,
		Expected: measurement, Measurement: measurement,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cp.GetInStoreDir("bindings", layer+".checkout.json"), append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The mount path reaches the overlay through preparedLayerDirectories, so a
// checkout that no longer matches the state it was made from has to stop there
// and not on the way back from the mount.
func TestRecordCheckoutMeasurementRefusesACheckoutTheStateDoesNotDescribe(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, shapedLayer, "usr/share/value", []byte("value"))
	directory := preparedLayerCheckout(t, cp, shapedLayer)
	tamperWithCheckout(t, filepath.Join(directory, "usr", "share", "value"))

	if err := cp.recordCheckoutMeasurement(shapedLayer, directory); !errors.Is(err, errCheckoutUnexpected) {
		t.Fatalf("got %v, want a checkout the state does not describe to be refused", err)
	}
	if _, err := cp.preparedLayerDirectories([]string{shapedLayer}); !errors.Is(err, errCheckoutUnexpected) {
		t.Fatalf("got %v, want the mount path to refuse it as well", err)
	}
}

// What the comparison cannot see, said out loud. Every case here is a change
// the shape of a checkout does not carry, and the test exists so that a later
// reader knows it was a decision and not an oversight.
func TestVerifyPreparedCheckoutIsBlindToWhatNoStateRecords(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerState(t, cp, shapedLayer, func(writer *fvsrepo.SnapshotWriter) error {
		if err := writer.Add(fvsrepo.Entry{Path: "usr", Kind: fvsrepo.EntryDir, Mode: 0o755}, nil); err != nil {
			return err
		}
		if err := addStateFile(writer, "usr/value", 0o644, "value"); err != nil {
			return err
		}
		return addStateFile(writer, "opt/vendor/tool", 0o755, "tool")
	})
	directory := preparedLayerCheckout(t, cp, shapedLayer)

	cases := []struct {
		name   string
		change func(t *testing.T)
	}{
		{"content swapped for content of the same length", func(t *testing.T) {
			swapCheckoutContent(t, filepath.Join(directory, "usr", "value"), "VALUE")
		}},
		{"a modification time moved", func(t *testing.T) {
			future := []unix.Timespec{{Sec: 1 << 30}, {Sec: 1 << 30}}
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, filepath.Join(directory, "usr", "value"), future, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				t.Fatal(err)
			}
		}},
		{"the permission bits of the checkout root itself", func(t *testing.T) {
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"an extended attribute added to a file", func(t *testing.T) {
			name := filepath.Join(directory, "usr", "value")
			if err := unix.Setxattr(name, "user.cpak.test", []byte("y"), 0); err != nil {
				t.Skipf("this filesystem does not carry user extended attributes: %v", err)
			}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.change(t)
			found, matches, err := cp.verifyPreparedCheckout(shapedLayer, directory)
			if err != nil {
				t.Fatal(err)
			}
			if !found || !matches {
				t.Fatalf("%s moved the comparison, so this test no longer documents what it was written for: found=%v matches=%v",
					testCase.name, found, matches)
			}
		})
	}
	// The permission bits the state does name are compared, or the case above
	// would prove nothing about the ones it does not.
	if err := os.Chmod(filepath.Join(directory, "usr"), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, matches, err := cp.verifyPreparedCheckout(shapedLayer, directory); err != nil || matches {
		t.Fatalf("a directory the state names was chmodded and still matched: matches=%v err=%v", matches, err)
	}
}

// The deep check is the answer the launch path deliberately does not buy, so
// this is where the content swap the shape misses is actually caught.
func TestCheckPreparedCheckoutContentsSeesTheSwapTheShapeMisses(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerState(t, cp, shapedLayer, func(writer *fvsrepo.SnapshotWriter) error {
		if err := writer.Add(fvsrepo.Entry{Path: "usr", Kind: fvsrepo.EntryDir, Mode: 0o755}, nil); err != nil {
			return err
		}
		return addStateFile(writer, "usr/value", 0o644, "value")
	})
	directory := preparedLayerCheckout(t, cp, shapedLayer)

	check, err := cp.CheckPreparedCheckoutContents(shapedLayer)
	if err != nil {
		t.Fatalf("an untouched checkout could not be read: %v", err)
	}
	if !check.Sound() || check.Files != 1 || check.Bytes != int64(len("value")) {
		t.Fatalf("an untouched checkout read as %+v", check)
	}

	swapCheckoutContent(t, filepath.Join(directory, "usr", "value"), "VALUE")
	if _, matches, verifyErr := cp.verifyPreparedCheckout(shapedLayer, directory); verifyErr != nil || !matches {
		t.Fatalf("the swap moved the shape, so this test no longer shows what the deep check adds: matches=%v err=%v", matches, verifyErr)
	}
	check, err = cp.CheckPreparedCheckoutContents(shapedLayer)
	if err != nil {
		t.Fatalf("a swapped checkout could not be read: %v", err)
	}
	if check.Sound() || len(check.Changed) != 1 || check.Changed[0] != "usr/value" {
		t.Fatalf("the deep check missed the swap: %+v", check)
	}
}

// A file the deep check could not open is not a file that agreed with the
// state, and saying so is the difference between reading a tree and claiming
// to have read it.
func TestCheckPreparedCheckoutContentsReportsAFileItCouldNotRead(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, shapedLayer, "usr/value", []byte("value"))
	directory := preparedLayerCheckout(t, cp, shapedLayer)
	if err := os.Remove(filepath.Join(directory, "usr", "value")); err != nil {
		t.Fatal(err)
	}

	check, err := cp.CheckPreparedCheckoutContents(shapedLayer)
	if err != nil {
		t.Fatalf("a checkout with a file removed could not be read: %v", err)
	}
	if check.Sound() || len(check.Missing) != 1 || check.Missing[0] != "usr/value" {
		t.Fatalf("the deep check did not report the file that is gone: %+v", check)
	}
}

// A walk reaches "a/c" before "a.b" and a plain string sort does not, so the
// two sides agree only if the derivation orders by path component the way the
// walk does. Three names are the smallest thing that tells the orders apart.
func TestDeriveCheckoutShapeOrdersTheWayTheWalkDoes(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerState(t, cp, shapedLayer, func(writer *fvsrepo.SnapshotWriter) error {
		if err := writer.Add(fvsrepo.Entry{Path: "a", Kind: fvsrepo.EntryDir, Mode: 0o755}, nil); err != nil {
			return err
		}
		if err := addStateFile(writer, "a/c", 0o644, "inside"); err != nil {
			return err
		}
		return addStateFile(writer, "a.b", 0o644, "beside")
	})
	directory := preparedLayerCheckout(t, cp, shapedLayer)

	shape, err := cp.boundCheckoutShape(shapedLayer)
	if err != nil {
		t.Fatal(err)
	}
	if walked := measured(t, directory, shape.loose); walked != shape.digest {
		t.Fatalf("the driver prepared %q where the state describes %q", walked, shape.digest)
	}
	if beforeCheckoutPath("a.b", "a/c") {
		t.Fatal("a.b was ordered before a/c, which is the string order and not the order a walk takes")
	}
	if !beforeCheckoutPath("a", "a/c") || !beforeCheckoutPath("a", "a.b") {
		t.Fatal("a directory has to be ordered before what is inside it and before its neighbours")
	}
}

// The corners of the whiteout rewrite, taken directly rather than through a
// driver, because a state that reaches them is one no cpak pull writes and a
// derivation that got them wrong would refuse a launch it should have allowed.
func TestExpectedCheckoutEntriesFollowsTheWhiteoutRewrite(t *testing.T) {
	entries := []fvsrepo.FileEntry{
		{Path: "app/.wh.removed", Mode: 0o644},
		{Path: "app/.wh..wh..opq", Mode: 0o644},
		{Path: "app/.wh.kept", Kind: string(fvsrepo.EntryDir), Mode: 0o755},
		{Path: "lone/.wh..wh..opq", Mode: 0o644},
	}
	expected, contents, err := expectedCheckoutEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	marker, found := expected["app/removed"]
	if !found || marker.kind != "file" || !marker.fixed || marker.mode != checkoutWhiteoutMode {
		t.Fatalf("the marker for a deleted entry was derived as %+v, found=%v", marker, found)
	}
	if _, named := contents["app/removed"]; named {
		t.Fatal("a whiteout marker carries no content the state can answer for, so the deep check must not read it")
	}
	for _, name := range []string{"app/.wh.removed", "app/.wh..wh..opq", "lone/.wh..wh..opq"} {
		if _, found = expected[name]; found {
			t.Fatalf("%s was derived under its own name, where the checkout holds no such entry", name)
		}
	}
	// A directory keeps its own name whatever it is called, and a directory
	// that exists only to carry an opaque marker still exists.
	if entry, found := expected["app/.wh.kept"]; !found || entry.kind != "dir" || !entry.fixed {
		t.Fatalf("a directory named like a marker was derived as %+v, found=%v", entry, found)
	}
	if entry, found := expected["lone"]; !found || entry.kind != "dir" || entry.fixed {
		t.Fatalf("the directory an opaque marker sits in was derived as %+v, found=%v", entry, found)
	}
}

// Losing the state must not lose the tripwire. An attacker who removes the
// repository takes the derivation with it, and what is left is the record the
// store filed for the object it prepared, so a checkout changed under that
// record still has to stop before the mount.
func TestRecordCheckoutMeasurementKeepsTheTripwireWhenTheStateIsGone(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, shapedLayer, "usr/share/value", []byte("value"))
	directory := preparedLayerCheckout(t, cp, shapedLayer)
	if err := os.RemoveAll(cp.fvsLayerPath(shapedLayer)); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.boundCheckoutShape(shapedLayer); !errors.Is(err, errCheckoutUnderivable) {
		t.Fatalf("got %v, want the shape to be underivable once the repository is gone", err)
	}

	if err := cp.recordCheckoutMeasurement(shapedLayer, directory); err != nil {
		t.Fatalf("an untouched checkout was refused once its state was gone: %v", err)
	}
	tamperWithCheckout(t, filepath.Join(directory, "usr", "share", "value"))
	if err := cp.recordCheckoutMeasurement(shapedLayer, directory); !errors.Is(err, errCheckoutChanged) {
		t.Fatalf("got %v, want the record alone to refuse a checkout changed under it", err)
	}
}
