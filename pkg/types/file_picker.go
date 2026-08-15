/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func DecodeFilePickerGrantJSON(data []byte) (FilePickerGrant, error) {
	grant := FilePickerGrant{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grant); err != nil {
		return FilePickerGrant{}, fmt.Errorf("decode file picker permission: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return FilePickerGrant{}, errors.New("file picker permission contains multiple JSON values")
	}
	if err := ValidateFilePickerGrant(grant); err != nil {
		return FilePickerGrant{}, err
	}
	return grant, nil
}
