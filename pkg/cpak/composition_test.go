package cpak

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestResolveLayerDependenciesUsesDependencyOrder(t *testing.T) {
	c := newTestCpak(t)
	base := types.Application{CpakId: "base", Origin: "github.com/example/base", ParsedLayers: []string{"base-layer"}}
	shell := types.Application{
		CpakId:       "shell",
		Origin:       "github.com/example/shell",
		ParsedLayers: []string{"base-layer", "shell-layer"},
		ParsedDependencies: []types.Dependency{{
			Id:     base.CpakId,
			Origin: base.Origin,
			Mode:   "layer",
		}},
	}
	apps := types.Application{CpakId: "apps", Origin: "github.com/example/apps", ParsedLayers: []string{"apps-layer"}}
	parent := types.Application{
		CpakId: "desktop",
		Origin: "github.com/example/desktop",
		ParsedDependencies: []types.Dependency{
			{Id: shell.CpakId, Origin: shell.Origin, Mode: "layer"},
			{Id: apps.CpakId, Origin: apps.Origin, Mode: "layer"},
		},
	}
	for _, app := range []types.Application{base, shell, apps} {
		seedApplication(t, c, app)
	}

	components, err := c.resolveLayerDependencies(parent)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{components[0].CpakId, components[1].CpakId, components[2].CpakId}
	if want := []string{"base", "shell", "apps"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("component order: got %v, want %v", got, want)
	}
	layers := composedLayers(parent, components, nil)
	if want := []string{"base-layer", "shell-layer", "apps-layer"}; !reflect.DeepEqual(layers, want) {
		t.Fatalf("composed layers: got %v, want %v", layers, want)
	}
}

func TestResolveLayerDependenciesRejectsCycles(t *testing.T) {
	c := newTestCpak(t)
	first := types.Application{CpakId: "first", Origin: "github.com/example/first"}
	second := types.Application{CpakId: "second", Origin: "github.com/example/second"}
	first.ParsedDependencies = []types.Dependency{{Id: second.CpakId, Origin: second.Origin, Mode: "layer"}}
	second.ParsedDependencies = []types.Dependency{{Id: first.CpakId, Origin: first.Origin, Mode: "layer"}}
	seedApplication(t, c, first)
	seedApplication(t, c, second)

	if _, err := c.resolveLayerDependencies(first); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("got %v", err)
	}
}

func TestContainerPolicyHashTracksLayerComponents(t *testing.T) {
	override := types.NewOverride()
	without, err := containerPolicyHash(override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	with, err := containerPolicyHash(override, []types.Application{{
		CpakId:       "shell",
		ImageDigest:  "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		ParsedLayers: []string{"shell"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if without == with {
		t.Fatal("layer component did not change the container policy hash")
	}
}

func TestDependencyUsersIncludeLayerComponents(t *testing.T) {
	c := newTestCpak(t)
	component := types.Application{CpakId: "component", Origin: "github.com/example/component"}
	parent := types.Application{
		CpakId: "desktop",
		Origin: "github.com/example/desktop",
		ParsedDependencies: []types.Dependency{{
			Id:     component.CpakId,
			Origin: component.Origin,
			Mode:   "layer",
		}},
	}
	seedApplication(t, c, component)
	seedApplication(t, c, parent)

	users, err := c.dependencyUsers(component.CpakId)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].CpakId != parent.CpakId {
		t.Fatalf("dependency users: got %v, want %s", users, parent.CpakId)
	}
}
