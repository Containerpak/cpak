/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type LogsCmd struct {
	Remote   string `arg:"remote" help:"Remote Git repository"`
	Instance string `cli:"instance,i" help:"Application instance"`
	Lines    int    `cli:"lines,n" default:"100" help:"Number of recent lines"`
	Follow   bool   `cli:"follow,f" help:"Follow new output"`

	cli.Base
}

func (c *LogsCmd) Run() error {
	if c.Lines < 1 {
		return fmt.Errorf("lines must be greater than zero")
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	remote, err := resolveApplicationOrigin(cp, c.Remote)
	if err != nil {
		return err
	}
	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		return err
	}
	app, err := store.GetApplicationByOrigin(remote, "", "", "", "")
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("application not found: %w", err)
	}
	if c.Instance != "" {
		app.CpakId = cpak.ApplicationScope(app.CpakId, c.Instance)
	}
	containers, err := store.GetApplicationContainers(app)
	if err != nil {
		_ = store.Close()
		return err
	}
	if err = store.Close(); err != nil {
		return err
	}
	container, running, err := selectContainer(containers, c.Instance)
	if err != nil {
		return err
	}
	if container.LogPath == "" {
		return fmt.Errorf("no log file is available for %s", c.Remote)
	}
	if err = printLogTail(container.LogPath, c.Lines, os.Stdout); err != nil {
		return err
	}
	if c.Follow {
		if !running {
			return fmt.Errorf("application instance is not running")
		}
		return followLog(container.LogPath, os.Stdout)
	}
	return nil
}

func selectContainer(containers []types.Container, instance string) (types.Container, bool, error) {
	filtered := make([]types.Container, 0, len(containers))
	for _, container := range containers {
		if instance == "" || container.Instance == instance {
			filtered = append(filtered, container)
		}
	}
	if len(filtered) == 0 {
		return types.Container{}, false, fmt.Errorf("application instance not found")
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	container := filtered[0]
	running := container.Pid > 0 && syscall.Kill(container.Pid, 0) == nil
	return container, running, nil
}

func printLogTail(path string, lines int, writer io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no output recorded yet")
		}
		return err
	}
	content := string(data)
	all := strings.Split(content, "\n")
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	if len(all) > 0 {
		_, err = io.WriteString(writer, strings.Join(all, "\n")+"\n")
	}
	return err
}

func followLog(path string, writer io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	for {
		data, readErr := io.ReadAll(file)
		if len(data) > 0 {
			if _, err = writer.Write(data); err != nil {
				return err
			}
		}
		if readErr != nil {
			return readErr
		}
		time.Sleep(200 * time.Millisecond)
	}
}
