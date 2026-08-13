/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const rollbackHistoryLimit = 3

type RollbackResult struct {
	Origin      string
	Name        string
	FromVersion string
	ToVersion   string
}

func (c *Cpak) rollbackHistoryPath(origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return filepath.Join(c.Options.StorePath, "history", hex.EncodeToString(digest[:])+".json")
}

func (c *Cpak) readRollbackHistory(origin string) ([]types.Application, error) {
	data, err := os.ReadFile(c.rollbackHistoryPath(origin))
	if os.IsNotExist(err) {
		return []types.Application{}, nil
	}
	if err != nil {
		return nil, err
	}
	apps := []types.Application{}
	if err = json.Unmarshal(data, &apps); err != nil {
		return nil, fmt.Errorf("decode rollback history for %s: %w", origin, err)
	}
	return apps, nil
}

func (c *Cpak) writeRollbackHistory(origin string, apps []types.Application) error {
	path := c.rollbackHistoryPath(origin)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".history.partial-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (c *Cpak) recordRollbackHistory(app types.Application) error {
	apps, err := c.readRollbackHistory(app.Origin)
	if err != nil {
		return err
	}
	apps = append(apps, app)
	if len(apps) > rollbackHistoryLimit {
		apps = apps[len(apps)-rollbackHistoryLimit:]
	}
	return c.writeRollbackHistory(app.Origin, apps)
}

func (c *Cpak) clearRollbackHistory(origin string) error {
	err := os.Remove(c.rollbackHistoryPath(origin))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c *Cpak) rollbackHistoryApplications() ([]types.Application, error) {
	directory := filepath.Join(c.Options.StorePath, "history")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	apps := []types.Application{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		var history []types.Application
		if err = json.Unmarshal(data, &history); err != nil {
			return nil, err
		}
		apps = append(apps, history...)
	}
	return apps, nil
}

// Rollback restores the most recent saved installation for an application.
func (c *Cpak) Rollback(origin string) (result RollbackResult, err error) {
	app, err := c.getStoredApplication(origin, "", "", "", "")
	if err != nil {
		return result, err
	}
	history, err := c.readRollbackHistory(app.Origin)
	if err != nil {
		return result, err
	}
	if len(history) == 0 {
		return result, errors.New("no rollback history available")
	}
	previous := history[len(history)-1]
	transaction, err := c.beginUpdateTransaction(app, previous)
	if err != nil {
		return result, err
	}
	if err = c.createExports(previous); err != nil {
		return result, err
	}
	if err = c.stopApplicationContainers(app); err != nil {
		return result, err
	}
	if err = c.replaceApplication(app, previous); err != nil {
		return result, err
	}
	if err = c.commitUpdateTransaction(transaction); err != nil {
		return result, err
	}
	if err = c.removeStaleExports(app, previous); err != nil {
		return result, err
	}
	if err = c.writeRollbackHistory(app.Origin, history[:len(history)-1]); err != nil {
		return result, err
	}
	if err = c.finishUpdateTransaction(transaction); err != nil {
		return result, err
	}
	return RollbackResult{
		Origin:      app.Origin,
		Name:        previous.Name,
		FromVersion: app.Version,
		ToVersion:   previous.Version,
	}, nil
}
