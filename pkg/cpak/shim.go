/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed system-broker-shim.tmpl
var systemBrokerShimTemplate string

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
