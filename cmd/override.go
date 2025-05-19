package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/spf13/cobra"
)

// NewOverrideCommand returns the cobra command for setting a single override key/value
func NewOverrideCommand() *cobra.Command {
	var appOrigin, key, value string
	cmd := &cobra.Command{
		Use:   "override",
		Short: "Set override key/value for a cpak application",
		Long: `Set a single override key to a given value for an installed cpak application.
Use JSON field names for KEY (e.g. socketX11, fsExtra, env, etc.).
For list fields (fsExtra, env, allowedHostCommands), separate items with ':'`,
		Args: cobra.NoArgs,
		RunE: overrideRun,
	}
	cmd.Flags().StringVarP(&appOrigin, "app", "a", "", "Application origin (required)")
	cmd.Flags().StringVarP(&key, "key", "k", "", "Override key (json field name) (required)")
	cmd.Flags().StringVarP(&value, "value", "v", "", "Override value (required)")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("key")
	_ = cmd.MarkFlagRequired("value")
	overrideCmd = &overrideOptions{app: &appOrigin, key: &key, value: &value}
	return cmd
}

// overrideOptions holds flags for override command
var overrideCmd *overrideOptions

type overrideOptions struct {
	app   *string
	key   *string
	value *string
}

// overrideRun implements the override command logic
func overrideRun(cmd *cobra.Command, args []string) error {
	appOrigin := *overrideCmd.app
	key := *overrideCmd.key
	value := *overrideCmd.value

	// Initialize cpak and store
	cpk, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("failed to initialize cpak: %w", err)
	}
	store, err := cpak.NewStore(cpk.Options.StorePath)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	apps, err := store.GetApplications()
	if err != nil {
		return fmt.Errorf("failed to list applications: %w", err)
	}
	if len(apps) == 0 {
		return fmt.Errorf("no cpak applications installed")
	}

	// Find the application by origin
	var selected types.Application
	for _, app := range apps {
		if app.Origin == appOrigin {
			selected = app
			break
		}
	}
	if selected.Origin == "" {
		return fmt.Errorf("application origin %q not found", appOrigin)
	}

	// Load existing override or fallback to manifest
	manifestOverride := selected.ParsedOverride
	userOverride, loadErr := cpak.LoadOverride(selected.Origin, selected.Version)
	var override types.Override
	if loadErr != nil {
		override = manifestOverride
	} else {
		override = userOverride
	}

	// Apply key/value
	if err := applyOverride(&override, key, value); err != nil {
		return err
	}

	// Save override
	if err := cpak.SaveOverride(override, selected.Origin, selected.Version); err != nil {
		return fmt.Errorf("failed to save override: %w", err)
	}
	fmt.Printf("Override %s=%s saved for %s\n", key, value, selected.Origin)
	return nil
}

// applyOverride sets the appropriate field in Override
func applyOverride(o *types.Override, key, value string) error {
	parseBool := func(val string) (bool, error) {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return false, fmt.Errorf("invalid boolean for %s: %w", key, err)
		}
		return b, nil
	}

	switch key {
	case "socketX11":
		o.SocketX11, _ = parseBool(value)
	case "socketWayland":
		o.SocketWayland, _ = parseBool(value)
	case "socketPulseAudio":
		o.SocketPulseAudio, _ = parseBool(value)
	case "socketSessionBus":
		o.SocketSessionBus, _ = parseBool(value)
	case "socketSystemBus":
		o.SocketSystemBus, _ = parseBool(value)
	case "socketSshAgent":
		o.SocketSshAgent, _ = parseBool(value)
	case "socketCups":
		o.SocketCups, _ = parseBool(value)
	case "socketGpgAgent":
		o.SocketGpgAgent, _ = parseBool(value)
	case "socketAtSpiBus":
		o.SocketAtSpiBus, _ = parseBool(value)
	case "notification":
		o.Notification, _ = parseBool(value)
	case "deviceDri":
		o.DeviceDri, _ = parseBool(value)
	case "deviceKvm":
		o.DeviceKvm, _ = parseBool(value)
	case "deviceShm":
		o.DeviceShm, _ = parseBool(value)
	case "deviceAll":
		o.DeviceAll, _ = parseBool(value)
	case "fsHost":
		o.FsHost, _ = parseBool(value)
	case "fsHostEtc":
		o.FsHostEtc, _ = parseBool(value)
	case "fsHostHome":
		o.FsHostHome, _ = parseBool(value)
	case "network":
		o.Network, _ = parseBool(value)
	case "process":
		o.Process, _ = parseBool(value)
	case "asRoot":
		o.AsRoot, _ = parseBool(value)
	case "fsExtra":
		o.FsExtra = parseList(value)
	case "env":
		o.Env = parseList(value)
	case "allowedHostCommands":
		o.AllowedHostCommands = parseList(value)
	default:
		return fmt.Errorf("unknown or unsupported key %q", key)
	}
	return nil
}

// isEmpty checks whether the override has only default values
func isEmpty(o types.Override) bool {
	def := types.NewOverride()
	if o.SocketX11 != def.SocketX11 || o.SocketWayland != def.SocketWayland ||
		o.SocketPulseAudio != def.SocketPulseAudio || o.SocketSessionBus != def.SocketSessionBus ||
		o.SocketSystemBus != def.SocketSystemBus || o.SocketSshAgent != def.SocketSshAgent ||
		o.SocketCups != def.SocketCups || o.SocketGpgAgent != def.SocketGpgAgent ||
		o.SocketAtSpiBus != def.SocketAtSpiBus || o.DeviceDri != def.DeviceDri ||
		o.DeviceKvm != def.DeviceKvm || o.DeviceShm != def.DeviceShm || o.DeviceAll != def.DeviceAll ||
		o.Notification != def.Notification || o.FsHost != def.FsHost || o.FsHostEtc != def.FsHostEtc ||
		o.FsHostHome != def.FsHostHome || o.Network != def.Network || o.Process != def.Process ||
		o.AsRoot != def.AsRoot {
		return false
	}
	if len(o.FsExtra) != 0 || len(o.Env) != 0 || len(o.AllowedHostCommands) != 0 {
		return false
	}
	return true
}

// parseList splits a colon-separated string into a slice
func parseList(val string) []string {
	parts := strings.Split(val, ":")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
