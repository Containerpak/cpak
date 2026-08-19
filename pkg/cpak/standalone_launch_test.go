/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// reachedTheLayerMount answers whether a launch got past the gate and stopped
// where this fixture always stops: none of its layers is on disk.
//
// Which of the two answers a machine gives depends on whether cpak's storage
// service is installed on it, and neither of them is the gate. Asserting one of
// the messages alone is asserting something about the machine the tests are
// running on.
func reachedTheLayerMount(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errStorageServiceMissing) || strings.Contains(err.Error(), "is not available")
}

// pulledInFixture is a package the user named and a package it pulled in, both
// installed, with the second one enrolled and its layers bound so that a launch
// of it reaches the gate with everything the gate needs.
func pulledInFixture(t *testing.T) (*Cpak, types.Application, types.Application) {
	t.Helper()

	cp := newTestCpak(t)
	useNoHostCeiling(t)
	ledger := useAnchorLedger(t)
	useEnforcement(t, systemauthority.EnforcementRefuse)
	bindLayer(t, cp, verifiedBaseLayer, "state-base")
	bindLayer(t, cp, verifiedTopLayer, "state-top")

	child := verifiedApplication()
	child.CpakId = "child-id"
	child.Name = "library"
	child.Origin = "github.com/user/library"
	child.PulledIn = true
	child.PulledInBy = testOrigin
	child.ParsedOverride = types.Override{
		Network:   true,
		DeviceDri: true,
		Env:       []string{"SHARED=1", "CHILD=1"},
		Filesystem: []types.FilesystemPermission{
			{Path: "/shared", Access: "read-write"},
			{Path: "/child-only", Access: "read-write"},
		},
		HostApplications: true,
		OpenURI:          true,
		MemoryMaxMB:      1024,
	}

	parent := types.Application{
		CpakId:  "parent-id",
		Name:    "demo",
		Origin:  testOrigin,
		Version: "1",
		ParsedDependencies: []types.Dependency{{
			Id:     child.CpakId,
			Origin: child.Origin,
			Branch: child.Branch,
		}},
		ParsedOverride: types.Override{
			Network:   false,
			DeviceDri: true,
			Env:       []string{"SHARED=1", "PARENT=1"},
			Filesystem: []types.FilesystemPermission{
				{Path: "/shared", Access: "read-write"},
				{Path: "/parent-only", Access: "read-write"},
			},
			HostApplications: false,
			OpenURI:          false,
			MemoryMaxMB:      512,
		},
	}

	seedApplication(t, cp, parent)
	seedApplication(t, cp, child)

	// The anchor is taken over the policy the application asked for, which is
	// what enrolApplication records, and not over anything a launch narrows it
	// to afterwards.
	derived, err := cp.verifyLaunch(child, resolvedOverride(child), nil, nil)
	if err != nil {
		t.Fatalf("the launch of the dependency could not be described: %v", err)
	}
	enrol(t, ledger, derived)

	return cp, parent, child
}

// The defect this test exists for. A dependency launched on its own is held to
// what the package that pulled it in may do, and holding it to that must not
// turn the launch into one no anchor names: the gate compares the launch root
// it derives with the recorded one and refuses anything else at every
// enforcement level, so a narrower policy shown to the gate does not narrow the
// launch, it cancels it.
func TestANarrowedStandaloneLaunchIsStillTheLaunchTheAnchorNames(t *testing.T) {
	cp, _, child := pulledInFixture(t)

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := standaloneLaunchPolicy(store, child)
	if err != nil {
		t.Fatal(err)
	}
	if policy.effective.Network {
		t.Fatal("the fixture narrows nothing, so it cannot tell the two policies apart")
	}

	// Everything RunInstance does once it has the application and the policy.
	// The launch cannot complete here, since no layer of the fixture is on
	// disk, but it fails at the mount and not at the gate.
	err = cp.runApplicationInstanceWithStore(child, policy, "", "/usr/bin/demo", false, false, store)
	if errors.Is(err, errLaunchUnrecognised) {
		t.Fatalf("a launch narrowed after enrolment was refused as one no anchor names: %v", err)
	}
	if !reachedTheLayerMount(err) {
		t.Fatalf("got %v, want the launch to reach the layer mount", err)
	}
}

