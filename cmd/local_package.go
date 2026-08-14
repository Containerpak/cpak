/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type localPackageRequest struct {
	Mode         string
	ManifestPath string
	LockPath     string
	Origin       string
	Binary       string
	Extra        []string
	Verbose      bool
	Launch       bool
}

func installedApplication(cp *cpak.Cpak, origin, branch string) (types.Application, error) {
	apps, err := cp.GetInstalledApps()
	if err != nil {
		return types.Application{}, err
	}
	for _, app := range apps {
		if strings.EqualFold(app.Origin, origin) && app.Branch == branch {
			return app, nil
		}
	}
	return types.Application{}, fmt.Errorf("installed application not found: %s", origin)
}

func runLocalPackage(request localPackageRequest) (resultErr error) {
	manifestPath := request.ManifestPath
	if manifestPath == "" {
		manifestPath = "cpak.json"
	}
	manifest, err := loadManifest(manifestPath)
	if err != nil {
		return err
	}
	origin, err := resolveManifestOrigin(manifestPath, request.Origin, manifest)
	if err != nil {
		return err
	}

	cleanupEnvironment, err := useIsolatedCpakEnvironment()
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := cleanupEnvironment(); resultErr == nil && cleanupErr != nil {
			resultErr = cleanupErr
		}
	}()

	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	defer func() {
		if stopErr := cp.StopOwnedService(); resultErr == nil && stopErr != nil {
			resultErr = stopErr
		}
	}()

	var lock *types.ManifestLock
	lockPath := inferredLockPath(manifestPath, request.LockPath)
	if lockPath != "" {
		loaded, loadErr := loadManifestLock(lockPath)
		if loadErr != nil {
			return loadErr
		}
		if verifyErr := cp.VerifyManifestLock(origin, manifest, loaded); verifyErr != nil {
			return fmt.Errorf("verify lock file: %w", verifyErr)
		}
		lock = &loaded
		manifest = applyLockedImage(manifest, loaded)
	}

	options := cpak.InstallOptions{CreateExports: false, ManifestLock: lock}
	if err = cp.InstallCpakWithOptions(origin, manifest, request.Mode, "", "", options); err != nil {
		return err
	}
	app, err := installedApplication(&cp, origin, request.Mode)
	if err != nil {
		return err
	}
	if err = cp.ValidateApplicationFiles(app); err != nil {
		return err
	}
	if !request.Launch || request.Binary == "" {
		return nil
	}
	defer func() {
		if stopErr := cp.Stop(origin, "", request.Mode, "", ""); resultErr == nil && stopErr != nil {
			resultErr = stopErr
		}
	}()
	return cp.Run(origin, "", request.Mode, "", "", request.Binary, request.Verbose, request.Extra...)
}

type savedEnvironment struct {
	name    string
	value   string
	present bool
}

func useIsolatedCpakEnvironment() (func() error, error) {
	root, err := os.MkdirTemp("", "cpak-local-")
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"CPAK_INSTALLATION_PATH":  root,
		"CPAK_BIN_PATH":           filepath.Join(root, "bin"),
		"CPAK_MANIFESTS_PATH":     filepath.Join(root, "manifests"),
		"CPAK_EXPORTS_PATH":       filepath.Join(root, "exports"),
		"CPAK_STORE_PATH":         filepath.Join(root, "store"),
		"CPAK_CACHE_PATH":         filepath.Join(root, "cache"),
		"CPAK_DABADEE_STORE_ROOT": filepath.Join(root, "dabadee"),
		"CPAK_OPTS_FILE":          filepath.Join(root, "no-config.json"),
		"CPAK_SERVICE_SOCKET":     filepath.Join(root, "cpak.sock"),
	}
	saved := make([]savedEnvironment, 0, len(values))
	for name, value := range values {
		previous, present := os.LookupEnv(name)
		saved = append(saved, savedEnvironment{name: name, value: previous, present: present})
		if err = os.Setenv(name, value); err != nil {
			restoreEnvironment(saved)
			_ = os.RemoveAll(root)
			return nil, err
		}
	}
	return func() error {
		restoreEnvironment(saved)
		return os.RemoveAll(root)
	}, nil
}

func restoreEnvironment(saved []savedEnvironment) {
	for index := len(saved) - 1; index >= 0; index-- {
		entry := saved[index]
		if entry.present {
			_ = os.Setenv(entry.name, entry.value)
		} else {
			_ = os.Unsetenv(entry.name)
		}
	}
}
