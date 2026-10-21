/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package gvariant reads and writes the GVariant serialisation format in its
// little-endian normal form, which is how OSTree stores every object a
// Flatpak repository serves. Only the subset of the type grammar those objects
// use is supported: basic types, strings, arrays, tuples, dictionary entries,
// variants and maybes.
//
// Values decode to plain Go values: booleans to bool, integers to their sized
// Go type, s/o/g to string, ay to []byte, other arrays to []any, tuples and
// dictionary entries to []any, variants to Variant and maybes to nil or the
// contained value.
package gvariant

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Variant is a value carried together with its own type.
type Variant struct {
	Signature string
	Value     any
}

var ErrMalformed = errors.New("malformed gvariant data")

type typeInfo struct {
	signature string
	kind      byte
	elem      *typeInfo
	members   []*typeInfo
	align     int
	size      int // 0 when the type is variable-sized
}

func parseType(signature string) (*typeInfo, error) {
	info, rest, err := parseOne(signature)
	if err != nil {
		return nil, err
	}
	if rest != "" {
		return nil, fmt.Errorf("gvariant: trailing type data %q in %q", rest, signature)
	}
	return info, nil
}

func parseOne(signature string) (*typeInfo, string, error) {
	if signature == "" {
		return nil, "", errors.New("gvariant: empty type")
	}
	kind := signature[0]
	rest := signature[1:]
	switch kind {
	case 'b', 'y':
		return &typeInfo{signature: signature[:1], kind: kind, align: 1, size: 1}, rest, nil
	case 'n', 'q':
		return &typeInfo{signature: signature[:1], kind: kind, align: 2, size: 2}, rest, nil
	case 'i', 'u', 'h':
		return &typeInfo{signature: signature[:1], kind: kind, align: 4, size: 4}, rest, nil
	case 'x', 't', 'd':
		return &typeInfo{signature: signature[:1], kind: kind, align: 8, size: 8}, rest, nil
	case 's', 'o', 'g':
		return &typeInfo{signature: signature[:1], kind: kind, align: 1}, rest, nil
	case 'v':
		return &typeInfo{signature: signature[:1], kind: kind, align: 8}, rest, nil
	case 'a', 'm':
		elem, after, err := parseOne(rest)
		if err != nil {
			return nil, "", err
		}
		info := &typeInfo{signature: signature[:len(signature)-len(after)], kind: kind, elem: elem, align: elem.align}
		return info, after, nil
	case '(', '{':
		closing := byte(')')
		if kind == '{' {
			closing = '}'
		}
		info := &typeInfo{kind: kind, align: 1}
		for {
			if rest == "" {
				return nil, "", fmt.Errorf("gvariant: unterminated container in %q", signature)
			}
			if rest[0] == closing {
				rest = rest[1:]
				break
			}
			member, after, err := parseOne(rest)
			if err != nil {
				return nil, "", err
			}
			info.members = append(info.members, member)
			info.align = max(info.align, member.align)
			rest = after
		}
		if kind == '{' && len(info.members) != 2 {
			return nil, "", fmt.Errorf("gvariant: dictionary entry needs two members in %q", signature)
		}
		info.signature = signature[:len(signature)-len(rest)]
		info.size = fixedContainerSize(info)
		return info, rest, nil
	}
	return nil, "", fmt.Errorf("gvariant: unsupported type %q", string(kind))
}

// fixedContainerSize answers with the size of a tuple whose members are all
// fixed-sized, which the format pads to its own alignment. The empty tuple is
// the one fixed-sized value with no members, and it takes one byte.
func fixedContainerSize(info *typeInfo) int {
	if len(info.members) == 0 {
		return 1
	}
	offset := 0
	for _, member := range info.members {
		if member.size == 0 {
			return 0
		}
		offset = alignUp(offset, member.align) + member.size
	}
	return alignUp(offset, info.align)
}

func alignUp(offset, align int) int {
	return (offset + align - 1) &^ (align - 1)
}

