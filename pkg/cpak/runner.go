package cpak

import (
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func (c *Cpak) Run(origin string, version string, binary string, extraArgs ...string) (err error) {
	workDir := os.Getenv("PWD")
	if !strings.HasPrefix(workDir, "/home") {
		workDir = "/"
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	app, err := store.GetApplicationByOrigin(origin, version)
	if err != nil {
		return fmt.Errorf("no application found for origin %s and version %s", origin, version)
	}

	var container types.Container
	if c.Options.Mode == "keep" {
		container, err = c.PrepareContainer(app)
		if err != nil {
			return
		}
	}

	if strings.HasPrefix(binary, "@") {
		if c.Options.Mode == "keep" {
			return c.Ce.ExecInContainer(
				container.Id, true, binary[1:], workDir, extraArgs...)
		} else {
			return c.Ce.RunInContainer(
				app.Id, true, binary[1:], workDir, extraArgs...)
		}
	} else if strings.HasPrefix(binary, "/") {
		binary = binary[strings.LastIndex(binary, "/")+1:]
	}

	for _, _binary := range app.Binaries {
		_binary = _binary[strings.LastIndex(_binary, "/")+1:]
		if _binary == binary {
			break
		}
	}

	if app.Id == "" {
		if version == "" {
			return fmt.Errorf("no application found for origin %s", origin)
		}
		return fmt.Errorf("no application found for origin %s and version %s", origin, version)
	}

	if len(app.Binaries) == 0 {
		return fmt.Errorf("no exported binaries found for application %s", app.Name)
	}

	if c.Options.Mode == "keep" {
		err = c.Ce.ExecInContainer(container.Id, true, binary, workDir, extraArgs...)
	} else {
		err = c.Ce.RunInContainer(app.Id, true, binary, workDir, extraArgs...)
	}
	return
}
