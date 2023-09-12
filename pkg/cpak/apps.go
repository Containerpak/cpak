package cpak

import "github.com/mirkobrombin/cpak/pkg/types"

// GetInstalledApps returns a list of installed applications.
//
// Note: this function should always be called after the Audit function
// to ensure that the store is in a consistent state.
func (c *Cpak) GetInstalledApps() (apps []types.Application, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	apps, err = store.GetApplications()
	return
}
