/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type ProgressRequest struct {
	Title   string
	Heading string
	Detail  string
	IconPNG []byte
}

type ProgressUpdate struct {
	Message string
	Current int64
	Total   int64
}

func Progress(backend Backend, request ProgressRequest, action func(func(ProgressUpdate)) error) error {
	updates := make(chan ProgressUpdate, 32)
	done := make(chan error, 1)
	go func() {
		done <- action(func(update ProgressUpdate) {
			select {
			case updates <- update:
			default:
			}
		})
	}()

	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
	}

	switch backend {
	case BackendGNOME:
		return progressGNOME(request, updates, done)
	case BackendKDE:
		return progressKDE(request, updates, done)
	}
	return progressBuiltin(request, updates, done)
}

func progressGNOME(request ProgressRequest, updates <-chan ProgressUpdate, done <-chan error) error {
	path, err := exec.LookPath("zenity")
	if err != nil {
		return waitProgress(done)
	}
	command := exec.Command(path, "--progress", "--auto-close", "--no-cancel", "--title="+request.Title, "--text="+request.Detail, "--percentage=0", "--width=520")
	input, err := command.StdinPipe()
	if err != nil {
		return waitProgress(done)
	}
	if err = command.Start(); err != nil {
		return waitProgress(done)
	}
	for {
		select {
		case update := <-updates:
			percentage := progressPercentage(update)
			_, _ = fmt.Fprintf(input, "%d\n# %s\n", percentage, strings.ReplaceAll(update.Message, "\n", " "))
		case result := <-done:
			_ = input.Close()
			_ = command.Wait()
			return result
		}
	}
}

func progressKDE(request ProgressRequest, updates <-chan ProgressUpdate, done <-chan error) error {
	path, err := exec.LookPath("kdialog")
	if err != nil {
		return waitProgress(done)
	}
	command := exec.Command(path, "--title", request.Title, "--passivepopup", request.Detail, "300")
	if err = command.Start(); err != nil {
		return waitProgress(done)
	}
	for {
		select {
		case <-updates:
		case result := <-done:
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
			return result
		}
	}
}

func waitProgress(done <-chan error) error {
	return <-done
}

func progressPercentage(update ProgressUpdate) int {
	if update.Total <= 0 || update.Current <= 0 {
		return 0
	}
	percentage := update.Current * 100 / update.Total
	if percentage > 100 {
		return 100
	}
	return int(percentage)
}