// A package the user named keeps the policy its owner decided, whoever else
// declares it as a dependency. Without that, any publisher could strip
// permissions from unrelated software by naming it in their manifest.
func TestAPackageTheUserNamedIsNotNarrowedByWhoeverDeclaresIt(t *testing.T) {
	cp, parent, child := pulledInFixture(t)

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	named := child
	named.PulledIn = false
	policy, err := standaloneLaunchPolicy(store, named)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.effective.Network || policy.effective.MemoryMaxMB != 1024 {
		t.Fatalf("a package the user installed by name was narrowed by %s: %+v", parent.Origin, policy.effective)
	}
	if len(policy.effective.Env) != 2 {
		t.Fatalf("a package the user installed by name lost its environment: %v", policy.effective.Env)
	}
}

// What the user agreed to when they installed the parent covers everything the
// parent asked for, however it asked for it, so a wide package cannot be put
// beyond reach of the intersection by being declared as a layer.
func TestAPulledInPackageIsHeldToEveryPackageThatDeclaresIt(t *testing.T) {
	cp, parent, child := pulledInFixture(t)

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, mode := range []string{"", "nested", "layer"} {
		declaring := parent
		declaring.ParsedDependencies = []types.Dependency{{
			Id:     child.CpakId,
			Origin: child.Origin,
			Branch: child.Branch,
			Mode:   mode,
		}}
		if err := store.NewApplication(declaring); err != nil {
			t.Fatal(err)
		}
		policy, err := standaloneLaunchPolicy(store, child)
		if err != nil {
			t.Fatal(err)
		}
		if policy.effective.Network {
			t.Fatalf("a dependency declared as %q kept a permission its parent was refused", mode)
		}
		if !policy.effective.DeviceDri {
			t.Fatalf("a dependency declared as %q lost a permission both were granted", mode)
		}
		if policy.effective.MemoryMaxMB != 512 {
			t.Fatalf("a dependency declared as %q kept a limit wider than its parent: %d", mode, policy.effective.MemoryMaxMB)
		}
		if len(policy.effective.Filesystem) != 1 || policy.effective.Filesystem[0].Path != "/shared" {
			t.Fatalf("a dependency declared as %q kept a mount its parent has not: %v", mode, policy.effective.Filesystem)
		}
	}
}

// The half of the intersection that reaches the process. Every command that
// runs in the container is built from the policy the launch was narrowed to,
// not from the manifest of the package being launched, or the environment of a
// dependency would carry what the intersection had just taken away.
func TestTheContainerEnvironmentComesFromThePolicyTheLaunchRunsUnder(t *testing.T) {
	_, parent, child := pulledInFixture(t)

	child.Config = `{"config":{"Env":["IMAGE_VALUE=1"]}}`
	narrowed := intersectOverrides(resolvedOverride(parent), resolvedOverride(child))
	environment, err := containerEnvironment(child, narrowed, types.Container{CpakId: "container-id"})
	if err != nil {
		t.Fatal(err)
	}

	if slicesContain(environment, "CHILD=1") {
		t.Fatalf("the launch carried an environment variable the intersection removed: %v", environment)
	}
	if !slicesContain(environment, "SHARED=1") {
		t.Fatalf("the launch lost an environment variable both packages granted: %v", environment)
	}
	for _, value := range environment {
		if strings.HasPrefix(value, "XDG_DATA_DIRS=") && strings.Contains(value, hostApplicationsTarget) {
			t.Fatalf("the launch was given the host applications tree the intersection removed: %s", value)
		}
	}
}

