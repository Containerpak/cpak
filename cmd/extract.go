/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
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
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

func NewExtractCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extract <remote>",
		Short: "Extract a cpak rootfs into a tarball",
		Long: `Extract a cpak rootfs into a tarball.
Questo comando crea un archivio .tar.gz unendo i layer dell'applicazione nell'ordine definito,
skippando le directory di sistema e ignorando eventuali permission errors.`,
		Args: cobra.ExactArgs(1),
		RunE: ExtractPackage,
	}

	cmd.Flags().StringP("branch", "b", "", "Specify a branch")
	cmd.Flags().StringP("commit", "c", "", "Specify a commit")
	cmd.Flags().StringP("release", "r", "", "Specify a release")
	cmd.Flags().StringP("output", "o", "", "Output tar.gz path (default: cpak-<remote>.tar.gz)")

	return cmd
}

func ExtractPackage(cmd *cobra.Command, args []string) error {
	origin := strings.ToLower(args[0])
	branch, _ := cmd.Flags().GetString("branch")
	commit, _ := cmd.Flags().GetString("commit")
	release, _ := cmd.Flags().GetString("release")
	output, _ := cmd.Flags().GetString("output")

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

	app, err := store.GetApplicationByOrigin(origin, "", branch, commit, release)
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

	for _, layer := range app.ParsedLayers {
		layerDir := cp.GetInStoreDir("layers", layer)

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
			progressbar.OptionSetDescription(fmt.Sprintf("Layer %s", layer[:12])),
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
			return fmt.Errorf("error archiving layer %s: %w", layer, err)
		}
	}

	logger.Printf("\nExtracted %s to %s", origin, output)
	return nil
}
