/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"github.com/mirkobrombin/go-struct-flags/v1/binder"
)

type OverrideCmd struct {
	AppOrigin  string `arg:"app_origin" help:"APP_ORIGIN or edit"`
	EditOrigin string `arg:"edit_origin" help:"APP_ORIGIN when using edit"`
	Key        string `cli:"key,k" help:"Override key (required)"`
	Value      string `cli:"value,v" help:"Override value (required)"`
	Edit       bool   `cli:"edit" help:"Edit override JSON with VISUAL or EDITOR"`

	cli.Base
}

func (c *OverrideCmd) Run() error {
	appOrigin := c.AppOrigin
	edit := c.Edit
	if strings.EqualFold(appOrigin, "edit") {
		appOrigin = c.EditOrigin
		edit = true
	} else if c.EditOrigin != "" {
		return fmt.Errorf("unexpected argument %q", c.EditOrigin)
	}
	appOrigin = strings.ToLower(appOrigin)

	if appOrigin == "" {
		return fmt.Errorf("application origin is required")
	}
	if edit && (c.Key != "" || c.Value != "") {
		return fmt.Errorf("edit cannot be combined with key or value")
	}
	if !edit && (c.Key == "" || c.Value == "") {
		return fmt.Errorf("key and value are required")
	}

	// Initialize cpak and store
	cpk, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	store, err := cpak.NewStore(cpk.Options.StorePath)
	if err != nil {
		return err
	}
	// The re-enrolment below opens the store itself, and the write-ahead log
	// refuses a second handle while this one is still open. The handle is
	// therefore released once, either here or by the enrolment, whichever
	// comes first.
	releaseStore := sync.OnceFunc(func() { store.Close() })
	defer releaseStore()

	apps, err := store.GetApplications()
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("no cpak applications installed")
	}

	// Find the application by origin
	var sel types.Application
	for _, a := range apps {
		if a.Origin == appOrigin {
			sel = a
			break
		}
	}
	if sel.Origin == "" {
		return fmt.Errorf("application %q not found", appOrigin)
	}

	// Load existing override or fallback to manifest
	over := sel.ParsedOverride
	if userO, err := cpak.LoadOverride(appOrigin, sel.Version); err == nil {
		over = userO
	}
	if edit {
		over, err = editOverride(over)
		if err != nil {
			return err
		}
		if err = validateOverride(over); err != nil {
			return err
		}
		if err = saveOverrideAndEnrol(cpk, over, appOrigin, sel, releaseStore); err != nil {
			return err
		}
		c.Logger.Success("Override saved for %s", appOrigin)
		return nil
	}
	if c.Key == "filesystem" {
		permissions, err := types.DecodeFilesystemPermissionsJSON([]byte(c.Value))
		if err != nil {
			return err
		}
		over.Filesystem = permissions
		if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel, releaseStore); err != nil {
			return err
		}
		c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
		return nil
	}
	if strings.Contains(c.Key, ".") || json.Valid([]byte(c.Value)) {
		if err = applyOverrideJSONValue(&over, c.Key, []byte(c.Value)); err != nil {
			return err
		}
		if err = validateOverride(over); err != nil {
			return err
		}
		if err = saveOverrideAndEnrol(cpk, over, appOrigin, sel, releaseStore); err != nil {
			return err
		}
		c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
		return nil
	}
	if c.Key == "hostActions" {
		actions, err := types.DecodeHostActionsJSON([]byte(c.Value))
		if err != nil {
			return err
		}
		over.HostActions = actions
		if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel, releaseStore); err != nil {
			return err
		}
		c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
		return nil
	}
	if c.Key == "filePicker" {
		grant, err := types.DecodeFilePickerGrantJSON([]byte(c.Value))
		if err != nil {
			return err
		}
		over.FilePicker = grant
		if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel, releaseStore); err != nil {
			return err
		}
		c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
		return nil
	}

	// Initialize the flag binder
	b, err := binder.NewBinder(&over, os.TempDir(), true)
	if err != nil {
		return err
	}

	argsList := []string{c.Value}
	if c.Key == "fsExtra" || c.Key == "env" {
		argsList = strings.Split(c.Value, ":")
	}

	// Register the key with the binder
	if err := b.Run(c.Key, argsList); err != nil {
		return err
	}
	if err = validateOverride(over); err != nil {
		return err
	}

	// Save the override
	if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel, releaseStore); err != nil {
		return err
	}

	c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
	return nil
}

const overrideEditorSizeLimit = 1 << 20

