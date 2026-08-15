/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type adapter struct {
	name string
	path string
}

func main() {
	output := flag.String("output", "pkg/desktopui", "directory for generated Go sources")
	adwaita := flag.String("adwaita", "", "Adwaita adapter binary")
	gtk := flag.String("gtk", "", "GTK adapter binary")
	kde := flag.String("kde", "", "KDE adapter binary")
	qt := flag.String("qt", "", "Qt adapter binary")
	flag.Parse()
	adapters := []adapter{{"adwaita", *adwaita}, {"gtk", *gtk}, {"kde", *kde}, {"qt", *qt}}
	for _, item := range adapters {
		if item.path == "" {
			continue
		}
		payload, err := os.ReadFile(item.path)
		if err != nil {
			fatal(err)
		}
		constraint := "cpak_ui_" + item.name
		if err = writeSource(*output, "adapter_embedded_"+item.name+"_generated.go", constraint, []adapterPayload{{item.name, payload}}); err != nil {
			fatal(err)
		}
	}
	available := make([]adapterPayload, 0, len(adapters))
	for _, item := range adapters {
		if item.path == "" {
			continue
		}
		payload, err := os.ReadFile(item.path)
		if err != nil {
			fatal(err)
		}
		available = append(available, adapterPayload{item.name, payload})
	}
	constraint := "!cpak_ui_builtin && !cpak_ui_adwaita && !cpak_ui_gtk && !cpak_ui_kde && !cpak_ui_qt"
	if err := writeSource(*output, "adapter_embedded_default_generated.go", constraint, available); err != nil {
		fatal(err)
	}
}

type adapterPayload struct {
	name    string
	payload []byte
}

func writeSource(directory, name, constraint string, payloads []adapterPayload) error {
	var source strings.Builder
	fmt.Fprintf(&source, "//go:build %s\n\n", constraint)
	source.WriteString("package desktopui\n\nfunc init() {\n")
	for _, payload := range payloads {
		fmt.Fprintf(&source, "registerEmbeddedAdapter(Backend%s, []byte(%s))\n", backendName(payload.name), strconv.Quote(string(payload.payload)))
	}
	source.WriteString("}\n")
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, name), formatted, 0600)
}

func backendName(name string) string {
	switch name {
	case "gtk":
		return "GTK"
	case "kde":
		return "KDE"
	case "qt":
		return "Qt"
	}
	return "Adwaita"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
