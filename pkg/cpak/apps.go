package cpak

import "github.com/mirkobrombin/cpak/pkg/types"

func (c *Cpak) GetInstalledApps() (apps []types.Application, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	apps, err = store.GetApplications()
	return
}
