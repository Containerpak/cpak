/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	storage "github.com/containerpak/storage/pkg/driver"
	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"golang.org/x/sys/unix"
)

// A prepared checkout is the tree a launch actually mounts, and it is built by
// the storage driver out of exactly one fvs state. That state is named by the
// layer binding, the binding is inside the package root, and the package root
// is what a root owned anchor is taken over. So what a checkout should hold is
// not a matter of opinion and does not have to be read back from a file: it is
// derived from the state, and the walk of the directory is compared with that.
//
// The record filed beside the bindings is not that comparison. It lives in a
// store the launching account owns, so on its own it says only what the store
// once claimed about itself. It is written, it is refused when it disagrees
// with the state, and it never decides that a checkout is the right one.
//
// What the derivation cannot state is on deriveCheckoutShape, and what no
// amount of metadata can see is on measureCheckoutTree.

// checkoutRecordLimit bounds what is read back. A record is one path and a few
// short fields, so anything larger than this was not written by us.
const checkoutRecordLimit = 8192

// checkoutRecordFormat versions the record and the digest inside it together.
// A record written in another format is answering a different question, so it
// is replaced rather than compared, and an installation that predates this
// does not start refusing its own launches.
const checkoutRecordFormat = 2

// The names overlayfs gives a deleted entry and an opaque directory, which fvs
// rewrites while it materializes a checkout.
const (
	checkoutWhiteoutPrefix = ".wh."
	checkoutOpaqueMarker   = ".wh..wh..opq"
)

// checkoutWhiteoutMode is the mode fvs creates an overlay whiteout marker with.
const checkoutWhiteoutMode = 0o600

var (
	errCheckoutOutsideDriver = errors.New("the prepared checkout is not under the storage driver root")
	errCheckoutChanged       = errors.New("the prepared checkout no longer holds what was measured for it")
	errCheckoutUnexpected    = errors.New("the prepared checkout is not the shape the state it was made from describes")
	errCheckoutUnderivable   = errors.New("no state the store holds describes the prepared checkout of the layer")
)

// checkoutEntry is one entry of a prepared checkout, reduced to what the state
// and a walk of the directory can both state about it.
//
// Three things are deliberately absent. Ownership and extended attributes,
// because the state carries neither. The modification time, because the state
// holds zero for it while the checkout holds the moment it was prepared, so
// comparing it would compare a value against nothing; it is attacker settable
// anyway and dropping it costs the measurement nothing it had.
type checkoutEntry struct {
	path string
	kind string
	mode uint32

	// fixed says whether the state decides the permission bits of this entry.
	// It does not for the checkout root, for a directory only implied by the
	// path of another entry, or for a fifo: all three are created through the
	// umask of whoever prepared the checkout, which no state records.
	fixed bool

	size int64
	link string
}

// checkoutShape is what a checkout of one state must look like on disk. The
// digest is comparable with a walk of the directory, loose names the paths
// whose permission bits the state does not decide, and contents carries the
// per file digest the deep check reads for.
type checkoutShape struct {
	state    string
	digest   string
	loose    map[string]bool
	contents map[string]string
}

// derived reports whether anything was derived at all, so that the zero shape
// is never mistaken for a shape that matched.
func (s checkoutShape) derived() bool {
	return s.digest != ""
}

// deriveCheckoutShape turns the entry list of a state into the shape a checkout
// of it has on disk. It reproduces what cpak asks the storage driver for, which
// is a checkout with the privileged bits cleared and whiteouts rewritten as
// overlay markers, so what it answers is what the driver would have written and
// never a reading of what it did write.
//
// What it cannot state, and says through loose instead of guessing: the
// permission bits of the checkout root, of a fifo, and of a directory no entry
// names. All three are created through the umask of whoever prepared the
// checkout. The last one is empty in practice, because fvs carries a directory
// entry for every parent, and it is here for the states that do not.
func deriveCheckoutShape(repository, state string) (checkoutShape, error) {
	entries, err := fvsrepo.StateFiles(repository, state)
	// A state the store no longer holds is the same answer as a repository it
	// never held: nothing describes the checkout. Which of the two it is, and
	// whether that is a disagreement, is measureLaunchStates to say.
	if layerNotStored(err) || errors.Is(err, fvsrepo.ErrStateNotFound) {
		return checkoutShape{}, fmt.Errorf("%w: state %s", errCheckoutUnderivable, state)
	}
	if err != nil {
		return checkoutShape{}, fmt.Errorf("read layer state %s: %w", state, err)
	}
	expected, contents, err := expectedCheckoutEntries(entries)
	if err != nil {
		return checkoutShape{}, err
	}
	shape := checkoutShape{
		state:    state,
		loose:    map[string]bool{".": true},
		contents: contents,
	}
	ordered := make([]string, 0, len(expected))
	for name, entry := range expected {
		ordered = append(ordered, name)
		if !entry.fixed {
			shape.loose[name] = true
		}
	}
	sort.Slice(ordered, func(first, second int) bool {
		return beforeCheckoutPath(ordered[first], ordered[second])
	})
	digest := newCheckoutDigest()
	writeCheckoutEntry(digest, checkoutEntry{path: ".", kind: "dir"})
	for _, name := range ordered {
		writeCheckoutEntry(digest, expected[name])
	}
	shape.digest = hex.EncodeToString(digest.Sum(nil))
	return shape, nil
}