// A dependency the user never named gets no launchers, and the answer is the
// same whichever command rebuilt the exports: an origin that flips between
// exported and not depending on what touched it last is worse than either.
func TestExportsFollowWhoNamedThePackage(t *testing.T) {
	cp := newTestCpak(t)

	named := types.Application{
		CpakId:         "named-id",
		Origin:         "github.com/user/demo",
		ParsedBinaries: []string{"/usr/bin/demo"},
	}
	if err := cp.createExports(named); err != nil {
		t.Fatal(err)
	}
	if _, err := statExport(cp, "github.com/user/demo", "demo"); err != nil {
		t.Fatalf("the package the user named was not exported: %v", err)
	}

	pulledIn := types.Application{
		CpakId:         "pulled-in-id",
		Origin:         "github.com/user/library",
		ParsedBinaries: []string{"/usr/bin/library"},
		PulledIn:       true,
	}
	if err := cp.createExports(pulledIn); err != nil {
		t.Fatal(err)
	}
	if _, err := statExport(cp, "github.com/user/library", "library"); err == nil {
		t.Fatal("a package the user never named was given a launcher on the host")
	}
}

func statExport(cp *Cpak, origin, binary string) (string, error) {
	path := filepath.Join(append([]string{cp.Options.ExportsPath}, append(strings.Split(origin, "/"), binary)...)...)
	_, err := os.Stat(path)
	return path, err
}

// The same wall stood in front of a nested run, which has been handing the gate
// an intersection since intersections existed: a package whose parent is
// narrower than it is could not be started by that parent either.
func TestANestedRunIsStillTheLaunchTheAnchorNames(t *testing.T) {
	cp, parent, child := pulledInFixture(t)

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	token, err := newNestedToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.NewContainer(types.Container{
		CpakId:            "parent-container",
		ApplicationCpakId: parent.CpakId,
		NestedToken:       token,
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	authorized, err := cp.authorizeNestedRun(types.RequestParams{
		Action: "run",
		Token:  token,
		Origin: child.Origin,
		Binary: "@/usr/bin/demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorized.policy.effective.Network {
		t.Fatal("the fixture narrows nothing, so it cannot tell the two policies apart")
	}

	err = cp.runApplication(authorized.child, authorized.policy, authorized.binary, false, true)
	if errors.Is(err, errLaunchUnrecognised) {
		t.Fatalf("a nested run narrowed by its parent was refused as a launch no anchor names: %v", err)
	}
	if !reachedTheLayerMount(err) {
		t.Fatalf("got %v, want the nested run to reach the layer mount", err)
	}
}

// Declaring a package is not the same as having brought it here. Only the
// installation a package came in behind has a say in how it starts on its own,
// or any publisher could reach into an installation they had nothing to do with
// by naming it in a manifest of their own.
func TestOnlyThePackageThatBroughtADependencyHereNarrowsIt(t *testing.T) {
	cp, _, child := pulledInFixture(t)

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	stranger := types.Application{
		CpakId:  "stranger-id",
		Name:    "stranger",
		Origin:  "github.com/stranger/thing",
		Version: "1",
		ParsedDependencies: []types.Dependency{{
			Id:     child.CpakId,
			Origin: child.Origin,
			Branch: child.Branch,
		}},
		ParsedOverride: types.Override{},
	}
	if err := store.NewApplication(stranger); err != nil {
		t.Fatal(err)
	}

	policy, err := standaloneLaunchPolicy(store, child)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.effective.DeviceDri {
		t.Fatal("a package that never brought the dependency here took a permission away from it")
	}
	if policy.effective.MemoryMaxMB != 512 {
		t.Fatalf("a package that never brought the dependency here changed its limits: %d", policy.effective.MemoryMaxMB)
	}
	if policy.effective.Network {
		t.Fatal("the package that did bring it here stopped narrowing it")
	}
}

// A record from before cpak kept the answer says only that it was pulled in.
// There is nobody to hold it to in particular, so it is held to everybody who
// declares it, which is the narrower of the two readings.
func TestADependencyThatNamesNoParentIsHeldToEveryDeclaringPackage(t *testing.T) {
	cp, _, child := pulledInFixture(t)

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	child.PulledInBy = ""
	policy, err := standaloneLaunchPolicy(store, child)
	if err != nil {
		t.Fatal(err)
	}
	if policy.effective.Network {
		t.Fatal("a record that names no parent was left at the policy its own publisher asked for")
	}
}
