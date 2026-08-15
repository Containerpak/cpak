/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"context"
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
	wrapper := func(progress func(string)) error {
		return action(progress)
	}
	if backend == BackendAdwaita || backend == BackendGTK || backend == BackendKDE || backend == BackendQt {
		acceptLabel := "Update"
		cancelLabel := "Later"
		recommended := true
		if request.Managed {
			acceptLabel = "Close"
			cancelLabel = ""
			recommended = false
		}
		result, err := runAdapterPrompt(context.Background(), backend, adapterPrompt{
			Title: "cpak update", Heading: "cpak " + request.Version + " is available",
			Body: stripMarkup(updateMessage(request)), AcceptLabel: acceptLabel, CancelLabel: cancelLabel, Recommended: recommended,
		})
		if err == nil {
			if !result.Accepted || request.Managed {
				return nil
			}
			return Progress(backend, ProgressRequest{Title: "Updating cpak", Heading: "Updating cpak", Detail: "Downloading cpak", IconPNG: request.IconPNG}, func(progress func(ProgressUpdate)) error {
				return wrapper(func(message string) {
					progress(ProgressUpdate{Message: message})
				})
			})
		}
	}
	return updateBuiltin(request, wrapper)
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