// expectedCheckoutEntries applies the state in the three passes the
// materializer applies it in, because the passes decide what wins when two
// entries land on the same name: a whiteout marker is written first, a
// directory replaces it, and a file replaces either.
func expectedCheckoutEntries(entries []fvsrepo.FileEntry) (map[string]checkoutEntry, map[string]string, error) {
	expected := make(map[string]checkoutEntry, len(entries))
	for _, entry := range entries {
		target, whiteout := checkoutWhiteoutTarget(entry.Path)
		if !whiteout {
			continue
		}
		expected[target] = checkoutEntry{path: target, kind: "file", mode: checkoutWhiteoutMode, fixed: true}
	}
	// A directory entry is materialized under its own name whatever it is
	// called, including the names that read as whiteout markers, so this pass
	// skips nothing. The pass over the rest is the one that skips them.
	for _, entry := range entries {
		if entry.Kind != string(fvsrepo.EntryDir) {
			continue
		}
		expected[entry.Path] = checkoutEntry{path: entry.Path, kind: "dir", mode: checkoutMode(entry.Mode), fixed: true}
	}
	contents := make(map[string]string)
	for _, entry := range entries {
		if entry.Kind == string(fvsrepo.EntryDir) || isCheckoutWhiteout(entry.Path) {
			continue
		}
		derived, err := deriveCheckoutEntry(entry)
		if err != nil {
			return nil, nil, err
		}
		expected[entry.Path] = derived
		if derived.kind == "file" {
			contents[entry.Path] = entry.ContentDigest
		}
	}
	addOpaqueCheckoutDirectories(expected, entries)
	addImplicitCheckoutDirectories(expected)
	return expected, contents, nil
}

// addOpaqueCheckoutDirectories keeps the directories that exist only because an
// opaque marker was written into them. The marker itself leaves no entry, so a
// state that carries no directory entry of its own would otherwise describe a
// checkout without a directory the driver did create.
func addOpaqueCheckoutDirectories(expected map[string]checkoutEntry, entries []fvsrepo.FileEntry) {
	for _, entry := range entries {
		if path.Base(entry.Path) != checkoutOpaqueMarker {
			continue
		}
		parent := path.Dir(entry.Path)
		if parent == "." || parent == "/" {
			continue
		}
		if existing, found := expected[parent]; found && existing.kind == "dir" {
			continue
		}
		expected[parent] = checkoutEntry{path: parent, kind: "dir"}
	}
}

// deriveCheckoutEntry answers with the entry a single state record becomes on
// disk. A kind no checkout can materialize is refused rather than left out,
// because a shape missing an entry is the shape of a tree that does not exist.
func deriveCheckoutEntry(entry fvsrepo.FileEntry) (checkoutEntry, error) {
	derived := checkoutEntry{path: entry.Path, mode: checkoutMode(entry.Mode), fixed: true}
	switch fvsrepo.EntryKind(entry.Kind) {
	case "", fvsrepo.EntryFile:
		derived.kind, derived.size = "file", entry.Size
	case fvsrepo.EntrySymlink:
		// A symlink carries the permission bits Linux gives every symlink, and
		// its size is the length of the target it was created with.
		derived.kind, derived.mode = "symlink", 0o777
		derived.size, derived.link = int64(len(entry.Link)), entry.Link
	case fvsrepo.EntryFIFO:
		derived.kind, derived.fixed = "fifo", false
	default:
		return checkoutEntry{}, fmt.Errorf("the state describes %s as a %q, which no checkout of it can hold", entry.Path, entry.Kind)
	}
	return derived, nil
}

