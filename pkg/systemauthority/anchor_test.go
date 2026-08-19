/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"golang.org/x/sys/unix"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func testAnchorLedger(t *testing.T) AnchorLedger {
	t.Helper()
	return AnchorLedger{
		Directory: filepath.Join(t.TempDir(), "integrity", "v1"),
		OwnerUID:  uint32(os.Getuid()),
	}
}

// The image and the manifest every anchor in this file describes, and the ones
// every signed state in it covers. A signature is checked against them, so a
// fixture that let them drift apart would be testing the refusal and not the
// thing being refused.
var (
	testImageDigest    = "sha256:" + strings.Repeat("cd", 32)
	testManifestDigest = strings.Repeat("ab", 32)
)

func testAnchor() integrity.Anchor {
	packageRoot := strings.Repeat("a1", 32)
	policyRoot := strings.Repeat("b2", 32)
	return integrity.Anchor{
		ABI:            integrity.ABIVersion,
		UID:            uint32(os.Getuid()),
		Origin:         "github.com/singularityos-lab/singularity-desktop",
		Generation:     7,
		ImageDigest:    testImageDigest,
		ManifestDigest: testManifestDigest,
		PackageRoot:    packageRoot,
		PolicyRoot:     policyRoot,
		LaunchRoot:     integrity.LaunchRoot(packageRoot, policyRoot),
	}
}

func writeAnchorFile(t *testing.T, ledger AnchorLedger, uid uint32, origin string, data []byte) string {
	t.Helper()
	path, err := ledger.anchorPath(uid, origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureDirectory(filepath.Dir(path), ledger.OwnerUID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnchorLedgerRoundTripsAnEnrolment(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("an empty ledger reported %v, %v", found, err)
	}
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := ledger.Load(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the enrolment was not recorded: %v, %v", found, err)
	}
	if !reflect.DeepEqual(loaded, anchor) {
		t.Fatalf("got %+v, want %+v", loaded, anchor)
	}
	if _, found, err := ledger.Load(anchor.UID+1, anchor.Origin); err != nil || found {
		t.Fatalf("an anchor answered for another user: %v, %v", found, err)
	}
	if _, found, err := ledger.Load(anchor.UID, "github.com/example/other"); err != nil || found {
		t.Fatalf("an anchor answered for another origin: %v, %v", found, err)
	}
	if err := ledger.Forget(anchor.UID, anchor.Origin); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the anchor survived its removal: %v, %v", found, err)
	}
	if err := ledger.Forget(anchor.UID, anchor.Origin); err != nil {
		t.Fatalf("forgetting an anchor that is not recorded failed: %v", err)
	}
}

func TestAnchorLedgerRefusesAGenerationDowngrade(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	older := anchor
	older.Generation = anchor.Generation - 1
	err := ledger.Store(older)
	if !errors.Is(err, ErrAnchorDowngrade) {
		t.Fatalf("got %v, want a downgrade refusal", err)
	}
	recorded, _, err := ledger.Load(anchor.UID, anchor.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Generation != anchor.Generation {
		t.Fatalf("the refused enrolment changed the recorded generation to %d", recorded.Generation)
	}
	if err := ledger.Store(anchor); err != nil {
		t.Fatalf("re-enrolling the recorded generation failed: %v", err)
	}
	newer := anchor
	newer.Generation = anchor.Generation + 1
	if err := ledger.Store(newer); err != nil {
		t.Fatalf("a newer generation was refused: %v", err)
	}
}

func TestAnchorLedgerRejectsAnAnchorAnybodyCanWrite(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	path, err := ledger.anchorPath(anchor.UID, anchor.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("a world writable anchor was trusted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor in a world writable directory was trusted")
	}
	if err := ledger.Forget(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor was unlinked from a world writable directory")
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledger.Directory, 0777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor under a world writable ledger root was trusted")
	}
}

func TestAnchorLedgerRejectsAnUnexpectedOwner(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	foreign := ledger
	foreign.OwnerUID = ledger.OwnerUID + 1
	if _, _, err := foreign.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor written by another user was trusted")
	}
}

func TestAnchorLedgerRejectsAMalformedRecord(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	encoded, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	unknownField := append(bytes.TrimSuffix(encoded, []byte("}")), []byte(`,"trusted":true}`)...)
	for name, data := range map[string][]byte{
		"an unknown field":      unknownField,
		"two JSON values":       append(append([]byte{}, encoded...), encoded...),
		"a record over the cap": append(append([]byte{}, encoded...), bytes.Repeat([]byte(" "), anchorSizeLimit)...),
		"no JSON at all":        []byte("not an anchor"),
	} {
		writeAnchorFile(t, ledger, anchor.UID, anchor.Origin, data)
		if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
			t.Fatalf("%s was accepted as an anchor", name)
		}
	}
}

func TestAnchorLedgerRejectsARecordThatDoesNotMatchItsFile(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	encoded, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	other := "github.com/example/other"
	writeAnchorFile(t, ledger, anchor.UID, other, encoded)
	if _, _, err := ledger.Load(anchor.UID, other); err == nil {
		t.Fatal("an anchor was read from the file of another origin")
	}
	writeAnchorFile(t, ledger, anchor.UID+1, anchor.Origin, encoded)
	if _, _, err := ledger.Load(anchor.UID+1, anchor.Origin); err == nil {
		t.Fatal("an anchor was read from the file of another user")
	}
}

