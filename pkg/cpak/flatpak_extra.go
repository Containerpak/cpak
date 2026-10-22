/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// Some applications, Chrome and Spotify among them, are published as a stub:
// Flatpak downloads the real program from its publisher at install time and
// the application's apply_extra script unpacks it into /app/extra. cpak does
// the same. The files are checked against the sizes and checksums the signed
// commit names, apply_extra runs inside the application's own sandbox with no
// network and nothing writable but /app/extra, and what it leaves becomes one
// more layer of the application, so a launch never runs publisher code again.

const extraDataInstance = "apply-extra"

var extraDataClient = &http.Client{Timeout: 30 * time.Minute}

func flatpakExtraData(files []flatpak.ExtraData) []types.FlatpakExtraData {
	records := make([]types.FlatpakExtraData, 0, len(files))
	for _, file := range files {
		records = append(records, types.FlatpakExtraData{Name: file.Name, URI: file.URI, Checksum: file.Checksum, Size: file.Size})
	}
	return records
}

// flatpakExtraLayerDigest is derived from the application commit and the
// checksums the commit promised, never from what apply_extra produced.
func flatpakExtraLayerDigest(source *types.FlatpakSource) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "cpak.flatpak.extra.v%d\n%s\n%s\n", integrity.ABIVersion, source.Ref, source.Commit)
	for _, file := range source.ExtraData {
		fmt.Fprintf(hash, "%s\n%s\n%d\n", file.Name, file.Checksum, file.Size)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (c *Cpak) downloadExtraData(ref string, file types.FlatpakExtraData, directory string, done, total int) error {
	request, err := http.NewRequestWithContext(c.Ctx, http.MethodGet, file.URI, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", flatpakUserAgent)
	response, err := extraDataClient.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", file.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", file.Name, response.Status)
	}
	target, err := os.OpenFile(filepath.Join(directory, file.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	digest := sha256.New()
	reporter := c.newDownloadReporter(ref, done, total)
	limited := io.LimitReader(response.Body, int64(file.Size)+1)
	written, copyErr := io.Copy(io.MultiWriter(target, digest, writerFunc(func(p []byte) (int, error) {
		reporter.add(int64(len(p)))
		return len(p), nil
	})), limited)
	closeErr := target.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("download %s: %w", file.Name, err)
	}
	if uint64(written) != file.Size {
		return fmt.Errorf("%s is %d bytes, the application promised %d", file.Name, written, file.Size)
	}
	if hex.EncodeToString(digest.Sum(nil)) != file.Checksum {
		return fmt.Errorf("%s does not match the checksum the application promised", file.Name)
	}
	return nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) {
	return f(p)
}

// applyFlatpakExtraData downloads an application's extra data, runs its
// apply_extra and records the result as a layer of the application.
func (c *Cpak) applyFlatpakExtraData(app *types.Application) error {
	source := app.Flatpak
	if source == nil || len(source.ExtraData) == 0 {
		return nil
	}
	digest := flatpakExtraLayerDigest(source)
	available, err := c.fvsLayerAvailable(digest)
	if err != nil {
		return err
	}
	if !available {
		if err := c.buildFlatpakExtraLayer(*app, digest); err != nil {
			return err
		}
	}
	if source.ExtraLayer != digest {
		app.ParsedLayers = append(app.ParsedLayers, digest)
		source.ExtraLayer = digest
	}
	return nil
}

func (c *Cpak) buildFlatpakExtraLayer(app types.Application, digest string) error {
	staging, err := os.MkdirTemp(c.GetInStoreDir("tmp"), "extra-data-")
	if err != nil {
		if mkErr := os.MkdirAll(c.GetInStoreDir("tmp"), 0o755); mkErr != nil {
			return mkErr
		}
		if staging, err = os.MkdirTemp(c.GetInStoreDir("tmp"), "extra-data-"); err != nil {
			return err
		}
	}
	defer os.RemoveAll(staging)
	extra := filepath.Join(staging, "app", "extra")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		return err
	}
	for index, file := range app.Flatpak.ExtraData {
		if err := c.downloadExtraData(app.Flatpak.Ref, file, extra, index, len(app.Flatpak.ExtraData)); err != nil {
			return err
		}
	}
	c.reportFlatpak(FlatpakProgress{Phase: FlatpakPhaseInstall, Ref: app.Flatpak.Ref})
	if err := c.runApplyExtra(app, extra); err != nil {
		return err
	}
	temporary, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{
		Message:       "extra data " + app.Flatpak.Ref,
		ComputeSHA256: true,
	})
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := writer.AddTreeProgress(staging, nil); err != nil {
		_ = writer.Abort()
		return fmt.Errorf("record extra data of %s: %w", app.Name, err)
	}
	if _, err := writer.Commit(); err != nil {
		return fmt.Errorf("commit extra data of %s: %w", app.Name, err)
	}
	if err := publishFVSLayer(temporary, c.fvsLayerPath(digest)); err != nil {
		return err
	}
	if err := c.recordLayerBinding(digest); err != nil {
		logger.Printf("Warning: the extra data layer of %s was not bound: %v", app.Name, err)
	}
	return nil
}

// runApplyExtra starts apply_extra the way Flatpak does: in the application's
// sandbox, from /app/extra, without network and with only that directory
// writable.
func (c *Cpak) runApplyExtra(app types.Application, extra string) error {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	policy, err := standaloneLaunchPolicy(store, app)
	if err != nil {
		_ = store.Close()
		return err
	}
	policy.effective.Network = false
	policy.effective.HostNetwork = false
	previous := c.writableLinks
	c.writableLinks = append(append([]string{}, previous...), extra+":/app/extra")
	defer func() { c.writableLinks = previous }()
	defer func() { _ = c.StopContainerInstance(app, extraDataInstance) }()
	err = c.runApplicationInstanceWithStore(app, policy, extraDataInstance, "@/bin/sh", false, false, store,
		"-c", "cd /app/extra && exec /app/bin/apply_extra")
	if err != nil {
		return fmt.Errorf("apply_extra of %s failed: %w", app.Name, err)
	}
	return nil
}