// addImplicitCheckoutDirectories adds the directories a checkout has because
// something inside them was written and that the state never named. They are
// created on the way to the entry that needs them and their mode is never set
// afterwards, so the state does not decide their permission bits.
func addImplicitCheckoutDirectories(expected map[string]checkoutEntry) {
	named := make([]string, 0, len(expected))
	for name := range expected {
		named = append(named, name)
	}
	for _, name := range named {
		for parent := path.Dir(name); parent != "." && parent != "/"; parent = path.Dir(parent) {
			if existing, found := expected[parent]; found && existing.kind == "dir" {
				break
			}
			expected[parent] = checkoutEntry{path: parent, kind: "dir"}
		}
	}
}

// checkoutMode is the permission bits fvs gives an entry when the storage
// driver asks for the privileged bits to be cleared, which is what every cpak
// checkout is prepared with.
func checkoutMode(mode uint32) uint32 {
	return mode & 0o7777 &^ 0o6000
}

func isCheckoutWhiteout(name string) bool {
	return strings.HasPrefix(path.Base(name), checkoutWhiteoutPrefix)
}

// checkoutWhiteoutTarget is the name an overlay whiteout marker takes on disk.
// The opaque marker takes none: it is an extended attribute on the directory
// that holds it and leaves no entry behind, which is one of the reasons an
// opaque directory is invisible to this measurement.
func checkoutWhiteoutTarget(name string) (string, bool) {
	base := path.Base(name)
	if !strings.HasPrefix(base, checkoutWhiteoutPrefix) || base == checkoutOpaqueMarker {
		return "", false
	}
	deleted := strings.TrimPrefix(base, checkoutWhiteoutPrefix)
	if deleted == "" {
		return "", false
	}
	parent := path.Dir(name)
	if parent == "." {
		return deleted, true
	}
	return parent + "/" + deleted, true
}

// beforeCheckoutPath reports whether one path is reached before another by a
// walk that sorts the names of every directory it enters. It compares by
// component and never as a string, because a walk reaches "a/c" before "a.b"
// while "a.b" is the smaller string.
func beforeCheckoutPath(first, second string) bool {
	for {
		firstHead, firstRest, firstDeeper := strings.Cut(first, "/")
		secondHead, secondRest, secondDeeper := strings.Cut(second, "/")
		if firstHead != secondHead {
			return firstHead < secondHead
		}
		if !firstDeeper || !secondDeeper {
			return !firstDeeper && secondDeeper
		}
		first, second = firstRest, secondRest
	}
}

// boundCheckoutShape derives the shape the checkout of a layer must have from
// the state its binding names, which is the value an anchor covers. A layer
// with no binding, or one whose bound state the store no longer holds, has no
// derivable shape and answers errCheckoutUnderivable: a caller has to read that
// as nothing answering for the checkout, and never as a checkout that matched.
func (c *Cpak) boundCheckoutShape(layer string) (checkoutShape, error) {
	if !bindableLayerDigest(layer) {
		return checkoutShape{}, fmt.Errorf("%w: %s is not a layer a pull can bind", errCheckoutUnderivable, layer)
	}
	bindings, err := c.layerBindings()
	if err != nil {
		return checkoutShape{}, err
	}
	binding, found, err := bindings.Lookup(layer)
	if err != nil {
		return checkoutShape{}, fmt.Errorf("read the binding of layer %s: %w", layer, err)
	}
	if !found {
		return checkoutShape{}, fmt.Errorf("%w: layer %s is not bound", errCheckoutUnderivable, layer)
	}
	return deriveCheckoutShape(c.fvsLayerPath(layer), binding.StateID)
}