func offsetSize(total int) int {
	switch {
	case total == 0:
		return 0
	case total <= math.MaxUint8:
		return 1
	case total <= math.MaxUint16:
		return 2
	case uint64(total) <= math.MaxUint32:
		return 4
	}
	return 8
}

func readOffset(data []byte, size int) int {
	switch size {
	case 1:
		return int(data[0])
	case 2:
		return int(binary.LittleEndian.Uint16(data))
	case 4:
		return int(binary.LittleEndian.Uint32(data))
	}
	value := binary.LittleEndian.Uint64(data)
	if value > math.MaxInt32 {
		return -1
	}
	return int(value)
}

// Parse decodes data as a value of the given type signature.
func Parse(signature string, data []byte) (any, error) {
	info, err := parseType(signature)
	if err != nil {
		return nil, err
	}
	return decode(info, data, 0)
}

const maxDepth = 64

func decode(info *typeInfo, data []byte, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: nesting deeper than %d", ErrMalformed, maxDepth)
	}
	if info.size != 0 && len(data) != info.size {
		return nil, fmt.Errorf("%w: %s needs %d bytes, got %d", ErrMalformed, info.signature, info.size, len(data))
	}
	switch info.kind {
	case 'b':
		return data[0] != 0, nil
	case 'y':
		return data[0], nil
	case 'n':
		return int16(binary.LittleEndian.Uint16(data)), nil
	case 'q':
		return binary.LittleEndian.Uint16(data), nil
	case 'i', 'h':
		return int32(binary.LittleEndian.Uint32(data)), nil
	case 'u':
		return binary.LittleEndian.Uint32(data), nil
	case 'x':
		return int64(binary.LittleEndian.Uint64(data)), nil
	case 't':
		return binary.LittleEndian.Uint64(data), nil
	case 'd':
		return math.Float64frombits(binary.LittleEndian.Uint64(data)), nil
	case 's', 'o', 'g':
		if len(data) == 0 || data[len(data)-1] != 0 {
			return nil, fmt.Errorf("%w: string is not terminated", ErrMalformed)
		}
		value := data[:len(data)-1]
		if bytes.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("%w: string holds a NUL byte", ErrMalformed)
		}
		return string(value), nil
	case 'v':
		separator := bytes.LastIndexByte(data, 0)
		if separator < 0 {
			return nil, fmt.Errorf("%w: variant carries no type", ErrMalformed)
		}
		inner, err := parseType(string(data[separator+1:]))
		if err != nil {
			return nil, fmt.Errorf("%w: variant type: %v", ErrMalformed, err)
		}
		value, err := decode(inner, data[:separator], depth+1)
		if err != nil {
			return nil, err
		}
		return Variant{Signature: inner.signature, Value: value}, nil
	case 'm':
		if len(data) == 0 {
			return nil, nil
		}
		if info.elem.size != 0 {
			return decode(info.elem, data, depth+1)
		}
		return decode(info.elem, data[:len(data)-1], depth+1)
	case 'a':
		return decodeArray(info, data, depth)
	case '(', '{':
		return decodeTuple(info, data, depth)
	}
	return nil, fmt.Errorf("gvariant: unsupported type %q", info.signature)
}

