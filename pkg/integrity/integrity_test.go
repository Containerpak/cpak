/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package integrity

import (
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func boundPackage() Package {
	return Package{
		Origin:         "github.com/bottlesdevs/bottles",
		Selector:       "branch:main",
		Version:        "66.6",
		ManifestDigest: "sha256:manifest",
		ImageDigest:    "sha256:image",
		ConfigDigest:   "sha256:config",
		Layers: []LayerBinding{
			{OCIDigest: "sha256:base", StateID: "state-base", StateRoot: "root-base"},
			{OCIDigest: "sha256:app", StateID: "state-app", StateRoot: "root-app"},
		},
		Binaries:       []string{"/usr/bin/bottles", "/usr/bin/bottles-cli"},
		DesktopEntries: []string{"/usr/share/applications/com.usebottles.bottles.desktop"},
	}
}

func TestPackageRootIsStableAcrossSetOrder(t *testing.T) {
	first, err := boundPackage().Root()
	if err != nil {
		t.Fatal(err)
	}
	shuffled := boundPackage()
	shuffled.Binaries = []string{"/usr/bin/bottles-cli", "/usr/bin/bottles"}
	second, err := shuffled.Root()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("the root changed although only the order of a set changed")
	}
}

// Layer order is the overlay order, so it is identity, not a set.
func TestPackageRootFollowsLayerOrder(t *testing.T) {
	first, err := boundPackage().Root()
	if err != nil {
		t.Fatal(err)
	}
	swapped := boundPackage()
	swapped.Layers[0], swapped.Layers[1] = swapped.Layers[1], swapped.Layers[0]
	second, err := swapped.Root()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two different overlay orders produced the same root")
	}
}

func TestPackageRootRefusesAnUnboundLayer(t *testing.T) {
	unbound := boundPackage()
	unbound.Layers[1].StateRoot = ""
	if _, err := unbound.Root(); err == nil {
		t.Fatal("a layer with no store state was accepted into the root")
	}
}

func TestPackageRootNoticesAChangedLayerState(t *testing.T) {
	first, err := boundPackage().Root()
	if err != nil {
		t.Fatal(err)
	}
	tampered := boundPackage()
	tampered.Layers[1].StateRoot = "root-app-modified"
	second, err := tampered.Root()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the same root covers two different layer contents")
	}
}

func TestLaunchRootSeparatesIdentityFromPolicy(t *testing.T) {
	packageRoot, err := boundPackage().Root()
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := PolicyRoot(types.Override{Network: false})
	if err != nil {
		t.Fatal(err)
	}
	wide, err := PolicyRoot(types.Override{Network: true})
	if err != nil {
		t.Fatal(err)
	}
	if narrow == wide {
		t.Fatal("two different policies share a root")
	}
	if LaunchRoot(packageRoot, narrow) == LaunchRoot(packageRoot, wide) {
		t.Fatal("the launch root ignores the policy")
	}
	// The identity survives a policy change, which is what lets an owner narrow
	// permissions without re-enrolling the package.
	second, err := boundPackage().Root()
	if err != nil {
		t.Fatal(err)
	}
	if packageRoot != second {
		t.Fatal("the package root is not stable")
	}
}

