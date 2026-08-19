/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	PickerOpenFile   = "open-file"
	PickerOpenFolder = "open-folder"
	PickerSaveFile   = "save-file"
	maxPickerFilters = 32
	maxPickerPaths   = 128
)

type FilePickerRequest struct {
	Mode                  string
	Application           string
	ParentWindow          string
	Title                 string
	AcceptLabel           string
	SuggestedName         string
	CurrentFolder         string
	Multiple              bool
	Filters               []FilePickerFilter
	OfferPersistent       bool
	OfferContainingFolder bool
}

type FilePickerFilter struct {
	Name      string
	Patterns  []string
	MIMETypes []string
}

type FilePickerResult struct {
	Path             string
	Paths            []string
	Persistent       bool
	ContainingFolder bool
	persistentSet    bool
	contextSet       bool
}

type portalFilter struct {
	Name  string
	Rules []portalFilterRule
}

type portalFilterRule struct {
	Kind    uint32
	Pattern string
}

type portalChoice struct {
	ID      string
	Label   string
	Options []portalChoiceOption
	Initial string
}

type portalChoiceOption struct {
	ID    string
	Label string
}

func PickFile(ctx context.Context, request FilePickerRequest) (FilePickerResult, error) {
	if err := ValidateFilePickerRequest(request); err != nil {
		return FilePickerResult{}, err
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return FilePickerResult{}, errors.New("file picker is unavailable in a headless session")
	}
	result, err := pickFilePortal(ctx, request)
	if err == nil {
		return confirmMissingFileGrantChoices(ctx, result, request)
	}
	if errors.Is(err, ErrCancelled) {
		return result, err
	}
	return pickFileCommand(ctx, request)
}

var ErrCancelled = errors.New("file selection was cancelled")

func ValidateFilePickerRequest(request FilePickerRequest) error {
	if request.Mode != PickerOpenFile && request.Mode != PickerOpenFolder && request.Mode != PickerSaveFile {
		return fmt.Errorf("unsupported file picker mode: %s", request.Mode)
	}
	if strings.TrimSpace(request.Title) == "" || len(request.Title) > 160 {
		return errors.New("file picker title is invalid")
	}
	if len(request.Application) > 160 || strings.ContainsAny(request.Application, "\x00\r\n") {
		return errors.New("file picker application is invalid")
	}
	if len(request.AcceptLabel) > 64 || strings.ContainsAny(request.AcceptLabel, "\x00\r\n") {
		return errors.New("file picker accept label is invalid")
	}
	if request.SuggestedName != "" && filepath.Base(request.SuggestedName) != request.SuggestedName {
		return errors.New("file picker suggested name is invalid")
	}
	if request.CurrentFolder != "" && (!filepath.IsAbs(request.CurrentFolder) || filepath.Clean(request.CurrentFolder) != request.CurrentFolder || strings.ContainsRune(request.CurrentFolder, '\x00')) {
		return errors.New("file picker current folder is invalid")
	}
	if request.Multiple && request.Mode != PickerOpenFile {
		return errors.New("multiple selection requires open-file mode")
	}
	if len(request.Filters) > maxPickerFilters {
		return errors.New("file picker has too many filters")
	}
	for _, filter := range request.Filters {
		if filter.Name == "" || len(filter.Name) > 80 || len(filter.Patterns)+len(filter.MIMETypes) == 0 {
			return errors.New("file picker filter is invalid")
		}
		for _, pattern := range filter.Patterns {
			if pattern == "" || len(pattern) > 128 || strings.ContainsAny(pattern, "\x00\r\n") {
				return errors.New("file picker filter is invalid")
			}
		}
		for _, mimeType := range filter.MIMETypes {
			if mimeType == "" || len(mimeType) > 128 || strings.ContainsAny(mimeType, "\x00\r\n") {
				return errors.New("file picker filter is invalid")
			}
		}
	}
	return nil
}

