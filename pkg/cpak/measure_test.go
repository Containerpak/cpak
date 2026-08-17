/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/storaged"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	measuredLayer      = "7777777777777777777777777777777777777777777777777777777777777777"
	measuredOtherLayer = "8888888888888888888888888888888888888888888888888888888888888888"
)

func measuredApplication(layers ...string) types.Application {
	return types.Application{
		CpakId:       "measured",
		Name:         "demo",
		Version:      "1.0",
		Branch:       "main",
		Origin:       testOrigin,
		ImageDigest:  "sha256:image",
		Config:       `{"config":{"Env":["PATH=/usr/bin"]}}`,
		ParsedLayers: layers,
	}
}

// rebuildFVSLayer throws a layer repository away and builds another one in its
// place. It is the shape the finding is about: the record filed for the layer
// is left exactly as it was, and only what the store holds moves.
func rebuildFVSLayer(t *testing.T, cp *Cpak, digest string, content []byte) {
	t.Helper()
	if err := os.RemoveAll(cp.fvsLayerPath(digest)); err != nil {
		t.Fatal(err)
	}
	seedFVSLayerFile(t, cp, digest, "usr/share/value", content)
}

// rewriteLayerBinding edits the record in place, which the account running the
// launch may do: the bindings live in a store it owns.
func rewriteLayerBinding(t *testing.T, cp *Cpak, digest string, change func(*integrity.LayerBinding)) {
	t.Helper()
	path := cp.GetInStoreDir("bindings", digest+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var binding integrity.LayerBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		t.Fatal(err)
	}
	change(&binding)
	edited, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
}

// preparedCheckouts puts layers through the storage driver the way a launch
// does, so that the index and the checkouts the gate reads are the real ones.
// It answers with the directory of each layer, undoing the overlay priority
// order the driver hands them back in.
func preparedCheckouts(t *testing.T, cp *Cpak, digests ...string) map[string]string {
	t.Helper()
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	for _, digest := range digests {
		seedFVSLayerFile(t, cp, digest, "usr/share/value", []byte("the value of "+digest))
		if err := cp.recordLayerBinding(digest); err != nil {
			t.Fatalf("the layer binding was refused: %v", err)
		}
	}
	paths, err := cp.prepareStorageDriver(digests)
	if err != nil {
		t.Fatalf("the layers could not be prepared: %v", err)
	}
	directories := make(map[string]string, len(digests))
	for index, path := range paths {
		directories[digests[len(digests)-1-index]] = path
	}
	return directories
}

// This is the finding. The record lives in a store the launching account owns,
// so it proves only that a record exists: leave it untouched, change the layer
// under it, and the launch used to be recognised as the layer it replaced.
func TestGateLaunchRefusesALayerRebuiltUnderItsRecord(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	seedFVSLayerFile(t, cp, measuredLayer, "usr/share/value", []byte("value"))
	if err := cp.recordLayerBinding(measuredLayer); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
	app := measuredApplication(measuredLayer)
	if _, err := cp.gateLaunch(app, types.Override{}, nil, nil); err != nil {
		t.Fatalf("a launch nothing had touched was refused: %v", err)
	}

	rebuildFVSLayer(t, cp, measuredLayer, []byte("something else entirely"))
	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchTampered) {
		t.Fatalf("got %v, want a layer rebuilt under its record to be refused", err)
	}
	if identity.Verdict != LaunchTampered {
		t.Fatalf("got verdict %s, want a store that disagrees with what it recorded", identity.Verdict)
	}
	if len(identity.Disagreements) != 1 || !strings.Contains(identity.Disagreements[0], measuredLayer) {
		t.Fatalf("the refusal does not name the layer that moved: %v", identity.Disagreements)
	}
	if !strings.Contains(identity.Disagreements[0], "state") {
		t.Fatalf("the refusal is not about the state of the layer: %q", identity.Disagreements[0])
	}
}

