package cpak

import (
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Run runs the given binary from the given application. The binary can be
// specified as a path or as a name. If the binary is specified as a name,
// the first binary matching the given name will be executed. To execute a
// unexported binary, the binary name must be prefixed with a "@".
//
// Note: binaries specified with the "@" prefix are not guaranteed to be
// available in required applications, so it is recommended to use them only
// for debugging purposes and handle the error case when the binary is not
// available, e.g. in shell scripts.
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
		return fmt.Errorf("no application found for origin %s and version %s: %s", origin, version, err)
	}

	var container types.Container
	container, err = c.PrepareContainer(app)
	if err != nil {
		return
	}

	command := []string{}
	if strings.HasPrefix(binary, "@") {
		command = append(command, binary[1:])
		command = append(command, extraArgs...)
		return c.ExecInContainer(container, command)
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

	command = append(command, app.Binaries[0])
	command = append(command, extraArgs...)
	err = c.ExecInContainer(container, command)
	return
}
