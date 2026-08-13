package cpak

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func (c *Cpak) resolveLayerDependencies(app types.Application) ([]types.Application, error) {
	if len(app.ParsedDependencies) == 0 {
		return nil, nil
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return c.resolveLayerDependenciesFromStore(app, store)
}

func (c *Cpak) resolveLayerDependenciesFromStore(app types.Application, store *Store) ([]types.Application, error) {
	if len(app.ParsedDependencies) == 0 {
		return nil, nil
	}
	states := make(map[string]int)
	states[app.CpakId] = 1
	components := make([]types.Application, 0)
	var visit func(types.Application) error
	visit = func(parent types.Application) error {
		for _, dependency := range parent.ParsedDependencies {
			if !dependency.IsLayer() {
				continue
			}
			component, getErr := store.GetApplicationByCpakId(dependency.Id)
			if getErr != nil {
				return fmt.Errorf("cannot load layer dependency %s: %w", dependency.Origin, getErr)
			}
			if component.Origin != dependency.Origin {
				return fmt.Errorf("layer dependency %s does not match the installed application", dependency.Origin)
			}
			switch states[component.CpakId] {
			case 1:
				return fmt.Errorf("layer dependency cycle detected at %s", component.Origin)
			case 2:
				continue
			}
			states[component.CpakId] = 1
			if err := visit(component); err != nil {
				return err
			}
			states[component.CpakId] = 2
			components = append(components, component)
		}
		return nil
	}
	if err := visit(app); err != nil {
		return nil, err
	}
	states[app.CpakId] = 2
	return components, nil
}

func composedLayers(app types.Application, components, addons []types.Application) []string {
	seen := make(map[string]bool)
	layers := make([]string, 0, len(app.ParsedLayers))
	appendLayers := func(values []string) {
		for _, layer := range values {
			if layer == "" || seen[layer] {
				continue
			}
			seen[layer] = true
			layers = append(layers, layer)
		}
	}
	for _, component := range components {
		appendLayers(component.ParsedLayers)
	}
	appendLayers(app.ParsedLayers)
	for _, addon := range addons {
		appendLayers(addon.ParsedLayers)
	}
	return layers
}
