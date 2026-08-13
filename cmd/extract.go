/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"github.com/schollz/progressbar/v3"
)

type ExtractCmd struct {
	Remote  string `arg:"remote" help:"Remote Git repository"`
	Branch  string `cli:"branch,b" help:"Specify a branch"`
	Commit  string `cli:"commit,c" help:"Specify a commit"`
	Release string `cli:"release,r" help:"Specify a release"`
	Output  string `cli:"output,o" help:"Output tar.gz path (default: cpak-<remote>.tar.gz)"`

	cli.Base
}

func (c *ExtractCmd) Run() error {
	origin := strings.ToLower(c.Remote)
	output := c.Output

	if output == "" {
		base := strings.ReplaceAll(origin, "/", "-")
		output = fmt.Sprintf("cpak-%s.tar.gz", base)
	}

	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("failed to initialize cpak: %w", err)
	}

	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	app, err := store.GetApplicationByOrigin(origin, "", c.Branch, c.Commit, c.Release)
	if err != nil {
		return fmt.Errorf("application not found for origin %q: %w", origin, err)
	}

	outFile, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("failed to create %q: %w", output, err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	excluded := []string{"dev", "home", "proc", "sys", "tmp", "run"}
	err = cp.WithApplicationFilesystem(app, func(layerDir string) error {
		var total int
		_ = filepath.Walk(layerDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				if os.IsPermission(walkErr) {
					return nil
				}
				return walkErr
			}
			rel, err := filepath.Rel(layerDir, path)
			if err != nil || rel == "" {
				return nil
			}
			for _, ex := range excluded {
				if rel == ex || strings.HasPrefix(rel, ex+string(os.PathSeparator)) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			total++
			return nil
		})

		bar := progressbar.NewOptions(total,
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "━",
				SaucerHead:    "╸",
				SaucerPadding: " ",
				BarStart:      "",
				BarEnd:        "",
			}),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionFullWidth(),
			progressbar.OptionSetDescription(app.Name),
			progressbar.OptionOnCompletion(func() { fmt.Fprint(os.Stderr, "\n") }),
		)

		err := filepath.Walk(layerDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				if os.IsPermission(walkErr) {
					return nil
				}
				return walkErr
			}
			rel, err := filepath.Rel(layerDir, path)
			if err != nil || rel == "" {
				return nil
			}
			for _, ex := range excluded {
				if rel == ex || strings.HasPrefix(rel, ex+string(os.PathSeparator)) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}

			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = rel
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					if os.IsPermission(err) {
						return nil
					}
					return err
				}
				hdr.Linkname = target
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				f, err := os.Open(path)
				if err != nil {
					if os.IsPermission(err) {
						return nil
					}
					return err
				}
				defer f.Close()
				if _, err := io.Copy(tw, f); err != nil {
					if os.IsPermission(err) {
						return nil
					}
					return err
				}
			}
			_ = bar.Add(1)
			return nil
		})
		if err != nil {
			return fmt.Errorf("error archiving application: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	c.Logger.Success("\nExtracted %s to %s", origin, output)
	return nil
}
