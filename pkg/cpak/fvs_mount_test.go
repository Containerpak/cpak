package cpak

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// The layers below are spelt as bare sha256 digests because that is what the
// binding ledger files, and a reference of another shape can never be bound.
const (
	legacyBoundLayer  = "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1"
	legacyStatedLayer = "c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2"
	legacyOrphanLayer = "d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3"
)

func legacyStore(t *testing.T, layers ...string) Cpak {
	t.Helper()
	root := t.TempDir()
	for _, layer := range layers {
		if err := os.MkdirAll(filepath.Join(root, "layers", layer), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return Cpak{Options: types.CpakOptions{StorePath: root}}
}

// plantLegacyLayer writes a tree where the legacy layout keeps a layer, which
// is what an installation made before repositories left behind and equally what
// anyone who may write the store can put there.
func plantLegacyLayer(t *testing.T, cp *Cpak, layer string, content []byte) string {
	t.Helper()
	root := cp.GetInStoreDir("layers", layer)
	if err := os.MkdirAll(filepath.Join(root, "usr", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr", "share", "value"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLegacyLayerDirectoriesUseOverlayPriority(t *testing.T) {
	c := legacyStore(t, "base", "top", "runtime")
	got, err := c.legacyLayerDirectories([]string{"base", "top", "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	root := c.Options.StorePath
	want := []string{
		filepath.Join(root, "layers", "runtime"),
		filepath.Join(root, "layers", "top"),
		filepath.Join(root, "layers", "base"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected overlay order: got %v, want %v", got, want)
	}
}

// A legacy layout used to reach the caller as a success carrying no directories,
// which left the spawn side to rebuild the paths from an argument nobody had
// checked. The directories have to come back with the success.
func TestLegacyLayoutReturnsItsDirectories(t *testing.T) {
	c := legacyStore(t, "base", "top")
	_, lowerDirs, _, err := c.prepareLayerMount(t.TempDir(), []string{"base", "top"})
	if err != nil {
		t.Fatal(err)
	}
	if lowerDirs == "" {
		t.Fatal("the legacy layout reported success without any layer directory")
	}
	for _, layer := range []string{"base", "top"} {
		if !strings.Contains(lowerDirs, filepath.Join(c.Options.StorePath, "layers", layer)) {
			t.Fatalf("%s is missing from %s", layer, lowerDirs)
		}
	}
}

// The binding is what an anchor is taken over, and it outlives the repository it
// names. Removing the repository is the whole of the attack: a check that only
// asked whether the store still holds a state would find none, decide the legacy
// directory is all there is, and serve it to a launch the ledger recognises.
func TestLegacyLayerDirectoriesRefuseTheCopyOfABoundLayer(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, legacyBoundLayer, "usr/share/value", []byte("payload"))
	if err := cp.recordLayerBinding(legacyBoundLayer); err != nil {
		t.Fatalf("the layer was not bound, so this test proves nothing: %v", err)
	}
	if err := os.RemoveAll(cp.fvsLayerPath(legacyBoundLayer)); err != nil {
		t.Fatal(err)
	}
	plantLegacyLayer(t, cp, legacyBoundLayer, []byte("trojan"))

	if _, err := cp.legacyLayerDirectories([]string{legacyBoundLayer}); !errors.Is(err, errLegacyLayerRefused) {
		t.Fatalf("the legacy copy of a bound layer was served: %v", err)
	}
}

// A state the store still holds is the answer for that layer, and a directory
// left beside it is a second copy nothing derives.
func TestLegacyLayerDirectoriesRefuseTheCopyOfAStoredLayer(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, legacyStatedLayer, "usr/share/value", []byte("payload"))
	plantLegacyLayer(t, cp, legacyStatedLayer, []byte("trojan"))

	if _, err := cp.legacyLayerDirectories([]string{legacyStatedLayer}); !errors.Is(err, errLegacyLayerRefused) {
		t.Fatalf("the legacy copy of a stored layer was served: %v", err)
	}
}

// The one thing this path must never do. A launch can be recognised only when
// every layer of it carries a binding, which is what launchPackageRoot deriving
// a root proves here, and a bound layer is exactly what the refusal keys on, so
// the two cannot both hold.
func TestALaunchThatCanBeRecognisedIsNeverServedTheLegacyCopy(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, legacyBoundLayer, "usr/share/value", []byte("payload"))
	if err := cp.recordLayerBinding(legacyBoundLayer); err != nil {
		t.Fatalf("the layer was not bound, so this test proves nothing: %v", err)
	}
	plantLegacyLayer(t, cp, legacyBoundLayer, []byte("trojan"))
	app := types.Application{
		Origin:       "github.com/containerpak/legacy",
		ImageDigest:  "sha256:" + legacyBoundLayer,
		ParsedLayers: []string{legacyBoundLayer},
	}

	if _, err := cp.launchPackageRoot(app, nil, nil); err != nil {
		t.Fatalf("the launch has no derivable identity, so this test says nothing about a recognisable one: %v", err)
	}
	if _, err := cp.legacyLayerDirectories(app.ParsedLayers); !errors.Is(err, errLegacyLayerRefused) {
		t.Fatalf("a launch whose identity the ledger can derive was served the legacy copy of %s: %v", legacyBoundLayer, err)
	}
}

// The refusal has to reach the caller as itself. Reported as a missing storage
// service it would read as something to install, when what happened is that the
// store answers for these layers somewhere the mount was not looking.
func TestPrepareLayerMountReportsTheLegacyRefusalItMade(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CPAK_STORAGE_BINARY", "")
	t.Setenv("CPAK_FVS2D_BINARY", "")
	t.Setenv("CPAK_STORAGE_DRIVER_BINARY", filepath.Join(t.TempDir(), "missing"))
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, legacyBoundLayer, "usr/share/value", []byte("payload"))
	if err := cp.recordLayerBinding(legacyBoundLayer); err != nil {
		t.Fatalf("the layer was not bound, so this test proves nothing: %v", err)
	}
	if err := os.RemoveAll(cp.fvsLayerPath(legacyBoundLayer)); err != nil {
		t.Fatal(err)
	}
	plantLegacyLayer(t, cp, legacyBoundLayer, []byte("trojan"))

	_, lowerDirs, _, err := cp.prepareLayerMount(t.TempDir(), []string{legacyBoundLayer})
	if !errors.Is(err, errLegacyLayerRefused) {
		t.Fatalf("the mount answered %v where it had refused the only layer it had", err)
	}
	if lowerDirs != "" {
		t.Fatalf("a refused mount still handed out %q", lowerDirs)
	}
}

// A store the launch gate can never recognise keeps working, which is the half
// of the legacy layout this change deliberately leaves alone.
func TestLegacyLayerDirectoriesServeALayerNothingAnswersFor(t *testing.T) {
	cp := newTestCpak(t)
	root := plantLegacyLayer(t, cp, legacyOrphanLayer, []byte("payload"))

	got, err := cp.legacyLayerDirectories([]string{legacyOrphanLayer})
	if err != nil {
		t.Fatalf("a layer the store holds no answer for was refused: %v", err)
	}
	if !slices.Equal(got, []string{root}) {
		t.Fatalf("got %v, want the legacy directory %q", got, root)
	}
}

// The cost of refusing rather than measuring on first use: on the half that
// stays open nothing looks at the tree at all, so a directory that was already
// trojaned before cpak ever saw it is served exactly as it stands. That is
// bounded by the other tests here, which show such a layer can never carry a
// binding and so can never belong to a launch an anchor recognises.
func TestTheLegacyPathIsBlindToWhatAnUnansweredDirectoryHolds(t *testing.T) {
	cp := newTestCpak(t)
	root := plantLegacyLayer(t, cp, legacyOrphanLayer, []byte("payload"))
	before, err := cp.legacyLayerDirectories([]string{legacyOrphanLayer})
	if err != nil {
		t.Fatal(err)
	}

	value := filepath.Join(root, "usr", "share", "value")
	if err := os.WriteFile(value, []byte("trojan"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := cp.legacyLayerDirectories([]string{legacyOrphanLayer})
	if err != nil {
		t.Fatalf("the swapped directory was refused, so this test no longer documents what it was written for: %v", err)
	}
	if !slices.Equal(before, after) {
		t.Fatalf("the answer moved from %v to %v, so this test no longer documents what it was written for", before, after)
	}
	content, err := os.ReadFile(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "trojan" {
		t.Fatalf("the swapped content is %q, so nothing was swapped", content)
	}
}

func TestLegacyLayerFindingsNameTheDirectoriesNothingMeasures(t *testing.T) {
	cp := newTestCpak(t)
	root := plantLegacyLayer(t, cp, legacyOrphanLayer, []byte("payload"))
	seedFVSLayerFile(t, cp, legacyStatedLayer, "usr/share/value", []byte("payload"))

	findings, err := cp.legacyLayerFindings([]string{legacyOrphanLayer, legacyStatedLayer, legacyOrphanLayer})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want the one layer the legacy layout holds: %v", len(findings), findings)
	}
	if findings[0].Layer != legacyOrphanLayer {
		t.Fatalf("the finding names %q, want %q", findings[0].Layer, legacyOrphanLayer)
	}
	if !strings.Contains(findings[0].Detail, root) {
		t.Fatalf("the finding does not name the directory %q: %s", root, findings[0].Detail)
	}
}

// The handoff described on legacyLayerFindings is not wired: measureLaunch does
// not call it, so a launch served out of the legacy layout still measures as a
// launch with nothing to report. This pins that gap, and fails the day it is
// closed rather than letting the silence pass unnoticed.
func TestMeasureLaunchStillReportsNothingAboutALegacyLayer(t *testing.T) {
	cp := newTestCpak(t)
	plantLegacyLayer(t, cp, legacyOrphanLayer, []byte("payload"))

	measurement, err := cp.measureLaunch([]string{legacyOrphanLayer})
	if err != nil {
		t.Fatal(err)
	}
	if len(measurement.Disagreements) != 0 || len(measurement.Unrecorded) != 0 || len(measurement.Unmeasured) != 0 {
		t.Fatalf("measureLaunch now reports the legacy layer as %+v; fold this test into the wiring that made it do so", measurement)
	}
}
