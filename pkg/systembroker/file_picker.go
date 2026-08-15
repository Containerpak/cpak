/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
)

func executeFilePicker(ctx context.Context, payload json.RawMessage, options Options, writer *frameWriter) (int, error) {
	if !options.FilePicker.Enabled() {
		return 0, errors.New("file selection is not permitted")
	}
	var request FilePickerRequest
	if err := decodePayload(payload, &request); err != nil {
		return 0, err
	}
	if err := validateFilePickerRequest(request, options.FilePicker); err != nil {
		return 0, err
	}
	pickerRequest := desktopui.FilePickerRequest{
		Mode:                  request.Mode,
		Application:           options.FilePickerApplication,
		ParentWindow:          request.ParentWindow,
		Title:                 request.Title,
		AcceptLabel:           request.AcceptLabel,
		SuggestedName:         request.SuggestedName,
		CurrentFolder:         request.CurrentFolder,
		Multiple:              request.Multiple,
		OfferPersistent:       options.FilePicker.Persistent,
		OfferContainingFolder: options.FilePicker.ContainingFolder,
	}
	for _, filter := range request.Filters {
		pickerRequest.Filters = append(pickerRequest.Filters, desktopui.FilePickerFilter{Name: filter.Name, Patterns: filter.Patterns, MIMETypes: filter.MIMETypes})
	}
	selectionRequest := pickerRequest
	selectionRequest.OfferPersistent = false
	selectionRequest.OfferContainingFolder = false
	pick := options.PickFile
	if pick == nil {
		pick = desktopui.PickFile
	}
	selected, err := pick(ctx, selectionRequest)
	if err != nil {
		return 0, err
	}
	paths := selected.Paths
	if len(paths) == 0 && selected.Path != "" {
		paths = []string{selected.Path}
	}
	if len(paths) == 0 || !request.Multiple && len(paths) != 1 {
		return 0, errors.New("file picker returned an invalid path count")
	}
	if len(paths) > 128 {
		return 0, errors.New("file picker returned too many paths")
	}
	allDirect := true
	for _, path := range paths {
		if _, direct := directFilePickerTarget(path, request.Mode, false, options.FilePickerPaths); !direct {
			allDirect = false
			break
		}
	}
	if !allDirect {
		confirm := options.ConfirmFileGrant
		if confirm == nil {
			confirm = desktopui.ConfirmFileGrant
		}
		selected, err = confirm(ctx, selected, pickerRequest)
		if err != nil {
			return 0, err
		}
	}
	lifetime := filegrant.LifetimeSession
	if selected.Persistent && options.FilePicker.Persistent {
		lifetime = filegrant.LifetimePersistent
	}
	targets := make([]string, 0, len(paths))
	kind := filegrant.KindFile
	access := filegrant.AccessReadOnly
	resultLifetime := lifetime
	containingFolder := false
	for index, path := range paths {
		if target, direct := directFilePickerTarget(path, request.Mode, selected.ContainingFolder, options.FilePickerPaths); direct {
			targets = append(targets, target)
			if index == 0 {
				kind, access = directFilePickerProperties(path, request.Mode)
				resultLifetime = filegrant.LifetimePersistent
				containingFolder = selected.ContainingFolder
			}
			continue
		}
		var grant filegrant.Grant
		if request.Mode == desktopui.PickerSaveFile {
			grant, err = filegrant.ResolveSave(options.FilePickerOrigin, path, lifetime)
		} else {
			grant, err = filegrant.Resolve(options.FilePickerOrigin, path, filegrant.AccessReadOnly, lifetime, selected.ContainingFolder && options.FilePicker.ContainingFolder)
		}
		if err != nil {
			return 0, err
		}
		source, openErr := filegrant.OpenSource(grant)
		if openErr != nil {
			return 0, openErr
		}
		mountSource, mountOpenErr := filegrant.OpenMountSource(grant)
		if mountOpenErr != nil {
			_ = source.Close()
			return 0, mountOpenErr
		}
		_, sendErr := grantproto.Send(options.FileGrantSocketPath, grant, source, mountSource)
		closeErr := source.Close()
		if mountSource != nil {
			if err = mountSource.Close(); closeErr == nil {
				closeErr = err
			}
		}
		if sendErr != nil {
			return 0, sendErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if grant.Lifetime == filegrant.LifetimePersistent {
			store := filegrant.Store{Directory: options.FileGrantStorePath}
			if err = store.Add(grant); err != nil {
				return 0, err
			}
		}
		targets = append(targets, grant.Target)
		if index == 0 {
			kind = grant.Kind
			access = grant.Access
			resultLifetime = grant.Lifetime
			containingFolder = grant.Source != grant.Selection
		}
	}
	result := FilePickerResult{
		Path:             targets[0],
		Paths:            targets,
		Kind:             kind,
		Access:           access,
		Lifetime:         resultLifetime,
		ContainingFolder: containingFolder,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	if _, err = writer.stdout().Write(append(encoded, '\n')); err != nil {
		return 0, fmt.Errorf("write file picker result: %w", err)
	}
	return 0, nil
}

func directFilePickerTarget(selected, mode string, containingFolder bool, paths []FilePickerPathGrant) (string, bool) {
	selected = filepath.Clean(selected)
	if !filepath.IsAbs(selected) || strings.ContainsRune(selected, '\x00') {
		return "", false
	}
	scope := selected
	if mode == desktopui.PickerOpenFile && containingFolder {
		scope = filepath.Dir(selected)
	}
	for _, grant := range paths {
		if mode == desktopui.PickerSaveFile && grant.ReadOnly {
			continue
		}
		relative, ok := relativeFilePickerPath(grant.Source, scope)
		if !ok {
			continue
		}
		target := grant.Target
		if relative != "." {
			target = filepath.Join(target, relative)
		}
		if scope != selected {
			target = filepath.Join(target, filepath.Base(selected))
		}
		return target, true
	}
	return "", false
}

func relativeFilePickerPath(root, selected string) (string, bool) {
	root = filepath.Clean(root)
	selected = filepath.Clean(selected)
	if relative, err := filepath.Rel(root, selected); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return relative, true
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", false
	}
	current := selected
	parts := []string{}
	for {
		if info, statErr := os.Stat(current); statErr == nil && os.SameFile(rootInfo, info) {
			if len(parts) == 0 {
				return ".", true
			}
			return filepath.Join(parts...), true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		parts = append([]string{filepath.Base(current)}, parts...)
		current = parent
	}
}

func directFilePickerProperties(path, mode string) (string, string) {
	if mode == desktopui.PickerSaveFile {
		return filegrant.KindDirectory, filegrant.AccessReadWrite
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return filegrant.KindDirectory, filegrant.AccessReadOnly
	}
	return filegrant.KindFile, filegrant.AccessReadOnly
}

func validateFilePickerRequest(request FilePickerRequest, policy FilePickerPolicy) error {
	switch request.Mode {
	case desktopui.PickerOpenFile:
		if !policy.OpenFile {
			return errors.New("selecting files is not permitted")
		}
	case desktopui.PickerOpenFolder:
		if !policy.OpenFolder {
			return errors.New("selecting folders is not permitted")
		}
	case desktopui.PickerSaveFile:
		if !policy.SaveFile {
			return errors.New("selecting save destinations is not permitted")
		}
	default:
		return errors.New("unsupported file picker mode")
	}
	filters := make([]desktopui.FilePickerFilter, 0, len(request.Filters))
	for _, filter := range request.Filters {
		filters = append(filters, desktopui.FilePickerFilter{Name: filter.Name, Patterns: filter.Patterns, MIMETypes: filter.MIMETypes})
	}
	return desktopui.ValidateFilePickerRequest(desktopui.FilePickerRequest{
		Mode:          request.Mode,
		ParentWindow:  request.ParentWindow,
		Title:         request.Title,
		AcceptLabel:   request.AcceptLabel,
		SuggestedName: request.SuggestedName,
		CurrentFolder: request.CurrentFolder,
		Multiple:      request.Multiple,
		Filters:       filters,
	})
}
