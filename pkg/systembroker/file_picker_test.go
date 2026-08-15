/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
)

func TestExecuteFilePickerMountsAndPersistsContainingFolder(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "game.exe")
	if err := os.WriteFile(selected, []byte("MZ"), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "grant.sock")
	listener, err := net.Listen("unixpacket", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mounted := make(chan filegrant.Grant, 1)
	go func() {
		accepted, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		connection := accepted.(*net.UnixConn)
		defer connection.Close()
		request, sources, receiveErr := grantproto.Receive(connection)
		if receiveErr != nil {
			return
		}
		defer sources.Close()
		mounted <- request.Grant
		_ = grantproto.Reply(connection, grantproto.Response{Target: request.Grant.Target})
	}()
	storePath := filepath.Join(t.TempDir(), "grants")
	const application = "Example Game"
	options := Options{
		FilePicker:            FilePickerPolicy{OpenFile: true, Persistent: true, ContainingFolder: true},
		FilePickerApplication: application,
		FilePickerOrigin:      "github.com/example/game",
		FileGrantSocketPath:   socket,
		FileGrantStorePath:    storePath,
		PickFile: func(context.Context, desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			return desktopui.FilePickerResult{Path: selected}, nil
		},
		ConfirmFileGrant: func(_ context.Context, result desktopui.FilePickerResult, request desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			if request.Application != application || !request.OfferPersistent || !request.OfferContainingFolder {
				t.Fatal("grant choices were not offered after selection")
			}
			result.Persistent = true
			result.ContainingFolder = true
			return result, nil
		},
	}
	payload, err := json.Marshal(FilePickerRequest{Mode: desktopui.PickerOpenFile, Title: "Select executable"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer := newFrameWriter(&output)
	if _, err = executeFilePicker(context.Background(), payload, options, writer); err != nil {
		t.Fatal(err)
	}
	grant := <-mounted
	if grant.Source != directory || filepath.Base(grant.Target) != "game.exe" || grant.Lifetime != filegrant.LifetimePersistent {
		t.Fatalf("mounted grant: %+v", grant)
	}
	stored, err := (filegrant.Store{Directory: storePath}).Load(options.FilePickerOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0] != grant {
		t.Fatalf("stored grants: %+v", stored)
	}
}

func TestExecuteFilePickerMountsMultipleFilesIndividually(t *testing.T) {
	directory := t.TempDir()
	selected := []string{filepath.Join(directory, "first.txt"), filepath.Join(directory, "second.txt")}
	for index, path := range selected {
		if err := os.WriteFile(path, []byte{byte(index)}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(t.TempDir(), "grant.sock")
	listener, err := net.Listen("unixpacket", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mounted := make(chan filegrant.Grant, len(selected))
	go func() {
		for range selected {
			accepted, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			connection := accepted.(*net.UnixConn)
			request, sources, receiveErr := grantproto.Receive(connection)
			if receiveErr == nil {
				mounted <- request.Grant
				sources.Close()
				_ = grantproto.Reply(connection, grantproto.Response{Target: request.Grant.Target})
			}
			_ = connection.Close()
		}
	}()
	options := Options{
		FilePicker:          FilePickerPolicy{OpenFile: true},
		FilePickerOrigin:    "github.com/example/editor",
		FileGrantSocketPath: socket,
		FileGrantStorePath:  filepath.Join(t.TempDir(), "grants"),
		PickFile: func(context.Context, desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			return desktopui.FilePickerResult{Path: selected[0], Paths: selected}, nil
		},
		ConfirmFileGrant: func(_ context.Context, result desktopui.FilePickerResult, _ desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			return result, nil
		},
	}
	payload, err := json.Marshal(FilePickerRequest{Mode: desktopui.PickerOpenFile, Title: "Select documents", Multiple: true})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err = executeFilePicker(context.Background(), payload, options, newFrameWriter(&output)); err != nil {
		t.Fatal(err)
	}
	grants := []filegrant.Grant{<-mounted, <-mounted}
	if grants[0].Source != selected[0] || grants[1].Source != selected[1] || grants[0].Kind != filegrant.KindFile || grants[1].Kind != filegrant.KindFile || grants[0].ID == grants[1].ID {
		t.Fatalf("mounted grants: %+v", grants)
	}
	var frame Frame
	if err = json.Unmarshal(output.Bytes(), &frame); err != nil {
		t.Fatal(err)
	}
	var result FilePickerResult
	if err = json.Unmarshal(frame.Data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 2 || result.Paths[0] != grants[0].Target || result.Paths[1] != grants[1].Target || result.ContainingFolder {
		t.Fatalf("picker result: %+v", result)
	}
}

func TestExecuteFilePickerUsesManifestPathWithoutAnotherGrant(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "document.pdf")
	if err := os.WriteFile(selected, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		FilePicker:       FilePickerPolicy{OpenFile: true, Persistent: true, ContainingFolder: true},
		FilePickerOrigin: "github.com/example/viewer",
		FilePickerPaths: []FilePickerPathGrant{{
			Source: directory,
			Target: "/home/user/Documents",
		}},
		PickFile: func(_ context.Context, request desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			if request.OfferPersistent || request.OfferContainingFolder {
				t.Fatal("grant choices were offered before the selected path was checked")
			}
			return desktopui.FilePickerResult{Path: selected}, nil
		},
		ConfirmFileGrant: func(context.Context, desktopui.FilePickerResult, desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			t.Fatal("manifest path requested another grant")
			return desktopui.FilePickerResult{}, nil
		},
	}
	payload, err := json.Marshal(FilePickerRequest{Mode: desktopui.PickerOpenFile, Title: "Select document"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err = executeFilePicker(context.Background(), payload, options, newFrameWriter(&output)); err != nil {
		t.Fatal(err)
	}
	var frame Frame
	if err = json.Unmarshal(output.Bytes(), &frame); err != nil {
		t.Fatal(err)
	}
	var result FilePickerResult
	if err = json.Unmarshal(frame.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != "/home/user/Documents/document.pdf" || result.Lifetime != filegrant.LifetimePersistent {
		t.Fatalf("manifest result: %+v", result)
	}
}

func TestExecuteFilePickerCancellationDeniesAccess(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "document.pdf")
	if err := os.WriteFile(selected, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		FilePicker:          FilePickerPolicy{OpenFile: true, Persistent: true, ContainingFolder: true},
		FilePickerOrigin:    "github.com/example/viewer",
		FileGrantSocketPath: filepath.Join(t.TempDir(), "grant.sock"),
		FileGrantStorePath:  t.TempDir(),
		PickFile: func(context.Context, desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			return desktopui.FilePickerResult{Path: selected}, nil
		},
		ConfirmFileGrant: func(context.Context, desktopui.FilePickerResult, desktopui.FilePickerRequest) (desktopui.FilePickerResult, error) {
			return desktopui.FilePickerResult{}, desktopui.ErrCancelled
		},
	}
	payload, err := json.Marshal(FilePickerRequest{Mode: desktopui.PickerOpenFile, Title: "Select document"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	_, err = executeFilePicker(context.Background(), payload, options, newFrameWriter(&output))
	if !errors.Is(err, desktopui.ErrCancelled) {
		t.Fatalf("error: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cancelled picker returned data: %q", output.String())
	}
}

func TestDirectFilePickerTargetHandlesPathAliases(t *testing.T) {
	directory := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(alias, "setup.exe")
	if err := os.WriteFile(filepath.Join(directory, "setup.exe"), []byte("MZ"), 0600); err != nil {
		t.Fatal(err)
	}
	target, ok := directFilePickerTarget(selected, desktopui.PickerOpenFile, false, []FilePickerPathGrant{{Source: directory, Target: "/home/user"}})
	if !ok || target != "/home/user/setup.exe" {
		t.Fatalf("aliased target: %q, %t", target, ok)
	}
}

func TestReadOnlyManifestPathDoesNotCoverSave(t *testing.T) {
	directory := t.TempDir()
	_, ok := directFilePickerTarget(filepath.Join(directory, "report.pdf"), desktopui.PickerSaveFile, false, []FilePickerPathGrant{{
		Source:   directory,
		Target:   "/home/user/Documents",
		ReadOnly: true,
	}})
	if ok {
		t.Fatal("read-only manifest path covered a save destination")
	}
}

func TestParseFilePickerRejectsUnknownOptions(t *testing.T) {
	if _, err := parseFilePicker([]string{"open-file", "--multiple", "true"}); err == nil {
		t.Fatal("unsupported file picker option was accepted")
	}
}

func TestParseFilePickerAcceptsCurrentFolder(t *testing.T) {
	request, err := parseFilePicker([]string{"open-file", "--current-folder", "/home/test/Downloads"})
	if err != nil {
		t.Fatal(err)
	}
	if request.CurrentFolder != "/home/test/Downloads" {
		t.Fatalf("current folder: %q", request.CurrentFolder)
	}
}
