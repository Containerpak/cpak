/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"time"
)

type ProgressRequest struct {
	Title   string
	Heading string
	Detail  string
	IconPNG []byte
}

type ProgressUpdate struct {
	Message string
	Current int64
	Total   int64
}

func Progress(backend Backend, request ProgressRequest, action func(func(ProgressUpdate)) error) error {
	updates := make(chan ProgressUpdate, 32)
	done := make(chan error, 1)
	go func() {
		done <- action(func(update ProgressUpdate) {
			select {
			case updates <- update:
			default:
			}
		})
	}()

	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
	}

	switch backend {
	case BackendAdwaita, BackendGTK, BackendKDE, BackendQt:
		handled, err := runAdapterProgress(backend, request, updates, done)
		if handled {
			return err
		}
	}
	return progressBuiltin(request, updates, done)
}

func progressPercentage(update ProgressUpdate) int {
	if update.Total <= 0 || update.Current <= 0 {
		return 0
	}
	percentage := update.Current * 100 / update.Total
	if percentage > 100 {
		return 100
	}
	return int(percentage)
}
