package cpak

import (
	"fmt"
	"os"

	cTypes "github.com/linux-immutability-tools/containers-wrapper/pkg/types"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// PrepareContainer prepares a container for the given application.
// It returns an existing one if it exists, otherwise it creates a new one.
func (c *Cpak) PrepareContainer(app types.Application) (container types.Container, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	containers, err := store.GetApplicationContainers(app)
	if err != nil {
		return
	}

	container.Application = app

	if len(containers) > 0 {
		container = containers[0]

		err = c.Ce.StartContainer(container.Id, false)
		if err == nil {
			fmt.Println("Existing container found:", container.Id)
			return
		}
	}

	container.Id, err = c.Ce.CreateContainer(
		app.Id,
		cTypes.ContainerCreateOptions{
			Entrypoint: "sleep",
			Env: []string{
				"HOME=" + os.Getenv("HOME"),
			},
			Volume: []string{
				"/tmp:/tmp:rslave",
				os.Getenv("HOME") + ":" + os.Getenv("HOME") + ":rslave",
				// "/:/run/host:rslave", TODO: enable this according to the maintainer/user choice
			},
		},
		[]string{"infinity"}...,
	)
	if err != nil {
		return
	}

	fmt.Println("Container created:", container.Id)

	err = store.NewContainer(container)
	if err != nil {
		return
	}

	fmt.Println("Container prepared:", container.Id)
	return
}