// The other direction of the same finding: the layer is untouched and the
// record is edited to claim something the store never produced.
func TestGateLaunchRefusesARewrittenLayerBinding(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	seedFVSLayerFile(t, cp, measuredLayer, "usr/share/value", []byte("value"))
	if err := cp.recordLayerBinding(measuredLayer); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
	rewriteLayerBinding(t, cp, measuredLayer, func(binding *integrity.LayerBinding) {
		binding.StateRoot = strings.Repeat("b", 64)
	})

	identity, err := cp.gateLaunch(measuredApplication(measuredLayer), types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchTampered) {
		t.Fatalf("got %v, want a record the store does not support to be refused", err)
	}
	if identity.Verdict != LaunchTampered {
		t.Fatalf("got verdict %s, want a store that disagrees with what it recorded", identity.Verdict)
	}
}

// Being unenrolled means nobody said what the launch should be. It does not
// mean the store may disagree with itself, so this refusal is not behind the
// switch that lets an unenrolled application run.
func TestGateLaunchRefusesATamperedStoreThatNothingEnrolled(t *testing.T) {
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	seedFVSLayerFile(t, cp, measuredLayer, "usr/share/value", []byte("value"))
	if err := cp.recordLayerBinding(measuredLayer); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
	if _, enrolled, err := ledger.Load(uint32(os.Getuid()), testOrigin); err != nil || enrolled {
		t.Fatalf("the ledger already answers for %s, so this test proves nothing: enrolled=%v err=%v", testOrigin, enrolled, err)
	}

	// Off is the level a host that has never been told otherwise runs at, and
	// it is the one where a tampered store still has to be refused.
	useEnforcement(t, systemauthority.EnforcementOff)

	app := measuredApplication(measuredLayer)
	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatalf("an unenrolled launch nothing had touched was refused: %v", err)
	}
	if identity.Verdict != LaunchUnenrolled {
		t.Fatalf("got verdict %s, want an application nothing was recorded for", identity.Verdict)
	}

	rebuildFVSLayer(t, cp, measuredLayer, []byte("something else entirely"))
	if _, err = cp.gateLaunch(app, types.Override{}, nil, nil); !errors.Is(err, errLaunchTampered) {
		t.Fatalf("got %v, want an unenrolled launch over a changed layer to be refused anyway", err)
	}
}

// The gate answers for the tree that is mounted, and on this backend that tree
// is the prepared checkout, which no layer binding covers.
func TestGateLaunchRefusesAChangedPreparedCheckout(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	useAnchorLedger(t)
	checkout := preparedCheckouts(t, cp, measuredLayer)[measuredLayer]

	app := measuredApplication(measuredLayer)
	if _, err := cp.gateLaunch(app, types.Override{}, nil, nil); err != nil {
		t.Fatalf("a prepared checkout nothing had touched was refused: %v", err)
	}
	tamperWithCheckout(t, filepath.Join(checkout, "usr", "share", "value"))

	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchTampered) {
		t.Fatalf("got %v, want a changed prepared checkout to be refused", err)
	}
	if identity.Verdict != LaunchTampered {
		t.Fatalf("got verdict %s, want a store that disagrees with what it recorded", identity.Verdict)
	}
	// The fvs layer was never touched, so the finding has to be the checkout
	// and not the state, or this test would pass for the wrong reason.
	if len(identity.Disagreements) != 1 || !strings.Contains(identity.Disagreements[0], "prepared checkout") {
		t.Fatalf("the refusal is not about the prepared checkout: %v", identity.Disagreements)
	}
}

// The index hands its directories back in overlay priority order, which is the
// reverse of the layer order. A gate that paired them in the order it received
// them would answer for the wrong layer, and a launch of one layer can never
// show that, so the pairing is checked against a launch of two.
func TestGateLaunchNamesTheLayerWhoseCheckoutChanged(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	useAnchorLedger(t)
	directories := preparedCheckouts(t, cp, measuredLayer, measuredOtherLayer)

	app := measuredApplication(measuredLayer, measuredOtherLayer)
	if _, err := cp.gateLaunch(app, types.Override{}, nil, nil); err != nil {
		t.Fatalf("two prepared checkouts nothing had touched were refused: %v", err)
	}
	tamperWithCheckout(t, filepath.Join(directories[measuredOtherLayer], "usr", "share", "value"))

	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchTampered) {
		t.Fatalf("got %v, want the changed checkout of the second layer to be refused", err)
	}
	if len(identity.Disagreements) != 1 || !strings.Contains(identity.Disagreements[0], measuredOtherLayer) {
		t.Fatalf("the refusal does not name the layer that changed: %v", identity.Disagreements)
	}
	if strings.Contains(identity.Disagreements[0], measuredLayer) {
		t.Fatalf("the refusal blames the layer nothing touched as well: %v", identity.Disagreements)
	}
}