func pickFilePortal(ctx context.Context, request FilePickerRequest) (FilePickerResult, error) {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return FilePickerResult{}, err
	}
	defer connection.Close()
	signals := make(chan *dbus.Signal, 1)
	connection.Signal(signals)
	defer connection.RemoveSignal(signals)
	// The reply has to come from the portal and from nothing else. An
	// application holding the session bus is a full peer there, so a rule that
	// names only the interface and the member accepts an answer any peer can
	// send, and the answer names the files cpak then grants. The unique name is
	// resolved once and both the rule and the handler are held to it.
	portalOwner, err := portalUniqueName(connection)
	if err != nil {
		return FilePickerResult{}, err
	}
	match := []dbus.MatchOption{
		dbus.WithMatchSender(portalOwner),
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	}
	if err = connection.AddMatchSignal(match...); err != nil {
		return FilePickerResult{}, err
	}
	defer connection.RemoveMatchSignal(match...)
	// The token used to be a clock reading, which is a value anybody can guess
	// and nobody has to steal.
	token, err := pickerHandleToken()
	if err != nil {
		return FilePickerResult{}, err
	}
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"modal":        dbus.MakeVariant(true),
		"multiple":     dbus.MakeVariant(request.Multiple),
		"directory":    dbus.MakeVariant(request.Mode == PickerOpenFolder),
	}
	if request.AcceptLabel != "" {
		options["accept_label"] = dbus.MakeVariant(request.AcceptLabel)
	}
	if request.SuggestedName != "" && request.Mode == PickerSaveFile {
		options["current_name"] = dbus.MakeVariant(request.SuggestedName)
	}
	if request.CurrentFolder != "" {
		options["current_folder"] = dbus.MakeVariant(append([]byte(request.CurrentFolder), 0))
	}
	if filters := portalFilters(request.Filters); len(filters) > 0 {
		options["filters"] = dbus.MakeVariant(filters)
	}
	choices := []portalChoice{}
	if request.OfferContainingFolder && request.Mode == PickerOpenFile {
		choices = append(choices, portalChoice{ID: "cpak-context", Label: "Allow the containing folder as context", Initial: "false"})
	}
	if request.OfferPersistent {
		choices = append(choices, portalChoice{
			ID:    "cpak-lifetime",
			Label: "Access",
			Options: []portalChoiceOption{
				{ID: "session", Label: "For this run"},
				{ID: "persistent", Label: "Always allow"},
			},
			Initial: "session",
		})
	}
	if len(choices) > 0 {
		options["choices"] = dbus.MakeVariant(choices)
	}
	method := "org.freedesktop.portal.FileChooser.OpenFile"
	if request.Mode == PickerSaveFile {
		method = "org.freedesktop.portal.FileChooser.SaveFile"
	}
	object := connection.Object("org.freedesktop.portal.Desktop", dbus.ObjectPath("/org/freedesktop/portal/desktop"))
	var handle dbus.ObjectPath
	if err = object.CallWithContext(ctx, method, 0, request.ParentWindow, request.Title, options).Store(&handle); err != nil {
		return FilePickerResult{}, err
	}
	for {
		select {
		case <-ctx.Done():
			_ = connection.Object("org.freedesktop.portal.Desktop", handle).Call("org.freedesktop.portal.Request.Close", 0).Err
			return FilePickerResult{}, ctx.Err()
		case signal := <-signals:
			if signal == nil || signal.Sender != portalOwner || signal.Path != handle || signal.Name != "org.freedesktop.portal.Request.Response" {
				continue
			}
			return decodePortalFilePickerResponse(signal.Body, request.Multiple)
		}
	}
}

func decodePortalFilePickerResponse(body []any, multiple bool) (FilePickerResult, error) {
	if len(body) != 2 {
		return FilePickerResult{}, errors.New("file picker returned an invalid response")
	}
	response, ok := body[0].(uint32)
	if !ok || response != 0 {
		return FilePickerResult{}, ErrCancelled
	}
	values, ok := body[1].(map[string]dbus.Variant)
	if !ok {
		return FilePickerResult{}, errors.New("file picker returned invalid results")
	}
	uris, ok := values["uris"].Value().([]string)
	if !ok || len(uris) == 0 || !multiple && len(uris) != 1 {
		return FilePickerResult{}, errors.New("file picker returned an invalid path count")
	}
	paths := make([]string, 0, len(uris))
	if len(uris) > maxPickerPaths {
		return FilePickerResult{}, errors.New("file picker returned too many paths")
	}
	for _, uri := range uris {
		parsed, err := url.Parse(uri)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" {
			return FilePickerResult{}, errors.New("file picker returned an unsupported URI")
		}
		paths = append(paths, filepath.Clean(parsed.Path))
	}
	result := FilePickerResult{Path: paths[0], Paths: paths}
	if variant, found := values["choices"]; found {
		if choices := decodePortalChoices(variant.Value()); choices != nil {
			if contextChoice, handled := choices["cpak-context"]; handled {
				result.ContainingFolder = contextChoice == "true"
				result.contextSet = true
			}
			if lifetime, handled := choices["cpak-lifetime"]; handled {
				result.Persistent = lifetime == "persistent"
				result.persistentSet = true
			}
		}
	}
	return result, nil
}

func decodePortalChoices(value any) map[string]string {
	if choices, ok := value.(map[string]string); ok {
		return choices
	}
	pairs := [][2]string{}
	if err := dbus.Store([]any{value}, &pairs); err != nil {
		return nil
	}
	choices := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		choices[pair[0]] = pair[1]
	}
	return choices
}

