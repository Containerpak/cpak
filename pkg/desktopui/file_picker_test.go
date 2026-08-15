/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"image"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestDecodePortalFilePickerResponse(t *testing.T) {
	result, err := decodePortalFilePickerResponse([]any{
		uint32(0),
		map[string]dbus.Variant{
			"uris":    dbus.MakeVariant([]string{"file:///home/test/Game/setup.exe"}),
			"choices": dbus.MakeVariant([][2]string{{"cpak-context", "true"}, {"cpak-lifetime", "persistent"}}),
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != "/home/test/Game/setup.exe" || len(result.Paths) != 1 || !result.ContainingFolder || !result.Persistent {
		t.Fatalf("file picker result: %+v", result)
	}
	if !result.contextSet || !result.persistentSet {
		t.Fatal("portal choices were not marked as handled")
	}
}

func TestPortalResultWithoutChoicesRequiresGrantConfirmation(t *testing.T) {
	result, err := decodePortalFilePickerResponse([]any{
		uint32(0),
		map[string]dbus.Variant{"uris": dbus.MakeVariant([]string{"file:///home/test/Game/setup.exe"})},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	request := FilePickerRequest{Mode: PickerOpenFile, Title: "Select a file", OfferPersistent: true, OfferContainingFolder: true}
	pending := request
	pending.OfferContainingFolder = request.OfferContainingFolder && !result.contextSet
	pending.OfferPersistent = request.OfferPersistent && !result.persistentSet
	if !pending.OfferContainingFolder || !pending.OfferPersistent {
		t.Fatal("missing portal choices did not require confirmation")
	}
}

func TestFileGrantControlsFollowAvailableChoices(t *testing.T) {
	request := FilePickerRequest{OfferPersistent: true, OfferContainingFolder: true}
	if got := fileGrantControlAt(image.Pt(150, 210), 560, 430, request); got != 0 {
		t.Fatalf("context control: %d", got)
	}
	if got := fileGrantControlAt(image.Pt(150, 254), 560, 430, request); got != 1 {
		t.Fatalf("persistent control: %d", got)
	}
}

func TestFilePickerRequestRejectsMultipleSelectionPatterns(t *testing.T) {
	err := ValidateFilePickerRequest(FilePickerRequest{
		Mode:  PickerOpenFile,
		Title: "Select a file",
		Filters: []FilePickerFilter{{
			Name:     "Invalid",
			Patterns: []string{"*.exe\n*.msi"},
		}},
	})
	if err == nil {
		t.Fatal("invalid file picker filter was accepted")
	}
}

func TestFilePickerRequestRejectsInvalidApplication(t *testing.T) {
	err := ValidateFilePickerRequest(FilePickerRequest{Mode: PickerOpenFile, Title: "Select a file", Application: "Viewer\nSpoofed"})
	if err == nil {
		t.Fatal("invalid application was accepted")
	}
}

func TestFilePickerRequestValidatesCurrentFolder(t *testing.T) {
	if err := ValidateFilePickerRequest(FilePickerRequest{Mode: PickerOpenFile, Title: "Select a file", CurrentFolder: "/home/test/Downloads"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFilePickerRequest(FilePickerRequest{Mode: PickerOpenFile, Title: "Select a file", CurrentFolder: "Downloads"}); err == nil {
		t.Fatal("relative current folder was accepted")
	}
}

func TestFilePickerFailsClosedWithoutDesktopBackend(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("PATH", t.TempDir())
	_, err := PickFile(t.Context(), FilePickerRequest{Mode: PickerOpenFile, Title: "Select a file"})
	if err == nil || !strings.Contains(err.Error(), "headless") {
		t.Fatalf("headless picker: %v", err)
	}
}