func applyOverrideJSONValue(override *types.Override, key string, value []byte) error {
	parts := strings.Split(key, ".")
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("invalid override key %q", key)
		}
	}

	encoded, err := json.Marshal(override)
	if err != nil {
		return err
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err = decoder.Decode(&document); err != nil {
		return err
	}
	var replacement any
	decoder = json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err = decoder.Decode(&replacement); err != nil {
		return fmt.Errorf("decode value for %s: %w", key, err)
	}
	if err = requireJSONEnd(decoder); err != nil {
		return fmt.Errorf("decode value for %s: %w", key, err)
	}

	current := document
	for _, part := range parts[:len(parts)-1] {
		next, exists := current[part]
		if !exists || next == nil {
			child := map[string]any{}
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("override key %q is not an object", strings.Join(parts[:len(parts)-1], "."))
		}
		current = child
	}
	current[parts[len(parts)-1]] = replacement

	encoded, err = json.Marshal(document)
	if err != nil {
		return err
	}
	updated, err := decodeOverrideJSON(encoded)
	if err != nil {
		return fmt.Errorf("set override %s: %w", key, err)
	}
	*override = updated
	return nil
}

func editOverride(override types.Override) (types.Override, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	arguments := strings.Fields(editor)
	if len(arguments) == 0 {
		return types.Override{}, fmt.Errorf("VISUAL or EDITOR is required")
	}
	file, err := os.CreateTemp("", "cpak-override-*.json")
	if err != nil {
		return types.Override{}, err
	}
	path := file.Name()
	defer os.Remove(path)
	encoded, err := json.MarshalIndent(override, "", "  ")
	if err == nil {
		_, err = file.Write(append(encoded, '\n'))
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return types.Override{}, err
	}

	command := exec.Command(arguments[0], append(arguments[1:], path)...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err = command.Run(); err != nil {
		return types.Override{}, fmt.Errorf("edit override: %w", err)
	}
	file, err = os.Open(path)
	if err != nil {
		return types.Override{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, overrideEditorSizeLimit+1))
	if err != nil {
		return types.Override{}, err
	}
	if len(data) > overrideEditorSizeLimit {
		return types.Override{}, fmt.Errorf("edited override exceeds %d bytes", overrideEditorSizeLimit)
	}
	return decodeOverrideJSON(data)
}

func decodeOverrideJSON(data []byte) (types.Override, error) {
	var override types.Override
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&override); err != nil {
		return types.Override{}, err
	}
	if err := requireJSONEnd(decoder); err != nil {
		return types.Override{}, err
	}
	return override, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateOverride(override types.Override) error {
	if err := types.ValidateHostActions(override.HostActions); err != nil {
		return err
	}
	if err := types.ValidateFilePickerGrant(override.FilePicker); err != nil {
		return err
	}
	if err := types.ValidateNetworkPermissions(override); err != nil {
		return err
	}
	if err := types.ValidateFilesystemPermissions(override.Filesystem); err != nil {
		return err
	}
	if err := types.ValidateClipboardGrant(override.Clipboard, override.DisplayX11); err != nil {
		return err
	}
	return types.ValidateDBusPolicy(override.SessionBus)
}

// saveOverrideAndEnrol keeps the anchor in step with the policy. A saved
// override changes the root a launch derives, so without re-enrolling here the
// application keeps working exactly until the next launch, which then finds a
// root the ledger does not hold and refuses it at every enforcement level.
func saveOverrideAndEnrol(cpk cpak.Cpak, over types.Override, origin string, app types.Application, releaseStore func()) error {
	if err := cpak.SaveOverride(over, origin, app.Version); err != nil {
		return err
	}
	releaseStore()

	// The override is on disk by now, so an enrolment that did not happen is
	// exactly the state this function exists to prevent, and it has to be said
	// out loud rather than returned as success. Reporting it costs a message;
	// not reporting it costs an application that refuses to launch and a user
	// with no idea why.
	enrolment := cpk.EnrolApplication(app)
	switch enrolment.Outcome {
	case cpak.EnrolmentRecorded, cpak.EnrolmentUnchanged:
		return nil
	}
	if enrolment.Advice != "" {
		return fmt.Errorf("the override was saved and %s could not be re-enrolled (%s): %w. %s",
			origin, enrolment.Outcome, enrolment.Reason, enrolment.Advice)
	}
	return fmt.Errorf("the override was saved and %s could not be re-enrolled (%s): %w",
		origin, enrolment.Outcome, enrolment.Reason)
}
