/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
)

func main() {
	installerPath := flag.String("installer", "", "path to the installer binary")
	cpakPath := flag.String("cpak", "", "path to the cpak binary")
	companionPath := flag.String("storaged", "", "path to the cpak storage service binary")
	outputPath := flag.String("output", "", "output path")
	flag.Parse()
	if *installerPath == "" || *cpakPath == "" || *outputPath == "" {
		fmt.Fprintln(os.Stderr, "installer, cpak and output are required")
		os.Exit(2)
	}

	installer, err := os.ReadFile(*installerPath)
	if err != nil {
		fail(err)
	}
	payload, err := os.ReadFile(*cpakPath)
	if err != nil {
		fail(err)
	}
	var companion []byte
	if *companionPath != "" {
		companion, err = os.ReadFile(*companionPath)
		if err != nil {
			fail(err)
		}
	}
	packed, err := bootstrap.PackInstallerWithCompanion(installer, payload, companion)
	if err != nil {
		fail(err)
	}
	if err = os.WriteFile(*outputPath, packed, 0755); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