// A checkout no state describes is not a match and not a mismatch. Nothing
// answers for what is about to be mounted, which is the unbound case. The
// binding is left in place here, so the launch is still describable and the
// refusal comes from the checkout half and not from the package root.
func TestGateLaunchTreatsAnUndescribedPreparedCheckoutAsUnbound(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	preparedCheckouts(t, cp, measuredLayer)

	app := measuredApplication(measuredLayer)
	derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)
	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if err != nil || identity.Verdict != LaunchRecognised {
		t.Fatalf("the launch that was enrolled was not recognised: verdict %s err %v", identity.Verdict, err)
	}

	if err = os.RemoveAll(cp.fvsLayerPath(measuredLayer)); err != nil {
		t.Fatal(err)
	}
	identity, err = cp.gateLaunch(app, types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchUnbound) {
		t.Fatalf("got %v, want a checkout no state describes to be refused as unbound", err)
	}
	if identity.Verdict != LaunchUnbound {
		t.Fatalf("got verdict %s, want a launch nothing answers for", identity.Verdict)
	}
	if len(identity.Unmeasured) == 0 {
		t.Fatal("the launch was refused without saying which layer could not be described")
	}
}

// The gate against the finding this round closes. Every file the old
// comparison read is one the launching account may write, so an attacker could
// change the prepared checkout and then write the measurement of the changed
// tree into the record. Nothing else moves here: the layer, its state and its
// binding are all untouched, and the launch is one the ledger recognised a
// moment earlier.
func TestGateLaunchRefusesACheckoutWhoseRecordWasRewrittenToAgree(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	ledger := useAnchorLedger(t)
	checkout := preparedCheckouts(t, cp, measuredLayer)[measuredLayer]

	app := measuredApplication(measuredLayer)
	derived, err := cp.verifyLaunch(app, types.Override{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol(t, ledger, derived)
	if identity, gateErr := cp.gateLaunch(app, types.Override{}, nil, nil); gateErr != nil || identity.Verdict != LaunchRecognised {
		t.Fatalf("the launch that was enrolled was not recognised: verdict %s err %v", identity.Verdict, gateErr)
	}

	tamperWithCheckout(t, filepath.Join(checkout, "usr", "share", "value"))
	rewriteCheckoutRecord(t, cp, measuredLayer, checkout)

	identity, err := cp.gateLaunch(app, types.Override{}, nil, nil)
	if !errors.Is(err, errLaunchTampered) {
		t.Fatalf("got %v, want a checkout whose record was rewritten to agree with it to be refused", err)
	}
	if identity.Verdict != LaunchTampered {
		t.Fatalf("got verdict %s, want a store that no longer holds what the state describes", identity.Verdict)
	}
	if len(identity.Disagreements) != 1 || !strings.Contains(identity.Disagreements[0], "prepared checkout") {
		t.Fatalf("the refusal is not about the prepared checkout: %v", identity.Disagreements)
	}
}

// A launch that mounts the repositories through FUSE never reads the prepared
// index, so a checkout left over in it is not what runs and must not decide
// anything about the launch.
func TestGateLaunchIgnoresThePreparedIndexUnderTheFUSEBackend(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	useAnchorLedger(t)
	checkout := preparedCheckouts(t, cp, measuredLayer)[measuredLayer]
	tamperWithCheckout(t, filepath.Join(checkout, "usr", "share", "value"))

	t.Setenv("CPAK_STORAGE_BACKEND", "fuse")
	identity, err := cp.gateLaunch(measuredApplication(measuredLayer), types.Override{}, nil, nil)
	if err != nil {
		t.Fatalf("a checkout the launch does not mount refused the launch: %v", err)
	}
	if identity.Verdict != LaunchUnenrolled {
		t.Fatalf("got verdict %s, want the checkout to have decided nothing", identity.Verdict)
	}
}

// The comparison is only worth anything if the two sides are derived the same
// way, so this pins that they are rather than trusting that they look alike.
func TestDeriveLayerStateAnswersWhatARecordedBindingHolds(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, measuredLayer, "usr/share/value", []byte("value"))
	if err := cp.recordLayerBinding(measuredLayer); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
	bindings, err := cp.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	recorded, found, err := bindings.Lookup(measuredLayer)
	if err != nil || !found {
		t.Fatalf("the layer that was just bound is not bound: found=%v err=%v", found, err)
	}
	derived, err := cp.deriveLayerState(measuredLayer)
	if err != nil {
		t.Fatalf("the layer the store holds could not be re-derived: %v", err)
	}
	if detail, agrees := compareLayerState(recorded, derived); !agrees {
		t.Fatalf("re-deriving an untouched layer disagreed with its own record: %s", detail)
	}
	if derived.StateID != recorded.StateID || derived.StateRoot != recorded.StateRoot {
		t.Fatalf("got state %s root %s, want the recorded state %s root %s", derived.StateID, derived.StateRoot, recorded.StateID, recorded.StateRoot)
	}
}

// A layer held as no repository is served from somewhere the measurement
// cannot see. That is reported rather than refused, and reported rather than
// passed over in silence, because a launch nothing contradicted is not the
// same as a launch that was checked.
func TestVerifyLaunchReportsALayerItCannotReDerive(t *testing.T) {
	cp := newTestCpak(t)
	useAnchorLedger(t)
	bindLayer(t, cp, measuredLayer, "state-measured")

	identity, err := cp.verifyLaunch(measuredApplication(measuredLayer), types.Override{}, nil, nil)
	if err != nil {
		t.Fatalf("a layer the store does not hold was reported as a failure: %v", err)
	}
	if len(identity.Unmeasured) != 1 || !strings.Contains(identity.Unmeasured[0], measuredLayer) {
		t.Fatalf("the layer that could not be re-derived was not named: %v", identity.Unmeasured)
	}
	if identity.Verdict != LaunchUnenrolled {
		t.Fatalf("got verdict %s, want a layer that could not be re-derived to decide nothing", identity.Verdict)
	}
}

// seedFVSLayerTree builds a layer wide enough for the cost of measuring it to
// mean something.
func seedFVSLayerTree(tb testing.TB, cp *Cpak, digest string, files int) {
	tb.Helper()
	repository, err := fvsrepo.InitWithOptions(cp.fvsLayerPath(digest), fvsrepo.InitOptions{BlocksPath: cp.fvsBlocksPath()})
	if err != nil {
		tb.Fatal(err)
	}
	writer, err := fvsrepo.BeginSnapshot(repository.Path, fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		tb.Fatal(err)
	}
	for index := range files {
		// Files the size a real layer carries, because a check that reads
		// every byte is only honest about its cost over bytes there are.
		content := fmt.Appendf(make([]byte, 0, layerFileSize), "the content of file %d", index)
		content = append(content, make([]byte, layerFileSize-len(content))...)
		entry := fvsrepo.Entry{
			Path: fmt.Sprintf("usr/share/%03d/file-%05d", index%100, index),
			Kind: fvsrepo.EntryFile,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := writer.Add(entry, bytes.NewReader(content)); err != nil {
			tb.Fatal(err)
		}
	}
	if _, err := writer.Commit(); err != nil {
		tb.Fatal(err)
	}
}

// layerFileSize is what one seeded file holds. It is the median size of a file
// in a distribution image, rounded to a block.
const layerFileSize = 4096

// benchmarkLayerWidths are the widths the cost is reported at. A launch pays
// it once per layer, and the widest prepared checkout on record holds about
// twenty thousand entries, so that is the figure the budget is set against.
var benchmarkLayerWidths = []int{2000, 20000}

// BenchmarkDeriveLayerState is the half of the launch cost that closes the
// record-is-a-claim finding: what re-reading the state of one layer costs.
func BenchmarkDeriveLayerState(b *testing.B) {
	for _, files := range benchmarkLayerWidths {
		b.Run(fmt.Sprintf("%d-files", files), func(b *testing.B) {
			cp := newTestCpak(b)
			seedFVSLayerTree(b, cp, measuredLayer, files)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := cp.deriveLayerState(measuredLayer); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMeasureLaunch is what the gate adds to a launch: the state of every
// layer re-derived, and the prepared checkout of every layer re-measured. The
// budget is a stat walk, and this is where that claim is checked instead of
// asserted.
func BenchmarkMeasureLaunch(b *testing.B) {
	if err := tools.CheckResolveSupport(); err != nil {
		b.Skipf("the restricted resolution is unavailable here: %v", err)
	}
	for _, files := range benchmarkLayerWidths {
		b.Run(fmt.Sprintf("%d-files", files), func(b *testing.B) {
			cp := newTestCpak(b)
			handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
			if err != nil {
				b.Fatal(err)
			}
			cp.storageDriver = handler
			seedFVSLayerTree(b, cp, measuredLayer, files)
			if err := cp.recordLayerBinding(measuredLayer); err != nil {
				b.Fatal(err)
			}
			if _, err := cp.prepareStorageDriver([]string{measuredLayer}); err != nil {
				b.Fatal(err)
			}

			layers := []string{measuredLayer}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				measurement, err := cp.measureLaunch(layers)
				if err != nil {
					b.Fatal(err)
				}
				if len(measurement.Disagreements) > 0 || len(measurement.Unrecorded) > 0 {
					b.Fatalf("an untouched store measured as %d disagreements and %d unrecorded layers",
						len(measurement.Disagreements), len(measurement.Unrecorded))
				}
			}
		})
	}
}

// BenchmarkCheckPreparedCheckoutContents is what reading a whole checkout
// costs, which is the number the decision to keep it off the launch path is
// made against. The files are the width of the widest layer on record at a
// size a real one carries, because the cost is both a syscall per file and a
// hash over every byte.
func BenchmarkCheckPreparedCheckoutContents(b *testing.B) {
	if err := tools.CheckResolveSupport(); err != nil {
		b.Skipf("the restricted resolution is unavailable here: %v", err)
	}
	for _, files := range benchmarkLayerWidths {
		b.Run(fmt.Sprintf("%d-files", files), func(b *testing.B) {
			cp := newTestCpak(b)
			handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
			if err != nil {
				b.Fatal(err)
			}
			cp.storageDriver = handler
			seedFVSLayerTree(b, cp, measuredLayer, files)
			if err = cp.recordLayerBinding(measuredLayer); err != nil {
				b.Fatal(err)
			}
			if _, err = cp.prepareStorageDriver([]string{measuredLayer}); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				check, checkErr := cp.CheckPreparedCheckoutContents(measuredLayer)
				if checkErr != nil {
					b.Fatal(checkErr)
				}
				if !check.Sound() || check.Files != files {
					b.Fatalf("an untouched checkout of %d files read as %d files, %d changed, %d missing",
						files, check.Files, len(check.Changed), len(check.Missing))
				}
			}
		})
	}
}

// BenchmarkBoundCheckoutShape is what this round added to a launch: the shape
// of the prepared checkout derived from the state it was made from, which is
// paid once per layer next to the state root and the walk.
func BenchmarkBoundCheckoutShape(b *testing.B) {
	for _, files := range benchmarkLayerWidths {
		b.Run(fmt.Sprintf("%d-files", files), func(b *testing.B) {
			cp := newTestCpak(b)
			seedFVSLayerTree(b, cp, measuredLayer, files)
			if err := cp.recordLayerBinding(measuredLayer); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := cp.boundCheckoutShape(measuredLayer); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
