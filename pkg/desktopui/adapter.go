/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const adapterProtocolVersion = "1"

var errorsNoEmbeddedAdapter = errors.New("desktop adapter is not embedded")

type adapterPrompt struct {
	Title            string
	Heading          string
	Body             string
	Application      string
	Resource         string
	AcceptLabel      string
	CancelLabel      string
	OfferParent      bool
	OfferPersistent  bool
	ParentSelected   bool
	PersistentChosen bool
	Recommended      bool
}

type adapterPromptResult struct {
	Accepted   bool
	Parent     bool
	Persistent bool
}

func runAdapterPrompt(ctx context.Context, backend Backend, prompt adapterPrompt) (adapterPromptResult, error) {
	path, err := adapterExecutable(backend)
	if err != nil {
		return adapterPromptResult{}, err
	}
	arguments := []string{
		"prompt",
		"--protocol", adapterProtocolVersion,
		"--title", prompt.Title,
		"--heading", prompt.Heading,
		"--body", prompt.Body,
		"--application", prompt.Application,
		"--resource", prompt.Resource,
		"--accept-label", prompt.AcceptLabel,
		"--cancel-label", prompt.CancelLabel,
		"--offer-parent", strconv.FormatBool(prompt.OfferParent),
		"--offer-persistent", strconv.FormatBool(prompt.OfferPersistent),
		"--parent-selected", strconv.FormatBool(prompt.ParentSelected),
		"--persistent-selected", strconv.FormatBool(prompt.PersistentChosen),
		"--recommended", strconv.FormatBool(prompt.Recommended),
	}
	command := exec.CommandContext(ctx, path, arguments...)
	output, err := command.Output()
	if err != nil {
		return adapterPromptResult{}, err
	}
	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) != 3 || fields[0] != "allow" && fields[0] != "deny" {
		return adapterPromptResult{}, errors.New("desktop adapter returned an invalid response")
	}
	parent, parentErr := strconv.ParseBool(fields[1])
	persistent, persistentErr := strconv.ParseBool(fields[2])
	if parentErr != nil || persistentErr != nil {
		return adapterPromptResult{}, errors.New("desktop adapter returned invalid choices")
	}
	return adapterPromptResult{Accepted: fields[0] == "allow", Parent: parent, Persistent: persistent}, nil
}

func runAdapterProgress(backend Backend, request ProgressRequest, updates <-chan ProgressUpdate, done <-chan error) (bool, error) {
	path, err := adapterExecutable(backend)
	if err != nil {
		return false, nil
	}
	command := exec.Command(path,
		"progress",
		"--protocol", adapterProtocolVersion,
		"--title", request.Title,
		"--heading", request.Heading,
		"--body", request.Detail,
	)
	input, err := command.StdinPipe()
	if err != nil {
		return false, nil
	}
	if err = command.Start(); err != nil {
		return false, nil
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	writer := bufio.NewWriter(input)
	for {
		select {
		case update := <-updates:
			message := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(update.Message)
			if _, err = fmt.Fprintf(writer, "%d\t%d\t%s\n", update.Current, update.Total, message); err != nil {
				_ = input.Close()
				return false, nil
			}
			if err = writer.Flush(); err != nil {
				_ = input.Close()
				return false, nil
			}
		case result := <-done:
			_ = input.Close()
			waitErr := <-wait
			if result != nil {
				return true, result
			}
			return true, waitErr
		case <-wait:
			_ = input.Close()
			return false, nil
		}
	}
}

func adapterExecutable(backend Backend) (string, error) {
	if !adapterBuilt(backend) {
		return "", fmt.Errorf("desktop adapter %s is not part of this build", backend)
	}
	if path, err := materializeEmbeddedAdapter(backend); err == nil {
		return usableAdapter(path, backend)
	}
	name := "cpak-ui-" + string(backend)
	if directory := os.Getenv("CPAK_UI_ADAPTER_DIR"); directory != "" {
		return usableAdapter(filepath.Join(directory, name), backend)
	}
	paths := []string{"/usr/libexec/cpak/ui", "/usr/local/libexec/cpak/ui"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append([]string{filepath.Join(home, ".local", "libexec", "cpak", "ui")}, paths...)
	}
	for _, directory := range paths {
		path, err := usableAdapter(filepath.Join(directory, name), backend)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("desktop adapter %s is unavailable", backend)
}

func usableAdapter(path string, backend Backend) (string, error) {
	stat, err := os.Stat(path)
	if err != nil || stat.IsDir() || stat.Mode().Perm()&0111 == 0 {
		return "", errors.New("desktop adapter is not executable")
	}
	command := exec.Command(path, "probe")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	expected := "cpak-ui " + adapterProtocolVersion + " " + string(backend)
	if strings.TrimSpace(string(output)) != expected {
		return "", errors.New("desktop adapter protocol mismatch")
	}
	return path, nil
}
