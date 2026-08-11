/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed shim.tmpl
var shimTemplate string

//go:embed system-broker-shim.tmpl
var systemBrokerShimTemplate string

// RenderShim rende il contenuto dello shim script, sostituendo
// {{.CpakBinaryPath}} con il path corretto del binario cpak.
func RenderShim(cpakBinaryPath string) ([]byte, error) {
	tmpl, err := template.New("shim").Parse(shimTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ CpakBinaryPath string }{cpakBinaryPath}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func RenderSystemBrokerShim() ([]byte, error) {
	tmpl, err := template.New("system-broker-shim").Parse(systemBrokerShimTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
