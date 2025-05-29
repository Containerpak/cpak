/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cpak

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// GetInstalledApps returns a list of installed applications.
//
// Note: this function should always be called after the Audit function
// to ensure that the store is in a consistent state.
func (c *Cpak) GetInstalledApps() (apps []types.Application, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open store for GetInstalledApps: %w", err)
	}
	defer store.Close()

	apps, err = store.GetApplications()
	if err != nil {
		return nil, fmt.Errorf("failed to get applications from store: %w", err)
	}
	return
}
