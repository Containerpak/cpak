/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import "time"

const (
	FlatpakPhaseResolve  = "resolve"
	FlatpakPhaseDownload = "download"
	FlatpakPhaseInstall  = "install"
)

// FlatpakProgress is one step of a Flatpak installation or update, for a
// program that drives cpak. A download counts the objects of one commit:
// OSTree does not say how large a file is before it is fetched, so there is
// no total of bytes to report, only how many were read so far.
type FlatpakProgress struct {
	Phase        string `json:"phase"`
	Ref          string `json:"ref,omitempty"`
	ObjectsDone  int    `json:"objects_done"`
	ObjectsTotal int    `json:"objects_total"`
	BytesDone    int64  `json:"bytes_done"`
}

// SetFlatpakProgress hands every progress step to report instead of drawing
// a progress bar.
func (c *Cpak) SetFlatpakProgress(report func(FlatpakProgress)) {
	c.flatpakProgress = report
}

func (c *Cpak) reportFlatpak(progress FlatpakProgress) {
	if c.flatpakProgress != nil {
		c.flatpakProgress(progress)
	}
}

// downloadReporter reports the objects of one commit as they are verified, at
// most every tenth of a second, and always the first and the last.
type downloadReporter struct {
	cpak     *Cpak
	progress FlatpakProgress
	last     time.Time
}

func (c *Cpak) newDownloadReporter(ref string, done, total int) *downloadReporter {
	reporter := &downloadReporter{cpak: c, progress: FlatpakProgress{Phase: FlatpakPhaseDownload, Ref: ref, ObjectsDone: done, ObjectsTotal: total}}
	reporter.report()
	return reporter
}

func (r *downloadReporter) add(size int64) {
	r.progress.ObjectsDone++
	r.progress.BytesDone += size
	if r.progress.ObjectsDone == r.progress.ObjectsTotal || time.Since(r.last) >= 100*time.Millisecond {
		r.report()
	}
}

func (r *downloadReporter) report() {
	r.last = time.Now()
	r.cpak.reportFlatpak(r.progress)
}