func TestAnchorLedgerKeepsAnOriginInsideTheLedger(t *testing.T) {
	root := t.TempDir()
	ledger := AnchorLedger{Directory: filepath.Join(root, "integrity", "v1"), OwnerUID: uint32(os.Getuid())}
	anchor := testAnchor()
	for _, origin := range []string{
		"../../etc/passwd",
		"github.com/../../etc",
		"github.com/owner/../../../escaped",
		"github.com/owner/..",
		"/etc/passwd",
		"github.com/owner",
	} {
		escaping := anchor
		escaping.Origin = origin
		if err := ledger.Store(escaping); err == nil {
			t.Fatalf("%q was accepted as an origin", origin)
		}
		if _, _, err := ledger.Load(escaping.UID, origin); err == nil {
			t.Fatalf("%q was read as an origin", origin)
		}
		if err := ledger.Forget(escaping.UID, origin); err == nil {
			t.Fatalf("%q was removed as an origin", origin)
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return errors.New("a refused origin wrote " + path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnchorFileIsUniqueForEveryOrigin(t *testing.T) {
	ledger := testAnchorLedger(t)
	first := testAnchor()
	first.Origin = "github.com/team_one/desktop"
	second := testAnchor()
	second.Origin = "github.com/team/one_desktop"
	second.Generation = first.Generation + 3
	for _, anchor := range []integrity.Anchor{first, second} {
		if err := ledger.Store(anchor); err != nil {
			t.Fatal(err)
		}
	}
	for _, anchor := range []integrity.Anchor{first, second} {
		loaded, found, err := ledger.Load(anchor.UID, anchor.Origin)
		if err != nil || !found {
			t.Fatalf("%s lost its anchor: %v, %v", anchor.Origin, found, err)
		}
		if !reflect.DeepEqual(loaded, anchor) {
			t.Fatalf("%s reads back as %+v", anchor.Origin, loaded)
		}
	}
}

func TestAnchorLedgerRejectsAnUnusableAnchor(t *testing.T) {
	ledger := testAnchorLedger(t)
	otherABI := testAnchor()
	otherABI.ABI = integrity.ABIVersion + 1
	forgedLaunchRoot := testAnchor()
	forgedLaunchRoot.LaunchRoot = strings.Repeat("c3", 32)
	shortRoot := testAnchor()
	shortRoot.PackageRoot = "a1b2c3"
	uppercaseRoot := testAnchor()
	uppercaseRoot.PolicyRoot = strings.ToUpper(uppercaseRoot.PolicyRoot)
	unprefixedImage := testAnchor()
	unprefixedImage.ImageDigest = strings.Repeat("cd", 32)
	prefixedManifest := testAnchor()
	prefixedManifest.ManifestDigest = "sha256:" + testManifestDigest
	for name, anchor := range map[string]integrity.Anchor{
		"another abi":                     otherABI,
		"a launch root of its own":        forgedLaunchRoot,
		"a root that is not a digest":     shortRoot,
		"a root that is not lowercase":    uppercaseRoot,
		"an image digest with no prefix":  unprefixedImage,
		"a manifest digest with a prefix": prefixedManifest,
	} {
		if err := ledger.Store(anchor); err == nil {
			t.Fatalf("an anchor with %s was enrolled", name)
		}
	}
}

func TestAuthoritySocketEnrolsAnAnchorWithoutABus(t *testing.T) {
	ledger := testAnchorLedger(t)
	path := startAuthoritySocket(t, socketService{
		Anchors:   ledger,
		Authorize: func(*unix.Ucred) error { return nil },
	})
	anchor := testAnchor()
	request := socketRequest{Action: anchorEnrolAction, Anchor: &anchor}
	if err := requestOverSocket(path, request); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := ledger.Load(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the anchor was not enrolled: %v, %v", found, err)
	}
	if !reflect.DeepEqual(loaded, anchor) {
		t.Fatalf("got %+v, want %+v", loaded, anchor)
	}
	older := anchor
	older.Generation = anchor.Generation - 1
	request.Anchor = &older
	err = asRefusal(requestOverSocket(path, request))
	if !errors.Is(err, ErrAnchorDowngrade) {
		t.Fatalf("got %v, want a downgrade the caller can recognise", err)
	}
	if err := requestOverSocket(path, socketRequest{Action: anchorForgetAction, Origin: anchor.Origin, UID: anchor.UID}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the anchor survived its removal: %v, %v", found, err)
	}
}

func TestAuthoritySocketRefusesAnAnchorFromAnUnauthorizedPeer(t *testing.T) {
	ledger := testAnchorLedger(t)
	path := startAuthoritySocket(t, socketService{
		Anchors:   ledger,
		Authorize: func(*unix.Ucred) error { return errors.New("not allowed here") },
	})
	anchor := testAnchor()
	err := requestOverSocket(path, socketRequest{
		Action: anchorEnrolAction, Anchor: &anchor,
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed here") {
		t.Fatalf("the refusal did not reach the caller: %v", err)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
}

func TestAuthoritySocketRejectsAnEnrolmentWithoutAnAnchor(t *testing.T) {
	path := startAuthoritySocket(t, socketService{
		Anchors:   testAnchorLedger(t),
		Authorize: func(*unix.Ucred) error { return nil },
	})
	if err := requestOverSocket(path, socketRequest{Action: anchorEnrolAction, Origin: testAnchor().Origin}); err == nil {
		t.Fatal("an enrolment without an anchor was accepted")
	}
}

func TestServiceAuthorizesEveryAnchorMutation(t *testing.T) {
	ledger := testAnchorLedger(t)
	authorizer := &testAuthorizer{}
	service := testAnchorService(ledger, authorizer)
	anchor := testAnchor()
	dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "")
	if dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionEnrolAnchor || authorizer.details["package-origin"] != anchor.Origin {
		t.Fatalf("unexpected enrolment policy request: %s %#v", authorizer.action, authorizer.details)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || !found {
		t.Fatalf("the authorized enrolment was not recorded: %v, %v", found, err)
	}
	if dbusErr := service.ForgetAnchor(":1.20", anchor.UID, anchor.Origin); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionForgetAnchor {
		t.Fatalf("unexpected removal policy request: %s", authorizer.action)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the anchor survived its removal: %v, %v", found, err)
	}
}

func TestServiceRejectsAnInvalidAnchorBeforeAuthorization(t *testing.T) {
	authorizer := &testAuthorizer{}
	service := testAnchorService(testAnchorLedger(t), authorizer)
	anchor := testAnchor()
	dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, strings.Repeat("c3", 32), "")
	if dbusErr == nil {
		t.Fatal("an anchor with a launch root of its own was accepted")
	}
	if authorizer.action != "" {
		t.Fatal("invalid request reached the authorization service")
	}
}

func TestServiceDenialDoesNotEnrolAnAnchor(t *testing.T) {
	ledger := testAnchorLedger(t)
	service := testAnchorService(ledger, &testAuthorizer{err: errors.New("denied")})
	anchor := testAnchor()
	dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "")
	if dbusErr == nil {
		t.Fatal("authorization denial was ignored")
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("denied enrolment changed the ledger: %v, %v", found, err)
	}
}

// The bus carries plain values, so a method it cannot describe is a method no
// caller can reach. This pins both that the anchor methods are describable and
// the order their arguments travel in.
func TestAnchorMethodsAreCarriedByTheBusInterface(t *testing.T) {
	arguments := map[string]string{}
	for _, method := range introspect.Methods(&Service{}) {
		taken := ""
		for _, argument := range method.Args {
			if argument.Direction == "in" {
				taken += argument.Type
			}
		}
		arguments[method.Name] = taken
	}
	for name, want := range map[string]string{
		"EnrolAnchor":    "iustssssss",
		"ForgetAnchor":   "us",
		"SetEnforcement": "s",
	} {
		if arguments[name] != want {
			t.Fatalf("%s takes %q on the bus, want %q", name, arguments[name], want)
		}
	}
}

func TestAnchorActionsAreDeclaredInThePolkitPolicy(t *testing.T) {
	for _, action := range []string{ActionEnrolAnchor, ActionWidenAnchor, ActionForgetAnchor} {
		declaration := `<action id="` + action + `">`
		if !strings.Contains(string(polkitPolicy), declaration) {
			t.Fatalf("%s is not declared in the polkit policy", action)
		}
	}
}

// testAnchorService is the authority as the bus builds it, with the one thing
// the bus supplies at runtime: the account behind the caller.
func testAnchorService(ledger AnchorLedger, authorizer Authorizer) Service {
	return Service{
		Anchors:    ledger,
		Authorizer: authorizer,
		CallerUID:  func(dbus.Sender) (uint32, error) { return uint32(os.Getuid()), nil },
	}
}

// anchorOver builds the anchor an enrolment of this policy would carry, so the
// policy the authority is handed is the one its policy root was taken over.
func anchorOver(t *testing.T, policy types.Override, packageRoot string, generation uint64) integrity.Anchor {
	t.Helper()
	policyRoot, err := integrity.PolicyRoot(policy)
	if err != nil {
		t.Fatal(err)
	}
	return integrity.Anchor{
		ABI:            integrity.ABIVersion,
		UID:            uint32(os.Getuid()),
		Origin:         testAnchor().Origin,
		Generation:     generation,
		ImageDigest:    testImageDigest,
		ManifestDigest: testManifestDigest,
		PackageRoot:    packageRoot,
		PolicyRoot:     policyRoot,
		LaunchRoot:     integrity.LaunchRoot(packageRoot, policyRoot),
	}
}

// policyMatches compares two policies the way everything else does, by the root
// they hash to, because that is the value the mechanism turns on.
func policyMatches(t *testing.T, got *types.Override, want types.Override) bool {
	t.Helper()
	if got == nil {
		return false
	}
	first, err := integrity.PolicyRoot(*got)
	if err != nil {
		t.Fatal(err)
	}
	second, err := integrity.PolicyRoot(want)
	if err != nil {
		t.Fatal(err)
	}
	return first == second
}

func TestEnrolmentRecordsThePolicyItsRootWasTakenOver(t *testing.T) {
	ledger := testAnchorLedger(t)
	policy := types.Override{SocketWayland: true, Network: true}
	anchor := anchorOver(t, policy, strings.Repeat("a1", 32), 1)
	if err := ledger.Record(Enrolment{Anchor: anchor, Policy: &policy}); err != nil {
		t.Fatal(err)
	}
	recorded, found, err := ledger.Recorded(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the enrolment was not recorded: %v, %v", found, err)
	}
	if !policyMatches(t, recorded.Policy, policy) {
		t.Fatalf("the recorded policy reads back as %+v", recorded.Policy)
	}
	if !reflect.DeepEqual(recorded.Anchor, anchor) {
		t.Fatalf("the recorded anchor reads back as %+v", recorded.Anchor)
	}
	// An update that states no policy must not throw away the one already
	// proven for the same policy root, or the next narrowing becomes a change
	// nobody can order.
	update := anchor
	update.Generation = anchor.Generation + 1
	update.PackageRoot = strings.Repeat("d4", 32)
	update.LaunchRoot = integrity.LaunchRoot(update.PackageRoot, update.PolicyRoot)
	if err := ledger.Store(update); err != nil {
		t.Fatal(err)
	}
	kept, _, err := ledger.Recorded(anchor.UID, anchor.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if !policyMatches(t, kept.Policy, policy) {
		t.Fatalf("the update dropped the recorded policy: %+v", kept.Policy)
	}
}

func TestEnrolmentPolicyMustHashToItsPolicyRoot(t *testing.T) {
	ledger := testAnchorLedger(t)
	policy := types.Override{SocketWayland: true}
	anchor := anchorOver(t, policy, strings.Repeat("a1", 32), 1)
	// The wide policy with the narrow root is the whole attack: it would ask
	// for the ordinary authorization while recording something else.
	wider := policy
	wider.FsHostHome = true
	if err := ledger.Record(Enrolment{Anchor: anchor, Policy: &wider}); err == nil {
		t.Fatal("a policy that does not hash to its policy root was recorded")
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
}

func TestAuthorizationFollowsWhatTheLedgerHolds(t *testing.T) {
	recordedPolicy := types.Override{SocketWayland: true, Network: true}
	narrower := types.Override{SocketWayland: true}
	wider := types.Override{SocketWayland: true, Network: true, FsHostHome: true}
	firstRoot := strings.Repeat("a1", 32)
	secondRoot := strings.Repeat("d4", 32)

	for name, offered := range map[string]struct {
		enrolment Enrolment
		recorded  bool
		want      string
	}{
		"a first install": {
			enrolment: Enrolment{Anchor: anchorOver(t, recordedPolicy, firstRoot, 1), Policy: &recordedPolicy},
			want:      ActionEnrolAnchor,
		},
		"an update that leaves the policy alone": {
			enrolment: Enrolment{Anchor: anchorOver(t, recordedPolicy, secondRoot, 2), Policy: &recordedPolicy},
			recorded:  true,
			want:      ActionEnrolAnchor,
		},
		"an update that narrows the policy": {
			enrolment: Enrolment{Anchor: anchorOver(t, narrower, secondRoot, 2), Policy: &narrower},
			recorded:  true,
			want:      ActionEnrolAnchor,
		},
		"an update that widens the policy": {
			enrolment: Enrolment{Anchor: anchorOver(t, wider, secondRoot, 2), Policy: &wider},
			recorded:  true,
			want:      ActionWidenAnchor,
		},
		"a narrowing nobody stated the policy of": {
			enrolment: Enrolment{Anchor: anchorOver(t, narrower, secondRoot, 2)},
			recorded:  true,
			want:      ActionWidenAnchor,
		},
		"a generation that goes backwards": {
			enrolment: Enrolment{Anchor: anchorOver(t, recordedPolicy, secondRoot, 1), Policy: &recordedPolicy},
			recorded:  true,
			want:      ActionWidenAnchor,
		},
	} {
		ledger := testAnchorLedger(t)
		if offered.recorded {
			held := Enrolment{Anchor: anchorOver(t, recordedPolicy, firstRoot, 2), Policy: &recordedPolicy}
			if err := ledger.Record(held); err != nil {
				t.Fatal(err)
			}
		}
		action, err := ledger.authorizationFor(offered.enrolment)
		if err != nil {
			t.Fatal(err)
		}
		if action != offered.want {
			t.Fatalf("%s asks for %s, want %s", name, action, offered.want)
		}
	}
}

func TestServiceAsksTheOwnerOnlyWhenAnEnrolmentWidens(t *testing.T) {
	ledger := testAnchorLedger(t)
	authorizer := &testAuthorizer{}
	service := testAnchorService(ledger, authorizer)
	policy := types.Override{SocketWayland: true}
	anchor := anchorOver(t, policy, strings.Repeat("a1", 32), 1)
	encoded, err := encodePolicy(&policy)
	if err != nil {
		t.Fatal(err)
	}
	if dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, encoded); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionEnrolAnchor {
		t.Fatalf("a first install asked for %s", authorizer.action)
	}
	wider := policy
	wider.FsHostHome = true
	widened := anchorOver(t, wider, strings.Repeat("d4", 32), 2)
	encodedWider, err := encodePolicy(&wider)
	if err != nil {
		t.Fatal(err)
	}
	if dbusErr := service.EnrolAnchor(":1.20", int32(widened.ABI), widened.UID, widened.Origin,
		widened.Generation, widened.ImageDigest, widened.ManifestDigest, widened.PackageRoot, widened.PolicyRoot, widened.LaunchRoot, encodedWider); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionWidenAnchor {
		t.Fatalf("an update that widens the policy asked for %s", authorizer.action)
	}
	recorded, _, err := ledger.Recorded(widened.UID, widened.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if !policyMatches(t, recorded.Policy, wider) {
		t.Fatalf("the authorized widening recorded %+v", recorded.Policy)
	}
}

func TestServiceTreatsAnEnrolmentForAnotherAccountAsWidening(t *testing.T) {
	authorizer := &testAuthorizer{}
	service := testAnchorService(testAnchorLedger(t), authorizer)
	anchor := testAnchor()
	anchor.UID++
	if dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, ""); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionWidenAnchor {
		t.Fatalf("enrolling for another account asked for %s", authorizer.action)
	}
	service.CallerUID = nil
	if dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, ""); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionWidenAnchor {
		t.Fatalf("a caller nobody can name asked for %s", authorizer.action)
	}
}

func TestServiceRejectsAPolicyItCannotDecode(t *testing.T) {
	authorizer := &testAuthorizer{}
	service := testAnchorService(testAnchorLedger(t), authorizer)
	anchor := testAnchor()
	if dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "{not a policy"); dbusErr == nil {
		t.Fatal("a policy that is not JSON was accepted")
	}
	if authorizer.action != "" {
		t.Fatal("invalid request reached the authorization service")
	}
}

// policyDefaults reads the shipped polkit actions. What a desktop user is asked
// is decided here and nowhere in the code, so it is pinned here.
func policyDefaults(t *testing.T) map[string][3]string {
	t.Helper()
	document := struct {
		Actions []struct {
			ID       string `xml:"id,attr"`
			Defaults struct {
				Any      string `xml:"allow_any"`
				Inactive string `xml:"allow_inactive"`
				Active   string `xml:"allow_active"`
			} `xml:"defaults"`
		} `xml:"action"`
	}{}
	if err := xml.Unmarshal(polkitPolicy, &document); err != nil {
		t.Fatalf("the shipped polkit policy does not parse: %v", err)
	}
	defaults := map[string][3]string{}
	for _, action := range document.Actions {
		defaults[action.ID] = [3]string{action.Defaults.Any, action.Defaults.Inactive, action.Defaults.Active}
	}
	return defaults
}

func TestAnchorActionsAskTheDesktopUserTheRightQuestion(t *testing.T) {
	defaults := policyDefaults(t)
	// Forgetting an anchor is not a widening: it leaves the application in the
	// state enforcement refuses, so an active session may do it while removing
	// software rather than being asked for an administrator password.
	for action, want := range map[string][3]string{
		ActionEnrolAnchor:  {"no", "no", "yes"},
		ActionWidenAnchor:  {"no", "no", "auth_admin"},
		ActionForgetAnchor: {"no", "no", "yes"},
	} {
		if defaults[action] != want {
			t.Fatalf("%s is declared as %v, want %v", action, defaults[action], want)
		}
	}
}

// The record is flat on purpose: the anchor fields sit at the top level, so a
// file written when nobody stated a policy is still a whole enrolment and not a
// record from another format.
func TestARecordWithoutAPolicyIsStillAnEnrolment(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	encoded, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	writeAnchorFile(t, ledger, anchor.UID, anchor.Origin, encoded)
	recorded, found, err := ledger.Recorded(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("an anchor with no policy beside it was not read: %v, %v", found, err)
	}
	if !reflect.DeepEqual(recorded.Anchor, anchor) || recorded.Policy != nil {
		t.Fatalf("the record reads back as %+v", recorded)
	}
}

// Everything below is the publisher signature: the evidence an enrolment
// carries, what the authority does with it, and the one prompt it changes.
//
// No test here can produce a bundle the shipped trust root accepts, because
// that needs a certificate Fulcio issued against a real CI token. So the
// offline check is driven, and two things are pinned separately so that a
// driven answer can never stand in for no answer at all: that the default
// checker is the real one, and that the real one refuses what these tests
// hand it.

func testSignatureIdentity(repo string) signature.Identity {
	return signature.Identity{
		Issuer:  "https://token.actions.githubusercontent.com",
		Subject: "https://" + repo + "/.github/workflows/release.yml@refs/heads/main",
		Repo:    repo,
	}
}

func testSignedState(generation uint64) *SignedState {
	return &SignedState{
		State: signature.State{
			ABI:            signature.ABIVersion,
			Origin:         testAnchor().Origin,
			ManifestSHA256: testManifestDigest,
			ImageDigest:    testImageDigest,
			Generation:     generation,
		},
		Bundle: []byte(`{"bundle":"what the publisher attached"}`),
	}
}

// useBundleVerifier drives the answer the authority acts on and puts the real
// one back when the test ends.
func useBundleVerifier(t *testing.T, verify func([]byte, signature.State) (signature.Verified, error)) {
	t.Helper()

	saved := verifyBundle
	t.Cleanup(func() { verifyBundle = saved })
	verifyBundle = verify
}

func acceptSignaturesOf(t *testing.T, repo string) {
	t.Helper()

	useBundleVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
		return signature.Verified{State: state, Identity: testSignatureIdentity(repo)}, nil
	})
}

// The test that stops every other test in this file from proving nothing. The
// authority checks a bundle with pkg/signature and with nothing else, and the
// real check refuses the bundles these tests are built out of.
func TestTheAuthorityChecksBundlesWithTheRealVerifier(t *testing.T) {
	if reflect.ValueOf(verifyBundle).Pointer() != reflect.ValueOf(separatedVerify).Pointer() {
		t.Fatal("the authority does not put bundles through the separated verifier")
	}
	if reflect.ValueOf(verifyDirect).Pointer() != reflect.ValueOf(signature.Verify).Pointer() {
		t.Fatal("the separated verifier ends at something other than pkg/signature")
	}
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)})
	if err == nil {
		t.Fatal("the real verifier accepted a bundle nobody signed")
	}
	if errors.Is(err, ErrUnsigned) {
		t.Fatalf("a bundle that does not stand was reported as no bundle at all: %v", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
}

// What a signed enrolment is worth to a reader: the bundle itself, so that
// whoever reads the ledger afterwards proves it instead of believing the
// authority, on a machine that may have no network at all.
func TestASignedEnrolmentRecordsTheBundleAndTheState(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	signed := testSignedState(3)
	acceptSignaturesOf(t, anchor.Origin)

	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: signed}); err != nil {
		t.Fatal(err)
	}
	recorded, found, err := ledger.Recorded(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the signed enrolment was not recorded: %v, %v", found, err)
	}
	if recorded.Signature == nil {
		t.Fatal("the record holds no signature for an enrolment that carried one")
	}
	if recorded.Signature.State != signed.State {
		t.Fatalf("the record holds state %+v, want %+v", recorded.Signature.State, signed.State)
	}
	if !bytes.Equal(recorded.Signature.Bundle, signed.Bundle) {
		t.Fatalf("the record holds bundle %q, want the one that was enrolled", recorded.Signature.Bundle)
	}
	signer, err := recorded.Signer()
	if err != nil {
		t.Fatalf("the recorded signature does not read back as one: %v", err)
	}
	if signer.Identity.Repo != anchor.Origin {
		t.Fatalf("the recorded signature reads back as made by %q", signer.Identity.Repo)
	}
}