// bindableLayerDigest reports whether a layer identifier is one the binding
// ledger can file at all. A name that is not a sha256 reference is not a layer
// any pull ever bound, so it has no derivable shape, and asking the ledger
// about it would turn that into a failure instead of the unrecorded case it is.
// It mirrors the rule the ledger applies, which the ledger does not export.
func bindableLayerDigest(layer string) bool {
	digest := strings.TrimPrefix(layer, "sha256:")
	if len(digest) != 64 {
		return false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// checkoutRecord is what the store remembers about one prepared checkout: the
// directory the index resolved to, the object the kernel found there, the state
// the shape was derived from, that shape, and the digest of the tree that was
// walked. It is a claim and not a proof, and it is filed so that a store can
// describe itself, not so that a launch can be decided by it.
type checkoutRecord struct {
	Format      int    `json:"format"`
	Layer       string `json:"layer"`
	Directory   string `json:"directory"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	State       string `json:"state,omitempty"`
	Expected    string `json:"expected,omitempty"`
	Measurement string `json:"measurement"`
}

// describes reports whether another record was taken from the same object,
// reached by the same name, in the same format. The format is part of it
// because a record written in another one holds a digest of something else,
// and comparing the two would report a disagreement that is only a version.
func (r checkoutRecord) describes(other checkoutRecord) bool {
	return r.Format == other.Format && r.Directory == other.Directory &&
		r.Device == other.Device && r.Inode == other.Inode
}

// checkoutRecords keeps one record per layer beside the layer bindings, so
// that a single directory holds everything the store claims about a layer.
type checkoutRecords struct {
	directory string
}

func (c *Cpak) checkoutRecords() (*checkoutRecords, error) {
	directory := c.GetInStoreDir("bindings")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("prepare the checkout measurement directory: %w", err)
	}
	return &checkoutRecords{directory: directory}, nil
}

// path is built from a validated layer identifier, which carries no separator,
// so no layer a caller supplies can name a file outside the directory. The
// suffix keeps these records apart from the layer bindings filed next to them.
func (s *checkoutRecords) path(layer string) string {
	return filepath.Join(s.directory, layer+".checkout.json")
}

// publish files a measurement. A record that says the same thing again is
// accepted in silence. A different measurement of the same object is refused:
// one of the two answers is not the tree the driver prepared, and the
// disagreement is the finding. A record whose object no longer exists is
// replaced, because it speaks for a tree that is gone and can no longer be
// compared with anything.
//
// This is the whole of what the record is for. It is what is left where a state
// cannot be derived, so it has to keep working when the derivation stops: only
// the measurement decides the refusal, and a record that agrees about the tree
// while naming another state is rewritten rather than treated as a finding.
func (s *checkoutRecords) publish(record checkoutRecord) error {
	existing, found, err := s.lookup(record.Layer)
	if err != nil {
		return err
	}
	if found {
		if existing == record {
			return nil
		}
		if existing.describes(record) && existing.Measurement != record.Measurement {
			return fmt.Errorf("%w: %s", errCheckoutChanged, record.Layer)
		}
	}
	staged, err := s.stage(record)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err = os.Rename(staged, s.path(record.Layer)); err != nil {
		return fmt.Errorf("publish the checkout measurement of %s: %w", record.Layer, err)
	}
	return nil
}

// lookup answers with the record filed for a layer.
func (s *checkoutRecords) lookup(layer string) (checkoutRecord, bool, error) {
	if err := storage.ValidateLayerID(layer); err != nil {
		return checkoutRecord{}, false, err
	}
	data, err := os.ReadFile(s.path(layer))
	if errors.Is(err, fs.ErrNotExist) {
		return checkoutRecord{}, false, nil
	}
	if err != nil {
		return checkoutRecord{}, false, fmt.Errorf("read the checkout measurement of %s: %w", layer, err)
	}
	if len(data) > checkoutRecordLimit {
		return checkoutRecord{}, false, fmt.Errorf("the checkout measurement of %s is too large", layer)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record checkoutRecord
	if err = decoder.Decode(&record); err != nil {
		return checkoutRecord{}, false, fmt.Errorf("decode the checkout measurement of %s: %w", layer, err)
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return checkoutRecord{}, false, fmt.Errorf("the checkout measurement of %s carries more than one record", layer)
	}
	// A record that does not name the layer it is filed under is refused
	// rather than returned, so moving a file cannot make it answer for another
	// layer.
	if record.Layer != layer {
		return checkoutRecord{}, false, fmt.Errorf("the checkout measurement of %s does not name its own layer", layer)
	}
	if record.Directory == "" || record.Measurement == "" {
		return checkoutRecord{}, false, fmt.Errorf("the checkout measurement of %s is incomplete", layer)
	}
	return record, true, nil
}

// stage writes the record beside its destination, so that publishing it is a
// rename within one directory and never a copy across filesystems.
func (s *checkoutRecords) stage(record checkoutRecord) (string, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode the checkout measurement of %s: %w", record.Layer, err)
	}
	file, err := os.CreateTemp(s.directory, ".checkout-")
	if err != nil {
		return "", fmt.Errorf("stage the checkout measurement of %s: %w", record.Layer, err)
	}
	if err = writeCheckoutRecord(file, append(encoded, '\n')); err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("stage the checkout measurement of %s: %w", record.Layer, err)
	}
	return file.Name(), nil
}

func writeCheckoutRecord(file *os.File, data []byte) error {
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// recordCheckoutMeasurement measures a prepared checkout and files what it
// found. Where the store can derive what that checkout should be, the walk is
// compared with the derivation before anything is filed, so a checkout that
// changed after it was prepared stops here and not on the way back from the
// overlay. Where it cannot, nothing is claimed beyond what was walked.
func (c *Cpak) recordCheckoutMeasurement(layerDigest, directory string) error {
	shape, err := c.boundCheckoutShape(layerDigest)
	if err != nil && !errors.Is(err, errCheckoutUnderivable) {
		return err
	}
	record, err := c.measurePreparedCheckout(layerDigest, directory, shape)
	if err != nil {
		return err
	}
	if shape.derived() && record.Measurement != shape.digest {
		return fmt.Errorf("%w: %s: %s", errCheckoutUnexpected, layerDigest, record.Directory)
	}
	records, err := c.checkoutRecords()
	if err != nil {
		return err
	}
	return records.publish(record)
}

// verifyPreparedCheckout walks the directory a prepared index resolved to and
// compares it with the shape the bound state of the layer describes. A layer
// whose shape cannot be derived answers found=false, because nothing then
// speaks for what is about to be mounted, which is the unrecorded case and not
// a match.
//
// The filed record decides nothing here. It sits in a store the launch can
// write, so it can be rewritten to agree with any tree at all; the only side of
// this comparison that an attacker does not own is the state, and that is the
// side the walk is put against.
func (c *Cpak) verifyPreparedCheckout(layerDigest, directory string) (found bool, matches bool, err error) {
	shape, err := c.boundCheckoutShape(layerDigest)
	if errors.Is(err, errCheckoutUnderivable) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	record, err := c.measurePreparedCheckout(layerDigest, directory, shape)
	if err != nil {
		return true, false, err
	}
	return true, record.Measurement == shape.digest, nil
}

// recordPreparedCheckouts measures every directory a prepared index resolved
// to. The directories arrive in overlay priority order, which is the reverse
// of the layer order, so the pairing the index made has to be undone before a
// measurement is filed under the layer it belongs to.
func (c *Cpak) recordPreparedCheckouts(layers, directories []string) error {
	if len(layers) != len(directories) {
		return fmt.Errorf("the storage driver named %d directories for %d layers", len(directories), len(layers))
	}
	for index, directory := range directories {
		if err := c.recordCheckoutMeasurement(layers[len(layers)-1-index], directory); err != nil {
			return err
		}
	}
	return nil
}

// measurePreparedCheckout measures the directory a prepared index resolved to.
// The name is opened once and the walk goes on from that descriptor, so the
// tree that ends up in the record is the one the kernel placed under the
// storage driver root, and not whatever the name resolves to a moment later.
func (c *Cpak) measurePreparedCheckout(layerDigest, directory string, shape checkoutShape) (checkoutRecord, error) {
	if err := storage.ValidateLayerID(layerDigest); err != nil {
		return checkoutRecord{}, err
	}
	checkout, resolved, err := c.openPreparedCheckout(directory)
	if err != nil {
		return checkoutRecord{}, err
	}
	defer unix.Close(checkout)
	identity, err := tools.IdentifyDescriptor(checkout)
	if err != nil {
		return checkoutRecord{}, err
	}
	if identity.Kind != tools.DescriptorKindDirectory {
		return checkoutRecord{}, fmt.Errorf("the prepared checkout %s is a %s", resolved, identity.Kind)
	}
	measurement, err := measureCheckoutTree(checkout, shape.loose)
	if err != nil {
		return checkoutRecord{}, fmt.Errorf("measure the prepared checkout %s: %w", resolved, err)
	}
	return checkoutRecord{
		Format:      checkoutRecordFormat,
		Layer:       layerDigest,
		Directory:   resolved,
		Device:      identity.Device,
		Inode:       identity.Inode,
		State:       shape.state,
		Expected:    shape.digest,
		Measurement: measurement,
	}, nil
}

// openPreparedCheckout opens the directory a prepared index named, under the
// storage driver root, and answers with its descriptor next to the path the
// kernel reached it by. Containment is decided by the kernel and not by
// comparing strings: the same call that opens refuses the paths that climb out
// of the root and the ones that step through a symlink. The caller closes the
// descriptor.
func (c *Cpak) openPreparedCheckout(directory string) (int, string, error) {
	driver, err := c.storageDriverName()
	if err != nil {
		return -1, "", err
	}
	root, err := filepath.EvalSymlinks(c.storageDriverRoot(driver))
	if err != nil {
		return -1, "", fmt.Errorf("resolve the storage driver root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return -1, "", fmt.Errorf("resolve the prepared checkout %s: %w", directory, err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || !underDriverRoot(relative) {
		return -1, "", fmt.Errorf("%w: %s", errCheckoutOutsideDriver, directory)
	}
	fd, err := tools.OpenBeneath(root, relative, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return -1, "", fmt.Errorf("open the prepared checkout %s: %w", directory, err)
	}
	return fd, resolved, nil
}

// underDriverRoot reports whether a relative path stays inside the root it was
// taken against. The root itself is not a checkout and is refused with the
// paths that leave it.
func underDriverRoot(relative string) bool {
	if relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// measureCheckout digests what a prepared checkout directory actually holds,
// cheaply enough for a launch: metadata only, never file contents. loose names
// the paths whose permission bits the state does not decide, and comes from the
// shape this measurement is going to be compared with.
func measureCheckout(directory string, loose map[string]bool) (string, error) {
	// The directory is its own anchor here. A caller that reached it through a
	// user writable index goes through openPreparedCheckout instead, which has
	// the kernel place it under the storage driver root first and then keeps
	// the descriptor it got.
	root, err := tools.OpenBeneath(directory, ".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return "", fmt.Errorf("open the prepared checkout %s: %w", directory, err)
	}
	defer unix.Close(root)
	measurement, err := measureCheckoutTree(root, loose)
	if err != nil {
		return "", fmt.Errorf("measure the prepared checkout %s: %w", directory, err)
	}
	return measurement, nil
}

// measureCheckoutTree measures the tree an open checkout descriptor holds.
//
// The digest covers, for every entry in the order a walk reaches it, the path
// it has relative to the checkout, its type, its permission bits where the
// state fixes them, its size and, for a symlink, its target. So it answers for
// the shape of the tree and not for what the files in it say: content replaced
// by content of the same length measures the same, and so does a change to an
// owner or an extended attribute, neither of which a state records. Answering
// for the content means reading the content, which is CheckPreparedCheckoutContents
// and is seconds against the tens of milliseconds this costs. A launch can be
// asked to pay the second figure and not the first.
//
// Nothing is skipped: an entry that cannot be read is an error, because a
// measurement that quietly leaves out what it could not open is a measurement
// of a tree that does not exist.
func measureCheckoutTree(root int, loose map[string]bool) (string, error) {
	// The root is left out of the permission comparison whether or not a shape
	// was derived, so that a measurement taken before a layer was bound and one
	// taken after are the same answer about the same tree.
	if loose == nil {
		loose = map[string]bool{".": true}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(root, &stat); err != nil {
		return "", fmt.Errorf("stat the checkout root: %w", err)
	}
	digest := newCheckoutDigest()
	writeCheckoutEntry(digest, walkedCheckoutEntry(".", stat, "", loose))
	if err := measureCheckoutEntries(digest, root, "", loose); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// newCheckoutDigest opens a digest over the domain both sides of the comparison
// belong to, so a digest of a checkout can never be read as a digest of
// anything else, and a digest taken in an older format never compares equal.
func newCheckoutDigest() hash.Hash {
	digest := sha256.New()
	fmt.Fprintf(digest, "cpak.checkout.v%d.%d\n", integrity.ABIVersion, checkoutRecordFormat)
	return digest
}

// measureCheckoutEntries walks one directory of the checkout. Every step is
// taken from the descriptor of the directory it is in and never from a path
// rebuilt as a string, so a component renamed halfway through the walk cannot
// send the walk somewhere else.
func measureCheckoutEntries(digest hash.Hash, directory int, prefix string, loose map[string]bool) error {
	names, err := checkoutDirectoryNames(directory)
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		relative := prefix + name
		var stat unix.Stat_t
		if statErr := unix.Fstatat(directory, name, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
			return fmt.Errorf("stat %s: %w", relative, statErr)
		}
		target := ""
		if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
			target, err = checkoutLinkTarget(directory, name, stat.Size)
			if err != nil {
				return fmt.Errorf("read the link %s: %w", relative, err)
			}
		}
		writeCheckoutEntry(digest, walkedCheckoutEntry(relative, stat, target, loose))
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		child, openErr := tools.OpenBeneathAt(directory, name, unix.O_RDONLY|unix.O_DIRECTORY)
		if openErr != nil {
			return fmt.Errorf("open %s: %w", relative, openErr)
		}
		err = measureCheckoutEntries(digest, child, relative+"/", loose)
		_ = unix.Close(child)
		if err != nil {
			return err
		}
	}
	return nil
}

// walkedCheckoutEntry reduces what the kernel reported about one object to the
// same fields the derivation states, so the two can be digested together.
func walkedCheckoutEntry(relative string, stat unix.Stat_t, target string, loose map[string]bool) checkoutEntry {
	entry := checkoutEntry{
		path:  relative,
		kind:  checkoutKind(uint32(stat.Mode)),
		mode:  uint32(stat.Mode) & 0o7777,
		fixed: !loose[relative],
		size:  stat.Size,
		link:  target,
	}
	if entry.kind == "dir" {
		// The size of a directory is what the filesystem spends on it, which
		// is not something a state decides.
		entry.size = 0
	}
	return entry
}

// checkoutKind names what an object is, in the vocabulary the state uses, so
// that a file replaced by a directory is a different entry and not a different
// set of bits inside the same one.
func checkoutKind(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return "dir"
	case unix.S_IFREG:
		return "file"
	case unix.S_IFLNK:
		return "symlink"
	case unix.S_IFIFO:
		return "fifo"
	case unix.S_IFSOCK:
		return "socket"
	case unix.S_IFCHR:
		return "char"
	case unix.S_IFBLK:
		return "block"
	}
	return "unknown"
}

// writeCheckoutEntry appends one entry to the digest. The fields are separated
// by a NUL, which a file name cannot contain, so no set of names can be spelt
// to look like a different set of entries. A permission field the state does
// not decide is written as a dash on both sides rather than left out, so an
// entry never borrows the shape of its neighbour.
func writeCheckoutEntry(digest io.Writer, entry checkoutEntry) {
	mode := "-"
	if entry.fixed {
		mode = fmt.Sprintf("%04o", entry.mode&0o7777)
	}
	fmt.Fprintf(digest, "%s\x00%s\x00%s\x00%d\x00%s\n", entry.path, entry.kind, mode, entry.size, entry.link)
}

// checkoutDirectoryNames lists one directory through a duplicate of its
// descriptor, so the walk keeps the descriptor it opened and the listing can
// never be pointed at another directory by a rename.
func checkoutDirectoryNames(directory int) ([]string, error) {
	duplicate, err := unix.FcntlInt(uintptr(directory), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate the checkout descriptor: %w", err)
	}
	file := os.NewFile(uintptr(duplicate), "checkout")
	names, err := file.Readdirnames(-1)
	closeErr := file.Close()
	if err != nil {
		return nil, fmt.Errorf("list a checkout directory: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("list a checkout directory: %w", closeErr)
	}
	return names, nil
}

// checkoutLinkTarget reads a symlink from the descriptor of the directory it
// sits in. A target that fills the buffer was truncated, which would measure a
// prefix of the link as if it were the link, so it is refused instead.
func checkoutLinkTarget(directory int, name string, size int64) (string, error) {
	length := int(size) + 1
	if length < 128 {
		length = 128
	}
	buffer := make([]byte, length)
	read, err := unix.Readlinkat(directory, name, buffer)
	if err != nil {
		return "", err
	}
	if read >= len(buffer) {
		return "", errors.New("the link target is longer than the size reported for it")
	}
	return string(buffer[:read]), nil
}