func TestPolicyRootIgnoresGrantOrderButNotContent(t *testing.T) {
	first, err := PolicyRoot(types.Override{Filesystem: []types.FilesystemPermission{
		{Path: "home/.local/share/bottles", Access: "read-write"},
		{Path: "xdg-documents", Access: "read-only"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := PolicyRoot(types.Override{Filesystem: []types.FilesystemPermission{
		{Path: "xdg-documents", Access: "read-only"},
		{Path: "home/.local/share/bottles", Access: "read-write"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("reordering the same grants changed the policy root")
	}
	widened, err := PolicyRoot(types.Override{Filesystem: []types.FilesystemPermission{
		{Path: "home/.local/share/bottles", Access: "read-write"},
		{Path: "xdg-documents", Access: "read-write"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first == widened {
		t.Fatal("a wider access produced the same policy root")
	}
}

func TestPolicyRootCanonicalizesSessionBusRules(t *testing.T) {
	first := types.Override{SessionBus: types.DBusPolicy{
		Talk: []types.DBusCallGrant{
			{Name: "org.example.Second", Path: "/org/example/Second", Interface: "org.example.Second", Members: []string{"Stop", "Start"}},
			{Name: "org.example.First", Path: "/org/example/First", Interface: "org.example.First", Members: []string{"Open"}},
		},
		Own: []string{"org.example.Second", "org.example.First"},
	}}
	second := types.Override{SessionBus: types.DBusPolicy{
		Talk: []types.DBusCallGrant{
			{Name: "org.example.First", Path: "/org/example/First", Interface: "org.example.First", Members: []string{"Open"}},
			{Name: "org.example.Second", Path: "/org/example/Second", Interface: "org.example.Second", Members: []string{"Start", "Stop"}},
		},
		Own: []string{"org.example.First", "org.example.Second"},
	}}
	firstRoot, err := PolicyRoot(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot, err := PolicyRoot(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstRoot != secondRoot {
		t.Fatal("reordering session bus rules changed the policy root")
	}
	second.SessionBus.Talk[1].Members = append(second.SessionBus.Talk[1].Members, "Pause")
	widenedRoot, err := PolicyRoot(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstRoot == widenedRoot {
		t.Fatal("a wider session bus rule produced the same policy root")
	}
}

func TestRestrictsSessionBusRules(t *testing.T) {
	current := types.Override{SessionBus: types.DBusPolicy{Talk: []types.DBusCallGrant{{
		Name: "org.example.Player", Path: "/org/example/Player", Interface: "org.example.Player", Members: []string{"Play", "Stop"},
	}}}}
	narrow := current
	narrow.SessionBus.Talk = []types.DBusCallGrant{{
		Name: "org.example.Player", Path: "/org/example/Player", Interface: "org.example.Player", Members: []string{"Play"},
	}}
	if !Restricts(current, narrow) {
		t.Fatal("a narrower session bus rule was refused")
	}
	if Restricts(narrow, current) {
		t.Fatal("a wider session bus rule was accepted")
	}
}

func TestPolicyRootReadsTheSchemaBeforeSerialDevices(t *testing.T) {
	policy := types.Override{SocketWayland: true, Network: true}
	legacy, err := PolicyRootForSchema(policy, PolicySchemaWithoutSerial)
	if err != nil {
		t.Fatal(err)
	}
	if legacy != "60f22559e6e387ce9a91f00256d8883ff55c0316a0dc85f4453c2bf16c3d8460" {
		t.Fatalf("legacy policy root changed to %s", legacy)
	}
	withoutSessionBus, err := PolicyRootForSchema(policy, PolicySchemaWithoutSessionBus)
	if err != nil {
		t.Fatal(err)
	}
	if withoutSessionBus != "d52cc55c1926145efb578cd47ca4e30aad5e4ee36769a6ffa82a799a3ca1813a" {
		t.Fatalf("policy root without session bus changed to %s", withoutSessionBus)
	}
	current, err := PolicyRoot(policy)
	if err != nil {
		t.Fatal(err)
	}
	if current != "384e94f695f06276a4c5b9f2ca519ab456be23e853c932beac05f2847271c60d" {
		t.Fatalf("current policy root changed to %s", current)
	}
}

func TestPolicyRootDoesNotDropSerialDeviceAccessFromAnOldSchema(t *testing.T) {
	policy := types.Override{DeviceSerial: true}
	if _, err := PolicyRootForSchema(policy, PolicySchemaWithoutSerial); err == nil {
		t.Fatal("the schema without serial devices accepted serial device access")
	}
}

func TestRestrictionsDoNotNeedConsent(t *testing.T) {
	current := types.Override{
		Network:    true,
		DeviceDri:  true,
		Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-write"}},
		HostActions: []types.HostActionGrant{
			{Provider: "containers", Capabilities: []string{"list", "start"}},
		},
	}
	for name, candidate := range map[string]types.Override{
		"a permission switched off": {
			DeviceDri:  true,
			Filesystem: current.Filesystem,
			HostActions: []types.HostActionGrant{
				{Provider: "containers", Capabilities: []string{"list", "start"}},
			},
		},
		"read-write turned into read-only": {
			Network:    true,
			DeviceDri:  true,
			Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-only"}},
			HostActions: []types.HostActionGrant{
				{Provider: "containers", Capabilities: []string{"list", "start"}},
			},
		},
		"a narrower path": {
			Network:    true,
			DeviceDri:  true,
			Filesystem: []types.FilesystemPermission{{Path: "home/.local/share/bottles", Access: "read-write"}},
			HostActions: []types.HostActionGrant{
				{Provider: "containers", Capabilities: []string{"list", "start"}},
			},
		},
		"one capability dropped": {
			Network:    true,
			DeviceDri:  true,
			Filesystem: current.Filesystem,
			HostActions: []types.HostActionGrant{
				{Provider: "containers", Capabilities: []string{"list"}},
			},
		},
	} {
		if !Restricts(current, candidate) {
			t.Fatalf("%s was read as a widening", name)
		}
	}
}

func TestWideningNeedsConsent(t *testing.T) {
	current := types.Override{
		Filesystem: []types.FilesystemPermission{{Path: "home/.local/share/bottles", Access: "read-only"}},
	}
	for name, candidate := range map[string]types.Override{
		"a permission switched on":        {Network: true, Filesystem: current.Filesystem},
		"read-only turned into readwrite": {Filesystem: []types.FilesystemPermission{{Path: "home/.local/share/bottles", Access: "read-write"}}},
		"a wider path":                    {Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-only"}}},
		"a new provider":                  {Filesystem: current.Filesystem, HostActions: []types.HostActionGrant{{Provider: "containers", Capabilities: []string{"list"}}}},
		"an environment change":           {Filesystem: current.Filesystem, Env: []string{"LD_PRELOAD=/tmp/evil.so"}},
		"a limit lifted":                  {Filesystem: current.Filesystem, PidsMax: 0},
	} {
		if name == "a limit lifted" {
			limited := current
			limited.PidsMax = 64
			if Restricts(limited, candidate) {
				t.Fatalf("%s was read as a restriction", name)
			}
			continue
		}
		if Restricts(current, candidate) {
			t.Fatalf("%s was read as a restriction", name)
		}
	}
}

func TestUncomparablePoliciesAreTreatedAsWidening(t *testing.T) {
	current := types.Override{Filesystem: []types.FilesystemPermission{{Path: "home/documents", Access: "read-write"}}}
	candidate := types.Override{Filesystem: []types.FilesystemPermission{{Path: "xdg-documents", Access: "read-write"}}}
	if Restricts(current, candidate) {
		t.Fatal("a scope that only looks narrower was accepted without consent")
	}
}

func TestRootsCarryTheABI(t *testing.T) {
	packageRoot, err := boundPackage().Root()
	if err != nil {
		t.Fatal(err)
	}
	if len(packageRoot) != 64 || strings.ContainsAny(packageRoot, "ghijklmnopqrstuvwxyz") {
		t.Fatalf("the root is not a hex sha256: %s", packageRoot)
	}
	if LaunchRoot(packageRoot, "policy") == LaunchRoot("policy", packageRoot) {
		t.Fatal("the launch root does not distinguish its inputs")
	}
}

func boundAnchor() Anchor {
	packageRoot := strings.Repeat("a1", 32)
	policyRoot := strings.Repeat("b2", 32)
	return Anchor{
		ABI:            ABIVersion,
		UID:            1000,
		Origin:         "github.com/bottlesdevs/bottles",
		Generation:     1,
		ImageDigest:    "sha256:" + strings.Repeat("cd", 32),
		ManifestDigest: strings.Repeat("ef", 32),
		PackageRoot:    packageRoot,
		PolicyRoot:     policyRoot,
		LaunchRoot:     LaunchRoot(packageRoot, policyRoot),
	}
}

// The two digests are what a publisher signature is compared against, so a
// value written in a shape no signed state can name is refused where it is
// written. Silently keeping one would mean a signature that can never match
// anything, which reads as a package no publisher signed.
func TestAnAnchorDigestMustBeAShapeASignedStateCanName(t *testing.T) {
	if err := boundAnchor().ValidateDigests(); err != nil {
		t.Fatalf("the digests a signed state names were refused: %v", err)
	}
	unprefixedImage := boundAnchor()
	unprefixedImage.ImageDigest = strings.Repeat("cd", 32)
	prefixedManifest := boundAnchor()
	prefixedManifest.ManifestDigest = "sha256:" + strings.Repeat("ef", 32)
	uppercase := boundAnchor()
	uppercase.ImageDigest = strings.ToUpper(uppercase.ImageDigest)
	truncated := boundAnchor()
	truncated.ManifestDigest = strings.Repeat("ef", 16)
	tagged := boundAnchor()
	tagged.ImageDigest = "sha256:latest"
	for name, anchor := range map[string]Anchor{
		"an image digest with no algorithm": unprefixedImage,
		"a manifest digest with one":        prefixedManifest,
		"a digest that is not lowercase":    uppercase,
		"a digest of the wrong length":      truncated,
		"a tag where a digest belongs":      tagged,
	} {
		if err := anchor.ValidateDigests(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// An anchor recorded before the digests existed is still an anchor. What it is
// not is an anchor a signature can be filed under, and that is decided where
// signatures are and not here.
func TestAnAnchorMayStateNoDigestsAtAll(t *testing.T) {
	anchor := boundAnchor()
	anchor.ImageDigest = ""
	anchor.ManifestDigest = ""
	if err := anchor.ValidateDigests(); err != nil {
		t.Fatalf("an anchor written before the digests existed was refused: %v", err)
	}
}

// A package installed from a v1 manifest was enrolled under fsHostHome and is
// offered again under the typed grant that field stands for. That is one
// restriction written twice, and an order that cannot see it answers "wider",
// which is the administrator's password: cpak would be asking for one because
// it changed the shape of its own manifest, and an owner asked for nothing
// stops reading the question by the time it is asked for something.
func TestTheMigratedFormOfAPolicyIsTheSameRestriction(t *testing.T) {
	legacy := types.Override{
		SocketWayland: true,
		FsHostHome:    true,
		FsExtra:       []string{"/srv/data"},
	}
	migrated := types.Override{
		SocketWayland: true,
		Filesystem: []types.FilesystemPermission{
			{Path: "home", Access: "read-write"},
			{Path: "/srv/data", Access: "read-write"},
		},
	}
	if !Restricts(legacy, migrated) {
		t.Fatal("the migrated form of a policy was read as a widening of it")
	}
	if !Restricts(migrated, legacy) {
		t.Fatal("the legacy form of a policy was read as a widening of its own migration")
	}

	for name, candidate := range map[string]types.Override{
		"the same grants, read-only": {
			SocketWayland: true,
			Filesystem: []types.FilesystemPermission{
				{Path: "home", Access: "read-only"},
				{Path: "/srv/data", Access: "read-only"},
			},
		},
		"a directory inside one of them": {
			SocketWayland: true,
			Filesystem:    []types.FilesystemPermission{{Path: "/srv/data/cache", Access: "read-write"}},
		},
		"nothing at all": {SocketWayland: true},
	} {
		if !Restricts(legacy, candidate) {
			t.Fatalf("%s was read as a widening of the v1 policy it narrows", name)
		}
	}
}

// The equivalence narrows nothing else. What the legacy fields never granted is
// still a widening whichever spelling asks for it.
func TestTheMigratedFormStillHasToBeCovered(t *testing.T) {
	for name, test := range map[string]struct {
		current   types.Override
		candidate types.Override
	}{
		"the home, from a policy that only held /etc": {
			current:   types.Override{FsHostEtc: true},
			candidate: types.Override{Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-write"}}},
		},
		"writing the host, from a policy that only read it": {
			current:   types.Override{FsHost: true},
			candidate: types.Override{Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-write"}}},
		},
		"a legacy field, from a policy that granted nothing": {
			current:   types.Override{SocketWayland: true},
			candidate: types.Override{SocketWayland: true, FsHostHome: true},
		},
		"a path beside the one that was granted": {
			current:   types.Override{FsExtra: []string{"/srv/data"}},
			candidate: types.Override{Filesystem: []types.FilesystemPermission{{Path: "/srv/data-other", Access: "read-write"}}},
		},
	} {
		if Restricts(test.current, test.candidate) {
			t.Fatalf("%s was read as a restriction", name)
		}
	}
}