// A bundle that does not stand is never written down. A record naming a signer
// nobody can prove would be read as provenance by everything downstream of it.
func TestASignatureThatDoesNotStandIsNeverRecorded(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	useBundleVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("no transparency log holds this")
	})

	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err == nil {
		t.Fatal("a bundle that does not stand was recorded")
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
	// The same enrolment, the same bundle, an offline check that accepts it:
	// the refusal above is about the answer and not about the record.
	acceptSignaturesOf(t, anchor.Origin)
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err != nil {
		t.Fatalf("the same enrolment was refused when its signature stood: %v", err)
	}
}

// A signature that checks out and was made by somebody who may not speak for
// this origin is the one failure that says the artifact is authentic and still
// not the publisher's. The comparison is the real one.
func TestASignatureFromAnotherIdentityIsNeverRecorded(t *testing.T) {
	anchor := testAnchor()
	for name, identity := range map[string]signature.Identity{
		"another repository": testSignatureIdentity("github.com/attacker/singularity-desktop"),
		"a lookalike owner":  testSignatureIdentity("github.com/singularityos-lab-inc/singularity-desktop"),
		"another issuer":     {Issuer: "https://accounts.google.com", Repo: anchor.Origin},
		"nobody at all":      {},
	} {
		ledger := testAnchorLedger(t)
		useBundleVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
			return signature.Verified{State: state, Identity: identity}, nil
		})
		if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err == nil {
			t.Fatalf("%s was recorded as the publisher of %s", name, anchor.Origin)
		}
		if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
			t.Fatalf("%s reached the ledger: %v, %v", name, found, err)
		}
	}
}

