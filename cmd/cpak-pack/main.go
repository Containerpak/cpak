/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
	if err = os.WriteFile(*outputPath, bootstrap.PackInstaller(installer, payload), 0755); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
