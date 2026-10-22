/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
	"github.com/mirkobrombin/cpak/pkg/ostree"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

func quietMigration() *MigrateFlatpakCmd {
	return &MigrateFlatpakCmd{Yes: true, Base: cli.Base{Logger: clilog.NewWriter(io.Discard, io.Discard)}}
}

// unreachableInstallation lays out a Flatpak installation holding one
// application from a remote nothing listens on.
func unreachableInstallation(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	commit := strings.Repeat("ab", 32)
	directory := filepath.Join(root, "app", "org.example.App", "x86_64", "stable", commit)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := gvariant.Encode("(ssasta{sv})", []any{"local", commit, []any{}, uint64(0), []any{}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join("..", "pkg", "ostree", "testdata", "flathub", "flathub.gpg"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(directory, "deploy"):                   data,
		filepath.Join(root, "repo", "config"):                []byte("[remote \"local\"]\nurl=http://127.0.0.1:1/repo/\ngpg-verify=true\n"),
		filepath.Join(root, "repo", "local.trustedkeys.gpg"): key,
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(commit, filepath.Join(filepath.Dir(directory), "active")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("x86_64/stable", filepath.Join(root, "app", "org.example.App", "current")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMigrationLeftBehindExitsWithTheReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CPAK_INSTALLATION_PATH", t.TempDir())
	t.Setenv("FLATPAK_USER_DIR", unreachableInstallation(t))
	t.Setenv("FLATPAK_SYSTEM_DIR", t.TempDir())
	err := quietMigration().Run()
	var exit *types.ExitError
	if !errors.As(err, &exit) || exit.Code != exitUnavailable {
		t.Fatalf("a migration that could not reach the remote ended with %v", err)
	}
}

func TestMigrationWithNothingInstalledSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CPAK_INSTALLATION_PATH", t.TempDir())
	t.Setenv("FLATPAK_USER_DIR", t.TempDir())
	t.Setenv("FLATPAK_SYSTEM_DIR", t.TempDir())
	if err := quietMigration().Run(); err != nil {
		t.Fatalf("a system without Flatpak applications failed to migrate: %v", err)
	}
}

func TestWorstFailureTellsVerificationFirst(t *testing.T) {
	unreachable := errors.Join(errors.New("dial"), ostree.ErrUnavailable)
	forged := errors.Join(errors.New("checksum"), ostree.ErrVerification)
	if worst := worstFailure([]error{errors.New("disk full"), unreachable, forged}); worst != forged {
		t.Fatalf("the worst failure is %v", worst)
	}
	if worst := worstFailure([]error{errors.New("disk full"), unreachable}); worst != unreachable {
		t.Fatalf("the worst failure is %v", worst)
	}
}