// A bundle covering somebody else's package can never be filed under this
// application, whoever signed it.
func TestASignedStateMustNameTheApplicationItIsFiledUnder(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	acceptSignaturesOf(t, anchor.Origin)
	signed := testSignedState(1)
	signed.State.Origin = "github.com/example/other"

	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: signed}); err == nil {
		t.Fatal("a signed state about another package was recorded")
	}
}

// The host policy. Nothing changes for anybody until an administrator sets it,
// and once it is set an application no publisher signed is not enrolled at all.
func TestARequiredSignatureDecidesWhetherAnEnrolmentIsRecorded(t *testing.T) {
	ledger := testAnchorLedger(t)
	settings := EnforcementStore{Directory: ledger.Directory, OwnerUID: ledger.OwnerUID}
	anchor := testAnchor()
	if err := ledger.Store(anchor); err != nil {
		t.Fatalf("a host that set no policy refused an unsigned enrolment: %v", err)
	}
	if err := ledger.Forget(anchor.UID, anchor.Origin); err != nil {
		t.Fatal(err)
	}

	if err := settings.SetSignaturePolicy(SignaturesRequired); err != nil {
		t.Fatal(err)
	}
	err := ledger.Store(anchor)
	if !errors.Is(err, ErrSignatureRequired) {
		t.Fatalf("got %v, want a host that requires signatures to refuse an unsigned enrolment", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}

	acceptSignaturesOf(t, anchor.Origin)
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err != nil {
		t.Fatalf("a signed enrolment was refused on a host that requires signatures: %v", err)
	}
}

// The prompt a signature does not change. A publisher signs the origin, the
// manifest and the image, and none of those is the policy being enrolled: the
// policy is the user's own override whenever they set one. So a counter that
// moved forward is the publisher shipping a release and never the publisher
// agreeing to what this host would hand it.
func TestEveryWideningIsPutToTheOwnerWhoeverSignedIt(t *testing.T) {
	recordedPolicy := types.Override{SocketWayland: true}
	// Nothing a publisher ships asks for this. It is the widening a local
	// override can state, and the one the owner of the machine has to see.
	wider := types.Override{SocketWayland: true, FsHost: true, AsRoot: true}
	firstRoot := strings.Repeat("a1", 32)
	secondRoot := strings.Repeat("d4", 32)
	origin := testAnchor().Origin

	for name, offered := range map[string]struct {
		held      *SignedState
		signature *SignedState
		identity  signature.Identity
		stands    bool
	}{
		"a counter that moved over an unchanged manifest and image": {
			held:      testSignedState(4),
			signature: testSignedState(5),
			identity:  testSignatureIdentity(origin),
			stands:    true,
		},
		"a counter that moved no further than the one on record": {
			held:      testSignedState(5),
			signature: testSignedState(5),
			identity:  testSignatureIdentity(origin),
			stands:    true,
		},
		"a widening nobody signed": {
			held:     testSignedState(4),
			identity: testSignatureIdentity(origin),
			stands:   true,
		},
		"a signed widening of an application nobody had signed": {
			signature: testSignedState(5),
			identity:  testSignatureIdentity(origin),
			stands:    true,
		},
		"a widening signed by somebody else": {
			held:      testSignedState(4),
			signature: testSignedState(5),
			identity:  testSignatureIdentity("github.com/attacker/singularity-desktop"),
			stands:    true,
		},
		"a widening whose signature does not stand": {
			held:      testSignedState(4),
			signature: testSignedState(5),
			identity:  testSignatureIdentity(origin),
		},
	} {
		ledger := testAnchorLedger(t)
		// The record is written while every signature stands, so what the
		// case is about is the offer and never the ability to record.
		acceptSignaturesOf(t, origin)
		held := Enrolment{Anchor: anchorOver(t, recordedPolicy, firstRoot, 2), Policy: &recordedPolicy, Signature: offered.held}
		if err := ledger.Record(held); err != nil {
			t.Fatal(err)
		}
		useBundleVerifier(t, func(_ []byte, state signature.State) (signature.Verified, error) {
			if !offered.stands {
				return signature.Verified{}, errors.New("no transparency log holds this")
			}
			if offered.signature != nil && state == offered.signature.State {
				return signature.Verified{State: state, Identity: offered.identity}, nil
			}
			return signature.Verified{State: state, Identity: testSignatureIdentity(origin)}, nil
		})
		widening := Enrolment{Anchor: anchorOver(t, wider, secondRoot, 3), Policy: &wider, Signature: offered.signature}
		action, err := ledger.authorizationFor(widening)
		if err != nil {
			t.Fatal(err)
		}
		if action != ActionWidenAnchor {
			t.Fatalf("%s asks for %s, want %s", name, action, ActionWidenAnchor)
		}
	}
}

// The other half of the same rule: a signature changes nothing about an update
// that does not widen, so an application whose publisher signs it is not asked
// about more often than one whose publisher does not.
func TestASignatureDoesNotMakeAnOrdinaryUpdateHarder(t *testing.T) {
	policy := types.Override{SocketWayland: true, Network: true}
	narrower := types.Override{SocketWayland: true}
	origin := testAnchor().Origin
	for name, offered := range map[string]types.Override{
		"an update that leaves the policy alone": policy,
		"an update that narrows the policy":      narrower,
	} {
		ledger := testAnchorLedger(t)
		acceptSignaturesOf(t, origin)
		held := Enrolment{Anchor: anchorOver(t, policy, strings.Repeat("a1", 32), 2), Policy: &policy, Signature: testSignedState(4)}
		if err := ledger.Record(held); err != nil {
			t.Fatal(err)
		}
		update := Enrolment{Anchor: anchorOver(t, offered, strings.Repeat("d4", 32), 3), Policy: &offered, Signature: testSignedState(5)}
		action, err := ledger.authorizationFor(update)
		if err != nil {
			t.Fatal(err)
		}
		if action != ActionEnrolAnchor {
			t.Fatalf("%s asks for %s, want %s", name, action, ActionEnrolAnchor)
		}
	}
}

// A record is still readable when its bundle is not. The ledger is what every
// launch on the host reads, so a trust root that moved on must cost a report
// its verdict and never cost an application its anchor.
func TestARecordSurvivesASignatureThatStoppedStanding(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	acceptSignaturesOf(t, anchor.Origin)
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err != nil {
		t.Fatal(err)
	}

	useBundleVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("this certificate chains to nothing this host trusts")
	})
	loaded, found, err := ledger.Load(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("a launch could not read the anchor of an application whose signature stopped standing: %v, %v", found, err)
	}
	if !reflect.DeepEqual(loaded, anchor) {
		t.Fatalf("the anchor reads back as %+v", loaded)
	}
	recorded, _, err := ledger.Recorded(anchor.UID, anchor.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorded.Signer(); err == nil {
		t.Fatal("a signature that stopped standing still reads as one")
	}
}

