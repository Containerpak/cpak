/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package oci

import (
	"context"
	"io"
)

const blobResumeAttempts = 3

type resumableBlob struct {
	ctx        context.Context
	client     *Client
	ref        Reference
	descriptor Descriptor
	body       io.ReadCloser
	offset     int64
	attempts   int
}

// ResumableBlob opens an OCI blob and resumes interrupted reads with verified
// range requests.
func (c *Client) ResumableBlob(ctx context.Context, ref Reference, descriptor Descriptor) (io.ReadCloser, error) {
	body, err := c.Blob(ctx, ref, descriptor)
	if err != nil {
		return nil, err
	}
	return &resumableBlob{
		ctx:        ctx,
		client:     c,
		ref:        ref,
		descriptor: descriptor,
		body:       body,
	}, nil
}

func (b *resumableBlob) Read(data []byte) (int, error) {
	for {
		read, err := b.body.Read(data)
		b.offset += int64(read)
		if err == nil || b.offset >= b.descriptor.Size {
			return read, err
		}
		if b.ctx.Err() != nil || b.attempts >= blobResumeAttempts {
			return read, err
		}
		b.body.Close()
		remaining := b.descriptor.Size - b.offset
		b.body, err = b.client.BlobRange(b.ctx, b.ref, b.descriptor, b.offset, remaining)
		if err != nil {
			return read, err
		}
		b.attempts++
		if read != 0 {
			return read, nil
		}
	}
}

func (b *resumableBlob) Close() error {
	return b.body.Close()
}