func decodeArray(info *typeInfo, data []byte, depth int) (any, error) {
	elem := info.elem
	if elem.kind == 'y' {
		return append([]byte{}, data...), nil
	}
	if elem.size != 0 {
		if len(data)%elem.size != 0 {
			return nil, fmt.Errorf("%w: %s length %d is not a multiple of %d", ErrMalformed, info.signature, len(data), elem.size)
		}
		values := make([]any, 0, len(data)/elem.size)
		for start := 0; start < len(data); start += elem.size {
			value, err := decode(elem, data[start:start+elem.size], depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	if len(data) == 0 {
		return []any{}, nil
	}
	size := offsetSize(len(data))
	if len(data) < size {
		return nil, fmt.Errorf("%w: %s is shorter than its framing", ErrMalformed, info.signature)
	}
	tableStart := readOffset(data[len(data)-size:], size)
	if tableStart < 0 || tableStart > len(data)-size || (len(data)-tableStart)%size != 0 {
		return nil, fmt.Errorf("%w: %s framing offset is out of range", ErrMalformed, info.signature)
	}
	count := (len(data) - tableStart) / size
	values := make([]any, 0, count)
	start := 0
	for index := range count {
		end := readOffset(data[tableStart+index*size:], size)
		start = alignUp(start, elem.align)
		if end < start || end > tableStart {
			return nil, fmt.Errorf("%w: %s element %d is out of range", ErrMalformed, info.signature, index)
		}
		value, err := decode(elem, data[start:end], depth+1)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		start = end
	}
	return values, nil
}

func decodeTuple(info *typeInfo, data []byte, depth int) (any, error) {
	values := make([]any, 0, len(info.members))
	size := offsetSize(len(data))
	framing := len(data)
	position := 0
	for index, member := range info.members {
		position = alignUp(position, member.align)
		var end int
		switch {
		case member.size != 0:
			end = position + member.size
		case index == len(info.members)-1:
			end = framing
		default:
			framing -= size
			if framing < 0 {
				return nil, fmt.Errorf("%w: %s framing is truncated", ErrMalformed, info.signature)
			}
			end = readOffset(data[framing:], size)
		}
		if position > len(data) || end < position || end > framing {
			return nil, fmt.Errorf("%w: %s member %d is out of range", ErrMalformed, info.signature, index)
		}
		value, err := decode(member, data[position:end], depth+1)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		position = end
	}
	return values, nil
}

// VariantDict reads an a{sv} value as decoded by Parse.
func VariantDict(value any) (map[string]Variant, error) {
	entries, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: expected a{sv}, got %T", ErrMalformed, value)
	}
	dict := make(map[string]Variant, len(entries))
	for _, entry := range entries {
		pair, ok := entry.([]any)
		if !ok || len(pair) != 2 {
			return nil, fmt.Errorf("%w: expected a dictionary entry", ErrMalformed)
		}
		key, keyOK := pair[0].(string)
		item, itemOK := pair[1].(Variant)
		if !keyOK || !itemOK {
			return nil, fmt.Errorf("%w: expected a {sv} entry", ErrMalformed)
		}
		dict[key] = item
	}
	return dict, nil
}

// Encode serialises value as the given type signature. The value must have
// the shape Parse would produce for that signature.
func Encode(signature string, value any) ([]byte, error) {
	info, err := parseType(signature)
	if err != nil {
		return nil, err
	}
	return encode(info, value)
}

func encode(info *typeInfo, value any) ([]byte, error) {
	switch info.kind {
	case 'b':
		flag, ok := value.(bool)
		if !ok {
			return nil, typeError(info, value)
		}
		if flag {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case 'y':
		number, ok := value.(byte)
		if !ok {
			return nil, typeError(info, value)
		}
		return []byte{number}, nil
	case 'n':
		number, ok := value.(int16)
		if !ok {
			return nil, typeError(info, value)
		}
		return binary.LittleEndian.AppendUint16(nil, uint16(number)), nil
	case 'q':
		number, ok := value.(uint16)
		if !ok {
			return nil, typeError(info, value)
		}
		return binary.LittleEndian.AppendUint16(nil, number), nil
	case 'i', 'h':
		number, ok := value.(int32)
		if !ok {
			return nil, typeError(info, value)
		}
		return binary.LittleEndian.AppendUint32(nil, uint32(number)), nil
	case 'u':
		number, ok := value.(uint32)
		if !ok {
			return nil, typeError(info, value)
		}
		return binary.LittleEndian.AppendUint32(nil, number), nil
	case 'x':
		number, ok := value.(int64)
		if !ok {
			return nil, typeError(info, value)
		}
		return binary.LittleEndian.AppendUint64(nil, uint64(number)), nil
	case 't':
		number, ok := value.(uint64)
		if !ok {
			return nil, typeError(info, value)
		}
		return binary.LittleEndian.AppendUint64(nil, number), nil
	case 'd':
		number, ok := value.(float64)
		if !ok {
			return nil, typeError(info, value)
		}
		return binary.LittleEndian.AppendUint64(nil, math.Float64bits(number)), nil
	case 's', 'o', 'g':
		text, ok := value.(string)
		if !ok || bytes.IndexByte([]byte(text), 0) >= 0 {
			return nil, typeError(info, value)
		}
		return append([]byte(text), 0), nil
	case 'v':
		variant, ok := value.(Variant)
		if !ok {
			return nil, typeError(info, value)
		}
		inner, err := parseType(variant.Signature)
		if err != nil {
			return nil, err
		}
		data, err := encode(inner, variant.Value)
		if err != nil {
			return nil, err
		}
		data = append(data, 0)
		return append(data, inner.signature...), nil
	case 'm':
		if value == nil {
			return []byte{}, nil
		}
		data, err := encode(info.elem, value)
		if err != nil {
			return nil, err
		}
		if info.elem.size == 0 {
			data = append(data, 0)
		}
		return data, nil
	case 'a':
		return encodeArray(info, value)
	case '(', '{':
		return encodeTuple(info, value)
	}
	return nil, fmt.Errorf("gvariant: unsupported type %q", info.signature)
}

func typeError(info *typeInfo, value any) error {
	return fmt.Errorf("gvariant: %T cannot be encoded as %s", value, info.signature)
}

func encodeArray(info *typeInfo, value any) ([]byte, error) {
	if info.elem.kind == 'y' {
		data, ok := value.([]byte)
		if !ok {
			return nil, typeError(info, value)
		}
		return append([]byte(nil), data...), nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, typeError(info, value)
	}
	var body []byte
	ends := make([]int, 0, len(items))
	for _, item := range items {
		body = pad(body, info.elem.align)
		data, err := encode(info.elem, item)
		if err != nil {
			return nil, err
		}
		body = append(body, data...)
		ends = append(ends, len(body))
	}
	if info.elem.size != 0 {
		return body, nil
	}
	return appendFraming(body, ends), nil
}

func encodeTuple(info *typeInfo, value any) ([]byte, error) {
	items, ok := value.([]any)
	if !ok || len(items) != len(info.members) {
		return nil, typeError(info, value)
	}
	if len(items) == 0 {
		return []byte{0}, nil
	}
	var body []byte
	ends := []int{}
	for index, member := range info.members {
		body = pad(body, member.align)
		data, err := encode(member, items[index])
		if err != nil {
			return nil, err
		}
		body = append(body, data...)
		if member.size == 0 && index != len(info.members)-1 {
			ends = append(ends, len(body))
		}
	}
	if info.size != 0 {
		return pad(body, info.align), nil
	}
	// A tuple stores the end of each framed member in reverse order, so the
	// first one is the last offset in the data.
	for left, right := 0, len(ends)-1; left < right; left, right = left+1, right-1 {
		ends[left], ends[right] = ends[right], ends[left]
	}
	return appendFraming(body, ends), nil
}

func pad(data []byte, align int) []byte {
	for len(data)%align != 0 {
		data = append(data, 0)
	}
	return data
}

func appendFraming(body []byte, ends []int) []byte {
	if len(ends) == 0 {
		return body
	}
	size := 1
	for offsetSize(len(body)+len(ends)*size) > size {
		size *= 2
	}
	for _, end := range ends {
		switch size {
		case 1:
			body = append(body, byte(end))
		case 2:
			body = binary.LittleEndian.AppendUint16(body, uint16(end))
		case 4:
			body = binary.LittleEndian.AppendUint32(body, uint32(end))
		default:
			body = binary.LittleEndian.AppendUint64(body, uint64(end))
		}
	}
	return body
}