// The bus is how an unprivileged install reaches the authority, so the
// signature has to travel it, and what travels has to be described.
func TestTheSignedEnrolmentIsCarriedByTheBusInterface(t *testing.T) {
	arguments := map[string]string{}
	for _, method := range introspect.Methods(&Service{}) {
		taken := ""
		for _, argument := range method.Args {
			if argument.Direction == "in" {
				taken += argument.Type
			}
		}
		arguments[method.Name] = taken
	}
	for name, want := range map[string]string{
		"EnrolSignedAnchor":  "iustssssssss",
		"SetSignaturePolicy": "s",
		"EnrolAnchor":        "iustssssss",
	} {
		if arguments[name] != want {
			t.Fatalf("%s takes %q on the bus, want %q", name, arguments[name], want)
		}
	}
}

func TestTheServiceRecordsASignedEnrolment(t *testing.T) {
	ledger := testAnchorLedger(t)
	authorizer := &testAuthorizer{}
	service := testAnchorService(ledger, authorizer)
	anchor := testAnchor()
	signed := testSignedState(2)
	acceptSignaturesOf(t, anchor.Origin)
	state, err := json.Marshal(signed.State)
	if err != nil {
		t.Fatal(err)
	}

	dbusErr := service.EnrolSignedAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest,
		anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "",
		string(state), string(signed.Bundle))
	if dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionEnrolAnchor {
		t.Fatalf("a first signed install asked for %s", authorizer.action)
	}
	recorded, found, err := ledger.Recorded(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the signed enrolment was not recorded: %v, %v", found, err)
	}
	if recorded.Signature == nil || !bytes.Equal(recorded.Signature.Bundle, signed.Bundle) {
		t.Fatalf("the record holds %+v, want the bundle that crossed the bus", recorded.Signature)
	}
	// The bus has to carry what the bundle is checked against as well, or the
	// authority would be checking it against an anchor that describes nothing.
	if recorded.ImageDigest != anchor.ImageDigest || recorded.ManifestDigest != anchor.ManifestDigest {
		t.Fatalf("the record names image %q and manifest %q", recorded.ImageDigest, recorded.ManifestDigest)
	}
}

