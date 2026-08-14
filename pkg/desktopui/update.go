/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var (
	updateMarkdownLink    = regexp.MustCompile(`\[([^]]+)\]\([^)]+\)`)
	updateGitHubReference = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/(?:pull|issues)/(\d+)`)
)

// UpdateRequest describes one available cpak update.
type UpdateRequest struct {
	CurrentVersion string
	Version        string
	Notes          string
	Managed        bool
	IconPNG        []byte
}

// Update shows the configured desktop interface and applies the update after confirmation.
func Update(backend Backend, request UpdateRequest, action func(func(string)) error) error {
	started := false
	wrapper := func(progress func(string)) error {
		started = true
		return action(progress)
	}
	switch backend {
	case BackendGNOME:
		if err := updateGNOME(request, wrapper); err == nil {
			return nil
		} else if started {
			return err
		}
	case BackendKDE:
		if err := updateKDE(request, wrapper); err == nil {
			return nil
		} else if started {
			return err
		}
	}
	return updateBuiltin(request, wrapper)
}

func updateGNOME(request UpdateRequest, action func(func(string)) error) error {
	path, err := exec.LookPath("zenity")
	if err != nil {
		return err
	}
	message := updateMessage(request)
	if request.Managed {
		return exec.Command(path, "--info", "--title=cpak update", "--text="+message, "--width=520").Run()
	}
	confirm := exec.Command(path, "--question", "--title=cpak update", "--text="+message, "--ok-label=Update", "--cancel-label=Later", "--width=520")
	if err = confirm.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return err
	}
	progress := exec.Command(path, "--progress", "--pulsate", "--auto-close", "--no-cancel", "--title=Updating cpak", "--text=Downloading cpak", "--width=520")
	input, err := progress.StdinPipe()
	if err != nil {
		return err
	}
	if err = progress.Start(); err != nil {
		return err
	}
	actionErr := action(func(message string) {
		_, _ = fmt.Fprintf(input, "# %s\n", strings.ReplaceAll(message, "\n", " "))
	})
	_ = input.Close()
	_ = progress.Wait()
	if actionErr != nil {
		showGNOMEError(path, "cpak update", actionErr)
		return actionErr
	}
	return exec.Command(path, "--info", "--title=cpak update", "--text=cpak "+request.Version+" is installed", "--width=520").Run()
}

func updateKDE(request UpdateRequest, action func(func(string)) error) error {
	path, err := exec.LookPath("kdialog")
	if err != nil {
		return err
	}
	message := stripMarkup(updateMessage(request))
	if request.Managed {
		return exec.Command(path, "--title", "cpak update", "--msgbox", message).Run()
	}
	confirm := exec.Command(path, "--title", "cpak update", "--yesno", message, "--yes-label", "Update", "--no-label", "Later")
	if err = confirm.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return err
	}
	progress := exec.Command(path, "--title", "Updating cpak", "--passivepopup", "Downloading cpak", "300")
	if err = progress.Start(); err != nil {
		return err
	}
	actionErr := action(func(string) {})
	_ = progress.Process.Kill()
	_, _ = progress.Process.Wait()
	if actionErr != nil {
		_ = exec.Command(path, "--title", "cpak update", "--error", actionErr.Error()).Run()
		return actionErr
	}
	return exec.Command(path, "--title", "cpak update", "--msgbox", "cpak "+request.Version+" is installed").Run()
}

func updateMessage(request UpdateRequest) string {
	title := "<b>cpak " + escapeMarkup(request.Version) + " is available</b>"
	if request.Managed {
		return title + "\n\nYour package manager owns this installation. Ask its maintainer to publish the update."
	}
	notes := summarizeNotes(request.Notes, 700)
	if notes == "" {
		return title + "\n\nInstalled version: " + escapeMarkup(request.CurrentVersion)
	}
	return title + "\n\n" + escapeMarkup(notes)
}

func summarizeNotes(notes string, limit int) string {
	lines := strings.Split(strings.ReplaceAll(notes, "\r", ""), "\n")
	result := make([]string, 0, len(lines))
	length := 0
	for _, line := range lines {
		raw := strings.TrimSpace(line)
		if strings.HasPrefix(raw, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#*- "))
		plain := strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "").Replace(line))
		if strings.HasPrefix(strings.ToLower(plain), "full changelog:") {
			continue
		}
		line = updateMarkdownLink.ReplaceAllString(line, "$1")
		line = updateGitHubReference.ReplaceAllString(line, "#$1")
		line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
		if line == "" {
			continue
		}
		if length+len(line) > limit {
			break
		}
		result = append(result, line)
		length += len(line)
	}
	return strings.Join(result, "\n")
}

func stripMarkup(value string) string {
	return strings.NewReplacer("<b>", "", "</b>", "", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'").Replace(value)
}
