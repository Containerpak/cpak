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