// A bundle that does not stand is refused before anybody is asked for a
// password, because the enrolment could not be recorded whatever the answer.
func TestTheServiceRefusesASignatureThatDoesNotStandBeforeAuthorization(t *testing.T) {
	ledger := testAnchorLedger(t)
	authorizer := &testAuthorizer{}
	service := testAnchorService(ledger, authorizer)
	anchor := testAnchor()
	signed := testSignedState(2)
	useBundleVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("no transparency log holds this")
	})
	state, err := json.Marshal(signed.State)
	if err != nil {
		t.Fatal(err)
	}

	dbusErr := service.EnrolSignedAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest,
		anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "",
		string(state), string(signed.Bundle))
	if dbusErr == nil {
		t.Fatal("a signature that does not stand was accepted")
	}
	if authorizer.action != "" {
		t.Fatal("a signature that does not stand reached the authorization service")
	}
}

func TestTheSignaturePolicyActionAsksTheOwnerOfTheMachine(t *testing.T) {
	defaults := policyDefaults(t)
	want := [3]string{"no", "no", "auth_admin"}
	if defaults[ActionSetSignaturePolicy] != want {
		t.Fatalf("%s is declared as %v, want %v", ActionSetSignaturePolicy, defaults[ActionSetSignaturePolicy], want)
	}
}

