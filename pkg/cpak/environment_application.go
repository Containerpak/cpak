/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const environmentApplicationRecordSizeLimit = 16 << 10
const environmentApplicationIconSizeLimit = 1 << 20

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

type environmentApplicationExportRecord struct {
	Application string `json:"application"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	Icon        bool   `json:"icon"`
}

func validateEnvironmentApplicationID(application string) error {
	if application == "" || len(application) > 4096 || !path.IsAbs(application) || path.Clean(application) != application || !strings.HasSuffix(application, ".desktop") || strings.Contains(application, "\\") {
		return errors.New("environment application identifier is invalid")
	}
	for _, character := range application {
		if unicode.IsControl(character) {
			return errors.New("environment application identifier is invalid")
		}
	}
	return nil
}

func validateEnvironmentApplicationExport(application string, export types.EnvironmentApplicationExport) error {
	if err := validateEnvironmentApplicationID(application); err != nil {
		return err
	}
	if export.Name == "" || export.Name != strings.TrimSpace(export.Name) || utf8.RuneCountInString(export.Name) > 240 {
		return errors.New("environment application name must contain 1-240 trimmed characters")
	}
	if utf8.RuneCountInString(export.Description) > 1024 {
		return errors.New("environment application description exceeds 1024 characters")
	}
	if export.Command == "" || export.Command != strings.TrimSpace(export.Command) || len(export.Command) > 4096 {
		return errors.New("environment application command is invalid")
	}
	for _, value := range []string{export.Name, export.Description, export.Command} {
		for _, character := range value {
			if unicode.IsControl(character) {
				return errors.New("environment application metadata contains a control character")
			}
		}
	}
	if len(export.IconPNG) > environmentApplicationIconSizeLimit {
		return fmt.Errorf("environment application icon exceeds %d bytes", environmentApplicationIconSizeLimit)
	}
	if len(export.IconPNG) > 0 && !bytes.HasPrefix(export.IconPNG, pngSignature) {
		return errors.New("environment application icon is not a PNG image")
	}
	if _, _, err := environmentApplicationCommand(export.Command); err != nil {
		return err
	}
	return nil
}

func environmentApplicationCommand(command string) (string, string, error) {
	command = strings.TrimSpace(command)
	var binary strings.Builder
	quoted := false
	escaped := false
	for index, character := range command {
		switch {
		case escaped:
			binary.WriteRune(character)
			escaped = false
		case character == '\\':
			escaped = true
		case character == '"':
			quoted = !quoted
		case !quoted && (character == ' ' || character == '\t'):
			if binary.Len() == 0 {
				continue
			}
			return binary.String(), strings.TrimSpace(command[index:]), nil
		default:
			binary.WriteRune(character)
		}
	}
	if escaped || quoted || binary.Len() == 0 {
		return "", "", errors.New("environment application command is malformed")
	}
	return binary.String(), "", nil
}

func environmentApplicationExportKey(environmentID, application string) string {
	digest := sha256.Sum256([]byte(environmentID + "\n" + application))
	return hex.EncodeToString(digest[:])
}

func environmentApplicationExportName(environmentID, application string) string {
	return "cpak-environment-" + environmentApplicationExportKey(environmentID, application)
}

func environmentApplicationDesktopPath(environmentID, application string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "applications", environmentApplicationExportName(environmentID, application)+".desktop"), nil
}

func environmentApplicationIconPath(environmentID, application string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "icons", "hicolor", "256x256", "apps", environmentApplicationExportName(environmentID, application)+".png"), nil
}

func (c *Cpak) environmentApplicationRecordPath(environment types.Environment, application string) (string, error) {
	directory, err := c.environmentPath(environment.ID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "application-exports", environmentApplicationExportKey(environment.ID, application)+".json"), nil
}

func renderEnvironmentApplicationDesktop(launcher string, environment types.Environment, record environmentApplicationExportRecord) ([]byte, error) {
	binary, arguments, err := environmentApplicationCommand(record.Command)
	if err != nil {
		return nil, err
	}
	exec := "Exec=" + desktopExecArgument(launcher) + " environment shell --environment " + environment.ID + " --command " + desktopExecArgument("@"+binary)
	if arguments != "" {
		exec += " -- " + arguments
	}
	lines := []string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=" + desktopEntryString(record.Name),
	}
	if record.Description != "" {
		lines = append(lines, "Comment="+desktopEntryString(record.Description))
	}
	lines = append(lines, exec)
	if record.Icon {
		lines = append(lines, "Icon="+environmentApplicationExportName(environment.ID, record.Application))
	}
	lines = append(lines,
		"TryExec="+launcher,
		"Terminal=false",
		"Categories=Utility;",
		"X-cpak-Environment="+environment.ID,
		"X-cpak-Environment-Application="+record.Application,
		"",
	)
	return []byte(strings.Join(lines, "\n")), nil
}

func desktopEntryString(value string) string {
	return strings.ReplaceAll(value, "\\", "\\\\")
}

func (c *Cpak) ExportEnvironmentApplication(value, application string, export types.EnvironmentApplicationExport) (types.EnvironmentApplicationExportState, error) {
	if err := validateEnvironmentApplicationExport(application, export); err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	unlock, err := c.lockContainerScope(environmentContainerScope(environment.ID))
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	defer unlock()
	environment, err = c.GetEnvironment(environment.ID)
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}

	record := environmentApplicationExportRecord{
		Application: application,
		Name:        export.Name,
		Description: export.Description,
		Command:     export.Command,
		Icon:        len(export.IconPNG) > 0,
	}
	launcher, err := desktopLauncherPath()
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	content, err := renderEnvironmentApplicationDesktop(launcher, environment, record)
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	desktopPath, err := environmentApplicationDesktopPath(environment.ID, application)
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	if err = refuseUnownedEnvironmentApplicationDesktop(desktopPath, environment.ID, application); err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	if err = os.MkdirAll(filepath.Dir(desktopPath), 0o755); err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	iconPath, err := environmentApplicationIconPath(environment.ID, application)
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	if record.Icon {
		if err = os.MkdirAll(filepath.Dir(iconPath), 0o755); err != nil {
			return types.EnvironmentApplicationExportState{}, err
		}
		if err = writeDesktopLauncher(iconPath, export.IconPNG, 0o644); err != nil {
			return types.EnvironmentApplicationExportState{}, err
		}
	} else if err = os.Remove(iconPath); err != nil && !os.IsNotExist(err) {
		return types.EnvironmentApplicationExportState{}, err
	}
	if err = writeDesktopLauncher(desktopPath, content, 0o644); err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	if err = c.writeEnvironmentApplicationRecord(environment, record); err != nil {
		_ = removeOwnedEnvironmentApplicationDesktop(desktopPath, environment.ID, application)
		_ = os.Remove(iconPath)
		return types.EnvironmentApplicationExportState{}, err
	}
	if err = refreshDesktopDatabase(); err != nil {
		logger.Printf("Warning: could not refresh the desktop database: %v", err)
	}
	return types.EnvironmentApplicationExportState{Application: application, Exported: true}, nil
}

func (c *Cpak) writeEnvironmentApplicationRecord(environment types.Environment, record environmentApplicationExportRecord) error {
	path, err := c.environmentApplicationRecordPath(environment, record.Application)
	if err != nil {
		return err
	}
	if err = securePrivateDirectoryUnder(c.Options.StorePath, filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > environmentApplicationRecordSizeLimit {
		return errors.New("environment application export record is too large")
	}
	return writeDesktopLauncher(path, data, 0o600)
}

func refuseUnownedEnvironmentApplicationDesktop(path, environmentID, application string) error {
	owned, err := environmentApplicationDesktopOwned(path, environmentID, application)
	if err != nil {
		return err
	}
	if !owned {
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			return nil
		}
		return errors.New("desktop entry is not owned by this environment")
	}
	return nil
}

func environmentApplicationDesktopOwned(path, environmentID, application string) (bool, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return desktopEntryValue(content, "X-cpak-Environment") == environmentID && desktopEntryValue(content, "X-cpak-Environment-Application") == application, nil
}

func removeOwnedEnvironmentApplicationDesktop(path, environmentID, application string) error {
	owned, err := environmentApplicationDesktopOwned(path, environmentID, application)
	if err != nil {
		return err
	}
	if !owned {
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			return nil
		}
		return errors.New("desktop entry is not owned by this environment")
	}
	if err = os.Remove(path); os.IsNotExist(err) {
		return nil
	}
	return err
}

func (c *Cpak) RemoveEnvironmentApplicationExport(value, application string) (types.EnvironmentApplicationExportState, error) {
	if err := validateEnvironmentApplicationID(application); err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	unlock, err := c.lockContainerScope(environmentContainerScope(environment.ID))
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	defer unlock()
	environment, err = c.GetEnvironment(environment.ID)
	if err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	if err = c.removeEnvironmentApplicationExport(environment, application); err != nil {
		return types.EnvironmentApplicationExportState{}, err
	}
	if err = refreshDesktopDatabase(); err != nil {
		logger.Printf("Warning: could not refresh the desktop database: %v", err)
	}
	return types.EnvironmentApplicationExportState{Application: application, Exported: false}, nil
}

func (c *Cpak) removeEnvironmentApplicationExport(environment types.Environment, application string) error {
	desktopPath, err := environmentApplicationDesktopPath(environment.ID, application)
	if err != nil {
		return err
	}
	if err = removeOwnedEnvironmentApplicationDesktop(desktopPath, environment.ID, application); err != nil {
		return err
	}
	iconPath, err := environmentApplicationIconPath(environment.ID, application)
	if err != nil {
		return err
	}
	if err = os.Remove(iconPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	recordPath, err := c.environmentApplicationRecordPath(environment, application)
	if err != nil {
		return err
	}
	if err = os.Remove(recordPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c *Cpak) ListEnvironmentApplicationExports(value string) ([]string, error) {
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return nil, err
	}
	unlock, err := c.lockContainerScope(environmentContainerScope(environment.ID))
	if err != nil {
		return nil, err
	}
	defer unlock()
	environment, err = c.GetEnvironment(environment.ID)
	if err != nil {
		return nil, err
	}
	records, err := c.environmentApplicationExportRecords(environment)
	if err != nil {
		return nil, err
	}
	applications := make([]string, 0, len(records))
	for _, record := range records {
		desktopPath, pathErr := environmentApplicationDesktopPath(environment.ID, record.Application)
		if pathErr != nil {
			return nil, pathErr
		}
		owned, ownedErr := environmentApplicationDesktopOwned(desktopPath, environment.ID, record.Application)
		if ownedErr != nil {
			return nil, ownedErr
		}
		if owned {
			applications = append(applications, record.Application)
		}
	}
	sort.Strings(applications)
	return applications, nil
}

func (c *Cpak) environmentApplicationExportRecords(environment types.Environment) ([]environmentApplicationExportRecord, error) {
	directory, err := c.environmentPath(environment.ID)
	if err != nil {
		return nil, err
	}
	directory = filepath.Join(directory, "application-exports")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []environmentApplicationExportRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]environmentApplicationExportRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, readErr := readEnvironmentApplicationExportRecord(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		if err = validateEnvironmentApplicationExport(record.Application, types.EnvironmentApplicationExport{Name: record.Name, Description: record.Description, Command: record.Command}); err != nil {
			return nil, err
		}
		if entry.Name() != environmentApplicationExportKey(environment.ID, record.Application)+".json" {
			return nil, errors.New("environment application export record has an invalid name")
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool { return records[left].Application < records[right].Application })
	return records, nil
}

func readEnvironmentApplicationExportRecord(path string) (environmentApplicationExportRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return environmentApplicationExportRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > environmentApplicationRecordSizeLimit {
		return environmentApplicationExportRecord{}, errors.New("environment application export record is not a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return environmentApplicationExportRecord{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, environmentApplicationRecordSizeLimit))
	decoder.DisallowUnknownFields()
	record := environmentApplicationExportRecord{}
	if err = decoder.Decode(&record); err != nil {
		return environmentApplicationExportRecord{}, err
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return environmentApplicationExportRecord{}, errors.New("environment application export record contains multiple JSON values")
	}
	return record, nil
}

func (c *Cpak) removeAllEnvironmentApplicationExports(environment types.Environment) error {
	records, err := c.environmentApplicationExportRecords(environment)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err = c.removeEnvironmentApplicationExport(environment, record.Application); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cpak) repairEnvironmentApplicationLaunchers(launcher string) error {
	environments, err := c.ListEnvironments()
	if err != nil {
		return err
	}
	for _, environment := range environments {
		records, recordErr := c.environmentApplicationExportRecords(environment)
		if recordErr != nil {
			return recordErr
		}
		for _, record := range records {
			desktopPath, pathErr := environmentApplicationDesktopPath(environment.ID, record.Application)
			if pathErr != nil {
				return pathErr
			}
			owned, ownedErr := environmentApplicationDesktopOwned(desktopPath, environment.ID, record.Application)
			if ownedErr != nil {
				return ownedErr
			}
			if !owned {
				continue
			}
			content, renderErr := renderEnvironmentApplicationDesktop(launcher, environment, record)
			if renderErr != nil {
				return renderErr
			}
			if writeErr := writeDesktopLauncher(desktopPath, content, 0o644); writeErr != nil {
				return writeErr
			}
		}
	}
	return nil
}
