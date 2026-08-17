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
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"golang.org/x/sys/unix"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func testAnchorLedger(t *testing.T) AnchorLedger {
	t.Helper()
	return AnchorLedger{
		Directory: filepath.Join(t.TempDir(), "integrity", "v1"),
		OwnerUID:  uint32(os.Getuid()),
	}
}

func testAnchor() integrity.Anchor {
	packageRoot := strings.Repeat("a1", 32)
	policyRoot := strings.Repeat("b2", 32)
	return integrity.Anchor{
		ABI:         integrity.ABIVersion,
		UID:         uint32(os.Getuid()),
		Origin:      "github.com/singularityos-lab/singularity-desktop",
		Generation:  7,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  integrity.LaunchRoot(packageRoot, policyRoot),
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
	if loaded != anchor {
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
		if loaded != anchor {
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
	for name, anchor := range map[string]integrity.Anchor{
		"another abi":                  otherABI,
		"a launch root of its own":     forgedLaunchRoot,
		"a root that is not a digest":  shortRoot,
		"a root that is not lowercase": uppercaseRoot,
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
	if loaded != anchor {
		t.Fatalf("got %+v, want %+v", loaded, anchor)
	}
	older := anchor
	older.Generation = anchor.Generation - 1
	request.Anchor = &older
	err = asDowngrade(requestOverSocket(path, request))
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
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "")
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
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, strings.Repeat("c3", 32), "")
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
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "")
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
		signature := ""
		for _, argument := range method.Args {
			if argument.Direction == "in" {
				signature += argument.Type
			}
		}
		arguments[method.Name] = signature
	}
	for name, want := range map[string]string{
		"EnrolAnchor":    "iustssss",
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
		ABI:         integrity.ABIVersion,
		UID:         uint32(os.Getuid()),
		Origin:      testAnchor().Origin,
		Generation:  generation,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  integrity.LaunchRoot(packageRoot, policyRoot),
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
	if recorded.Anchor != anchor {
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
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, encoded); dbusErr != nil {
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
		widened.Generation, widened.PackageRoot, widened.PolicyRoot, widened.LaunchRoot, encodedWider); dbusErr != nil {
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
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, ""); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionWidenAnchor {
		t.Fatalf("enrolling for another account asked for %s", authorizer.action)
	}
	service.CallerUID = nil
	if dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, ""); dbusErr != nil {
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
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "{not a policy"); dbusErr == nil {
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
	if recorded.Anchor != anchor || recorded.Policy != nil {
		t.Fatalf("the record reads back as %+v", recorded)
	}
}