// The host policy is held beside the enforcement level and proven the same
// way, so it is pinned the same way: a host that never set it behaves as it
// always did, and a file nobody can vouch for never decides anything.

func TestTheSignaturePolicyIsOptionalUntilItIsSet(t *testing.T) {
	store := testEnforcementStore(t)
	policy, err := store.SignaturePolicy()
	if err != nil {
		t.Fatalf("a host that never set a policy answered %v", err)
	}
	if policy != SignaturesOptional {
		t.Fatalf("a host that never set a policy enrols %s signatures", policy)
	}
	for _, want := range []SignaturePolicy{SignaturesRequired, SignaturesOptional} {
		if err := store.SetSignaturePolicy(want); err != nil {
			t.Fatal(err)
		}
		policy, err := store.SignaturePolicy()
		if err != nil || policy != want {
			t.Fatalf("the policy reads back as %s, %v", policy, err)
		}
	}
	// The two settings are separate files, so setting one must not answer for
	// the other.
	if err := store.Set(EnforcementRefuse); err != nil {
		t.Fatal(err)
	}
	if policy, err := store.SignaturePolicy(); err != nil || policy != SignaturesOptional {
		t.Fatalf("the enforcement level moved the signature policy to %s, %v", policy, err)
	}
}

func TestTheSignaturePolicyRejectsWhatItCannotTrust(t *testing.T) {
	store := testEnforcementStore(t)
	if err := store.SetSignaturePolicy(SignaturePolicy("sometimes")); err == nil {
		t.Fatal("a policy nobody defines was written")
	}
	if err := store.SetSignaturePolicy(SignaturesRequired); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, signaturePolicyFileName)
	if err := os.WriteFile(path, []byte("required, mostly"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SignaturePolicy(); err == nil {
		t.Fatal("a policy nobody defines was read")
	}
	if err := store.SetSignaturePolicy(SignaturesRequired); err != nil {
		t.Fatal(err)
	}
	// The mode is changed rather than written, because a umask decides what a
	// mode passed to a create becomes and this has to be world writable.
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SignaturePolicy(); err == nil {
		t.Fatal("a world writable policy was trusted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	foreign := store
	foreign.OwnerUID = store.OwnerUID + 1
	if _, err := foreign.SignaturePolicy(); err == nil {
		t.Fatal("a policy written by another user was trusted")
	}
}

// The policy decides whether an installation is enrolled, so the account doing
// the installing must have no way to state it.
func TestTheSignaturePolicyIsNeverTakenFromTheEnvironment(t *testing.T) {
	recorded, err := DefaultEnforcementStore().SignaturePolicy()
	if err != nil {
		t.Skipf("the signature policy of this host cannot be read here: %v", err)
	}
	for _, name := range []string{"CPAK_SIGNATURES", "CPAK_SIGNATURE_POLICY", "SIGNATURES"} {
		t.Setenv(name, string(SignaturesOptional))
	}
	if policy := Signatures(); policy != recorded {
		t.Fatalf("the environment moved the signature policy to %s", policy)
	}
}

func TestServiceAuthorizesEverySignaturePolicyChange(t *testing.T) {
	store := testEnforcementStore(t)
	authorizer := &testAuthorizer{}
	service := Service{Enforcement: store, Authorizer: authorizer}
	if dbusErr := service.SetSignaturePolicy(":1.20", string(SignaturesRequired)); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionSetSignaturePolicy {
		t.Fatalf("changing the signature policy asked for %s", authorizer.action)
	}
	policy, err := store.SignaturePolicy()
	if err != nil || policy != SignaturesRequired {
		t.Fatalf("the authorized policy reads back as %s, %v", policy, err)
	}

	denying := Service{Enforcement: testEnforcementStore(t), Authorizer: &testAuthorizer{err: errors.New("denied")}}
	if dbusErr := denying.SetSignaturePolicy(":1.20", string(SignaturesRequired)); dbusErr == nil {
		t.Fatal("authorization denial was ignored")
	}
	if policy, err := denying.Enforcement.SignaturePolicy(); err != nil || policy != SignaturesOptional {
		t.Fatalf("a denied change left the policy at %s, %v", policy, err)
	}
}

func TestServiceRejectsAPolicyItDoesNotKnowBeforeAuthorization(t *testing.T) {
	authorizer := &testAuthorizer{}
	service := Service{Enforcement: testEnforcementStore(t), Authorizer: authorizer}
	if dbusErr := service.SetSignaturePolicy(":1.20", "sometimes"); dbusErr == nil {
		t.Fatal("a policy nobody defines was accepted")
	}
	if authorizer.action != "" {
		t.Fatal("invalid request reached the authorization service")
	}
}

// What makes a signature a binding rather than a label. A bundle that verifies
// and names this origin is not evidence about this installation unless it also
// names the image it is made of and the manifest it is configured by.

func TestASignedStateMustCoverWhatTheAnchorDescribes(t *testing.T) {
	origin := testAnchor().Origin
	other := strings.Repeat("ef", 32)
	for name, break_ := range map[string]func(*Enrolment){
		"an anchor that names no image":      func(e *Enrolment) { e.ImageDigest = "" },
		"an anchor that names no manifest":   func(e *Enrolment) { e.ManifestDigest = "" },
		"a bundle covering another image":    func(e *Enrolment) { e.Signature.State.ImageDigest = "sha256:" + other },
		"a bundle covering another manifest": func(e *Enrolment) { e.Signature.State.ManifestSHA256 = other },
	} {
		ledger := testAnchorLedger(t)
		acceptSignaturesOf(t, origin)
		enrolment := Enrolment{Anchor: testAnchor(), Signature: testSignedState(1)}
		break_(&enrolment)
		if err := ledger.Record(enrolment); err == nil {
			t.Fatalf("%s was recorded as the provenance of this launch", name)
		}
		if _, found, err := ledger.Recorded(enrolment.UID, origin); err != nil || found {
			t.Fatalf("%s reached the ledger: %v, %v", name, found, err)
		}
	}
	// The same enrolment with the anchor and the state naming one package: the
	// refusals above are about the binding and not about the record.
	ledger := testAnchorLedger(t)
	acceptSignaturesOf(t, origin)
	if err := ledger.Record(Enrolment{Anchor: testAnchor(), Signature: testSignedState(1)}); err != nil {
		t.Fatalf("a state naming the image and the manifest of its anchor was refused: %v", err)
	}
}

// The attack the binding exists for: a genuine bundle, made by the publisher,
// for a release of this origin that is not the one on disk.
func TestABundleForAnotherReleaseOfTheSameOriginIsRefused(t *testing.T) {
	ledger := testAnchorLedger(t)
	origin := testAnchor().Origin
	policy := types.Override{SocketWayland: true}
	acceptSignaturesOf(t, origin)
	first := anchorOver(t, policy, strings.Repeat("a1", 32), 1)
	if err := ledger.Record(Enrolment{Anchor: first, Policy: &policy, Signature: testSignedState(1)}); err != nil {
		t.Fatal(err)
	}
	// A second installation made of another image. The bundle above is
	// genuine, verifies, and says nothing whatever about this one.
	second := anchorOver(t, policy, strings.Repeat("d4", 32), 2)
	second.ImageDigest = "sha256:" + strings.Repeat("ef", 32)
	err := ledger.Record(Enrolment{Anchor: second, Policy: &policy, Signature: testSignedState(2)})
	if err == nil {
		t.Fatal("a bundle for one image was recorded as the provenance of another")
	}
	recorded, _, err := ledger.Recorded(first.UID, origin)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.ImageDigest != first.ImageDigest {
		t.Fatalf("the refused enrolment changed the recorded image to %s", recorded.ImageDigest)
	}
}

// The publisher counter is ordered like the anchor's own, because an old
// bundle is a genuine bundle and nothing about it fails to verify.
func TestAnOlderPublisherGenerationIsRefused(t *testing.T) {
	ledger := testAnchorLedger(t)
	origin := testAnchor().Origin
	policy := types.Override{SocketWayland: true}
	acceptSignaturesOf(t, origin)
	held := Enrolment{Anchor: anchorOver(t, policy, strings.Repeat("a1", 32), 1), Policy: &policy, Signature: testSignedState(5)}
	if err := ledger.Record(held); err != nil {
		t.Fatal(err)
	}
	replay := Enrolment{Anchor: anchorOver(t, policy, strings.Repeat("d4", 32), 2), Policy: &policy, Signature: testSignedState(4)}
	err := ledger.Record(replay)
	if !errors.Is(err, ErrSignatureDowngrade) {
		t.Fatalf("got %v, want the replay of an older signed state to be refused", err)
	}
	recorded, _, err := ledger.Recorded(held.UID, origin)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Signature.State.Generation != 5 {
		t.Fatalf("the refused enrolment moved the recorded generation to %d", recorded.Signature.State.Generation)
	}
	// Re-recording what is held is not going backwards, and neither is moving
	// forward: only a counter that has already been left is refused.
	same := held
	same.Signature = testSignedState(5)
	if err := ledger.Record(same); err != nil {
		t.Fatalf("re-recording the signed state on record failed: %v", err)
	}
	forward := Enrolment{Anchor: anchorOver(t, policy, strings.Repeat("d4", 32), 2), Policy: &policy, Signature: testSignedState(6)}
	if err := ledger.Record(forward); err != nil {
		t.Fatalf("a later signed state was refused: %v", err)
	}
}

// A signed application does not quietly become an unsigned one. The record is
// the only place that fact is kept, so it is kept by refusing to lose it.
func TestAnEnrolmentCannotDropTheSignatureOnRecord(t *testing.T) {
	ledger := testAnchorLedger(t)
	origin := testAnchor().Origin
	policy := types.Override{SocketWayland: true}
	acceptSignaturesOf(t, origin)
	held := Enrolment{Anchor: anchorOver(t, policy, strings.Repeat("a1", 32), 1), Policy: &policy, Signature: testSignedState(3)}
	if err := ledger.Record(held); err != nil {
		t.Fatal(err)
	}
	unsigned := Enrolment{Anchor: anchorOver(t, policy, strings.Repeat("d4", 32), 2), Policy: &policy}
	err := ledger.Record(unsigned)
	if !errors.Is(err, ErrSignatureLost) {
		t.Fatalf("got %v, want an unsigned enrolment over a signed one to be refused", err)
	}
	recorded, found, err := ledger.Recorded(held.UID, origin)
	if err != nil || !found {
		t.Fatalf("the refused enrolment took the record with it: %v, %v", found, err)
	}
	if recorded.Signature == nil || recorded.Signature.State.Generation != 3 {
		t.Fatalf("the record now holds %+v", recorded.Signature)
	}
	// The fact is read off the record and never re-proven, so a trust root
	// that moved on cannot turn a signed application into one anybody may
	// re-enrol unsigned.
	useBundleVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("this certificate chains to nothing this host trusts")
	})
	if err := ledger.Record(unsigned); !errors.Is(err, ErrSignatureLost) {
		t.Fatalf("got %v, want a signature that stopped standing to still be a signature on record", err)
	}
	// An update that carries one is the way forward, and it is open.
	acceptSignaturesOf(t, origin)
	next := unsigned
	next.Signature = testSignedState(4)
	if err := ledger.Record(next); err != nil {
		t.Fatalf("a signed update of a signed application was refused: %v", err)
	}
}

// Every refusal a caller has to act on differently has to survive a transport,
// where an error is only its own text.
func TestTheRefusalsACallerActsOnSurviveATransport(t *testing.T) {
	for _, refusal := range []error{ErrAnchorDowngrade, ErrSignatureDowngrade, ErrSignatureLost} {
		crossed := errors.New("it.cpak.system.Error.Failed: " + refusal.Error() + ": recorded 5, offered 4")
		if !errors.Is(asRefusal(crossed), refusal) {
			t.Fatalf("%v does not read back as itself after a transport", refusal)
		}
	}
	if errors.Is(asRefusal(errors.New("the authority is not running")), ErrAnchorDowngrade) {
		t.Fatal("an unrelated failure reads back as a downgrade")
	}
	if asRefusal(nil) != nil {
		t.Fatal("a transport that succeeded reads back as a refusal")
	}
}

// The anchor and the signed state are compared field for field, so the shape
// each of them allows has to be the same shape. Two packages that disagreed
// about how a digest is written would refuse every signature ever made, and
// would refuse it as a package no publisher signed.
func TestTheAnchorAndTheSignedStateAgreeOnHowADigestIsWritten(t *testing.T) {
	anchor := testAnchor()
	state := signature.State{
		ABI:            signature.ABIVersion,
		Origin:         anchor.Origin,
		ManifestSHA256: anchor.ManifestDigest,
		ImageDigest:    anchor.ImageDigest,
		Generation:     1,
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("the digests an anchor accepts are not the digests a state accepts: %v", err)
	}
	if err := anchor.ValidateDigests(); err != nil {
		t.Fatalf("the digests a state accepts are not the digests an anchor accepts: %v", err)
	}
}