func portalFilters(filters []FilePickerFilter) []portalFilter {
	result := make([]portalFilter, 0, len(filters))
	for _, filter := range filters {
		rules := make([]portalFilterRule, 0, len(filter.Patterns)+len(filter.MIMETypes))
		for _, pattern := range filter.Patterns {
			rules = append(rules, portalFilterRule{Kind: 0, Pattern: pattern})
		}
		for _, mimeType := range filter.MIMETypes {
			rules = append(rules, portalFilterRule{Kind: 1, Pattern: mimeType})
		}
		result = append(result, portalFilter{Name: filter.Name, Rules: rules})
	}
	return result
}

func pickFileCommand(ctx context.Context, request FilePickerRequest) (FilePickerResult, error) {
	if path, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--title=" + request.Title}
		if request.Mode == PickerOpenFolder {
			args = append(args, "--directory")
		}
		if request.Mode == PickerSaveFile {
			args = append(args, "--save", "--confirm-overwrite")
		}
		if request.CurrentFolder != "" {
			filename := request.CurrentFolder + string(filepath.Separator)
			if request.SuggestedName != "" && request.Mode == PickerSaveFile {
				filename = filepath.Join(request.CurrentFolder, request.SuggestedName)
			}
			args = append(args, "--filename="+filename)
		} else if request.SuggestedName != "" && request.Mode == PickerSaveFile {
			args = append(args, "--filename="+request.SuggestedName)
		}
		if request.Multiple {
			args = append(args, "--multiple", "--separator=\n")
		}
		for _, filter := range request.Filters {
			args = append(args, "--file-filter="+filter.Name+" | "+strings.Join(filter.Patterns, " "))
		}
		output, runErr := exec.CommandContext(ctx, path, args...).Output()
		if runErr != nil {
			return FilePickerResult{}, ErrCancelled
		}
		selected := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(selected) == 0 || selected[0] == "" || len(selected) > maxPickerPaths {
			return FilePickerResult{}, errors.New("file picker returned an invalid path count")
		}
		result, confirmErr := ConfirmFileGrant(ctx, FilePickerResult{Path: selected[0], Paths: []string{selected[0]}}, request)
		if confirmErr != nil {
			return FilePickerResult{}, confirmErr
		}
		result.Paths = selected
		return result, nil
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return FilePickerResult{}, errors.New("file picker is unavailable in a headless session")
	}
	return FilePickerResult{}, errors.New("no native file picker backend is available")
}

func confirmMissingFileGrantChoices(ctx context.Context, result FilePickerResult, request FilePickerRequest) (FilePickerResult, error) {
	pending := request
	pending.OfferContainingFolder = request.OfferContainingFolder && request.Mode == PickerOpenFile && !result.contextSet
	pending.OfferPersistent = request.OfferPersistent && !result.persistentSet
	return ConfirmFileGrant(ctx, result, pending)
}

func ConfirmFileGrant(ctx context.Context, result FilePickerResult, request FilePickerRequest) (FilePickerResult, error) {
	if !request.OfferContainingFolder && !request.OfferPersistent {
		return result, nil
	}
	backend := SelectBackend("")
	if backend != BackendBuiltin {
		response, err := runAdapterPrompt(ctx, backend, adapterPrompt{
			Title: "cpak file access", Heading: "Allow access?", Application: request.Application,
			Resource: result.Path, AcceptLabel: "Allow", CancelLabel: "Deny",
			OfferParent: request.OfferContainingFolder, OfferPersistent: request.OfferPersistent,
			ParentSelected: result.ContainingFolder, PersistentChosen: result.Persistent,
		})
		if err == nil {
			if !response.Accepted {
				return FilePickerResult{}, ErrCancelled
			}
			result.ContainingFolder = response.Parent
			result.Persistent = response.Persistent
			return result, nil
		}
	}
	return confirmFileGrantBuiltin(ctx, result, request)
}

// portalUniqueName resolves who currently owns the portal name, so a reply can
// be held to that peer rather than to a well-known name anybody may answer for.
func portalUniqueName(connection *dbus.Conn) (string, error) {
	var owner string
	call := connection.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, "org.freedesktop.portal.Desktop")
	if call.Err != nil {
		return "", fmt.Errorf("resolve the desktop portal: %w", call.Err)
	}
	if err := call.Store(&owner); err != nil {
		return "", fmt.Errorf("resolve the desktop portal: %w", err)
	}
	if owner == "" {
		return "", errors.New("the desktop portal has no owner")
	}
	return owner, nil
}

// pickerHandleToken answers with a value nobody can predict, which is what the
// token was always supposed to be.
func pickerHandleToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("derive a request token: %w", err)
	}
	return "cpak_" + hex.EncodeToString(raw), nil
}
