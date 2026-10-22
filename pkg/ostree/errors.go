/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package ostree

import (
	"errors"
	"io"
)

var (
	// ErrVerification marks an answer that failed its signature or its
	// checksum: the repository served something the trusted keys do not
	// vouch for.
	ErrVerification = errors.New("ostree: verification failed")

	// ErrUnavailable marks a repository that could not be reached, or that
	// failed while answering.
	ErrUnavailable = errors.New("ostree: repository unavailable")
)

// markedError keeps the message of err and lets errors.Is find the mark
// beside it, so callers can tell failures apart without reading text.
type markedError struct {
	err  error
	mark error
}

func (e markedError) Error() string   { return e.err.Error() }
func (e markedError) Unwrap() []error { return []error{e.err, e.mark} }

func verificationFailure(err error) error {
	return markedError{err: err, mark: ErrVerification}
}

func unavailable(err error) error {
	return markedError{err: err, mark: ErrUnavailable}
}

// unavailableReader marks what goes wrong while a response body is read, so a
// connection dropped half way is told apart from a failure to write it.
type unavailableReader struct {
	reader io.Reader
}

func (r unavailableReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && err != io.EOF {
		err = unavailable(err)
	}
	return n, err
}
