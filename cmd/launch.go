/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/sandbox"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type LaunchCmd struct {
	UserNamespaces     bool     `cli:"user-namespaces" help:"allow application-created user namespaces"`
	AllowPtrace        bool     `cli:"allow-ptrace" help:"allow tracing inside a private process namespace"`
	RequireSandbox     bool     `cli:"require-sandbox" help:"fail if filesystem or syscall restrictions are unavailable"`
	LandlockReadOnly   []string `cli:"landlock-read-only" help:"grant read-only filesystem access"`
	LandlockWriteFiles []string `cli:"landlock-write-files" help:"grant writes to existing files"`
	LandlockReadWrite  []string `cli:"landlock-read-write" help:"grant read-write filesystem access"`
	ExtraArgs          []string `arg:"extra" help:"command and arguments"`

	cli.Base
}

func (c *LaunchCmd) Run() error {
	logger.ProxyMode()
	if len(c.ExtraArgs) == 0 {
		return fmt.Errorf("command is required")
	}
	grants := c.landlockGrants()
	if len(grants) == 0 {
		return fmt.Errorf("landlock grants are required: launch runs a command inside an existing sandbox, use cpak run to start a package")
	}
	_, landlockErr := sandbox.ApplyLandlock(grants)
	if err := c.sandboxOutcome(landlockErr, landlockUnavailable(grants)); err != nil {
		return err
	}
	if err := c.sandboxOutcome(sandbox.ApplySeccomp(c.UserNamespaces, c.AllowPtrace), seccompUnavailable()); err != nil {
		return err
	}
	path, err := exec.LookPath(c.ExtraArgs[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, c.ExtraArgs, os.Environ())
}

// sandboxNotice is what an operator is told when the kernel will not give one
// of the two barriers a launch establishes.
//
// It is a block of prose and not a log line on purpose. What is lost is the
// enforcement of the permissions the user agreed to when they installed the
// package, it is lost for as long as the application runs, and the one line
// this used to print scrolled past in a log nobody was reading, next to the
// program's own output, saying the name of a kernel feature and nothing about
// what it was for.
type sandboxNotice struct {
	// name is the restriction, for the one-line refusal on cpak's own launches.
	name string

	// headline is what is wrong, in the words of somebody who has to decide
	// whether to keep using this machine this way.
	headline string

	// lines are the body, already broken, because the shape is the half of an
	// unmissable message that a formatter cannot be trusted with.
	lines []string

	// remedy is how to get the restriction back, said in one clause so it fits
	// inside an error as well as inside the block.
	remedy string
}

// sandboxNoticeWriter is where a notice goes.
//
// The error stream, because it belongs to cpak: the output stream is the one
// the program about to be executed will write on, and a caller reading that
// output is entitled to find nothing of cpak's in it. It is a variable so that
// a test can read what an operator would have read.
var sandboxNoticeWriter io.Writer = os.Stderr

// sandboxNoticeWidth is the rule that frames a notice. Narrow enough to survive
// the default terminal and the frames the container init wraps this output in.
const sandboxNoticeWidth = 74

func (n sandboxNotice) report(out io.Writer) {
	rule := strings.Repeat("=", sandboxNoticeWidth)
	lines := []string{"", rule, " cpak: " + n.headline, rule}
	lines = append(lines, n.lines...)
	lines = append(lines, rule, "")
	fmt.Fprintln(out, strings.Join(lines, "\n"))
}

// sandboxOutcome decides what a restriction the kernel would not give means for
// this launch.
//
// It means the application starts. A machine whose kernel has no Landlock is a
// machine cpak still has to run on, and software that refuses to start on it
// teaches its owner to turn the protection off rather than to go and get it;
// the applications that would refuse are the ones that write to the user's own
// files, which is to say the ones people actually installed cpak for.
//
// What it must never mean is that nobody was told. The barrier the user was
// promised is not there, so it is said in full, at every launch that runs
// without it, where the launch happens.
//
// cpak's own launches are the exception, and they ask for it themselves with
// --require-sandbox: the storage driver running inside a container cpak built
// for it is not somebody's application, nothing of theirs is lost if it does
// not start, and it is cpak's own policy to refuse rather than to weaken it.
func (c *LaunchCmd) sandboxOutcome(err error, notice sandboxNotice) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, sandbox.ErrUnavailable) {
		return err
	}
	if c.RequireSandbox {
		return fmt.Errorf(
			"%s is unavailable and this is one of cpak's own launches, which are not run without it: %s",
			notice.name,
			notice.remedy,
		)
	}
	notice.report(sandboxNoticeWriter)
	return nil
}

// landlockUnavailable is the notice for the barrier that holds an application
// to the files it was given.
//
// It is written for somebody who has never heard of Landlock, because that is
// who is reading it: what is missing, what it would have refused, what is still
// standing, and what to do to get it back. Naming the feature and stopping
// there tells that reader nothing they can act on.
func landlockUnavailable(grants []sandbox.PathGrant) sandboxNotice {
	lines := []string{
		"",
		" This application is running WITHOUT the file restrictions it was",
		" installed with. It has been started anyway.",
		"",
		" Landlock is the part of the Linux kernel cpak uses to hold a running",
		" application to the files it was given. This kernel does not offer it,",
		" so cpak could not apply it, and nothing is enforcing that list now.",
		"",
		" What it would have refused, to this program and to every program it",
		" starts, for as long as it runs:",
		"   every write to a path the application was not given. Its own",
		"   installed files, the system directories inside its container and",
		"   anything else mounted there that is not already held read-only are",
		"   all writable now, and what it writes there stays for as long as",
		"   that container lives. Landlock would have gone on refusing those",
		"   writes after the application had been broken into, which is the",
		"   moment all of this is for.",
		"",
	}
	lines = append(lines, writableGrantLines(grants)...)
	lines = append(lines,
		"",
		" What is still standing:",
		"   the application sees only the files cpak mounted into its container,",
		"   and what cpak mounted read-only is still read-only. Everything it",
		"   was never given is still out of reach. What is gone is the second",
		"   barrier, the one that holds when the first one turns out to be",
		"   wrong.",
		"",
		" How to get it back:",
		"   boot a kernel that offers Landlock. Distributions have built it in",
		"   since 5.13, and it is switched off when the kernel is booted with an",
		"   lsm= list that leaves landlock out. This machine says which ones it",
		"   has here:",
		"     cat /sys/kernel/security/lsm",
	)
	return sandboxNotice{
		name:     "Landlock",
		headline: "THIS APPLICATION IS NOT HELD TO THE FILES IT WAS GIVEN",
		lines:    lines,
		remedy:   "boot the kernel with landlock in its lsm= list",
	}
}

// seccompUnavailable is the notice for the other barrier: which of the kernel's
// own operations the application may ask for at all.
func seccompUnavailable() sandboxNotice {
	return sandboxNotice{
		name:     "Seccomp",
		headline: "THIS APPLICATION MAY ASK THE KERNEL FOR ANYTHING",
		lines: []string{
			"",
			" This application is running WITHOUT the system call filter it was",
			" installed with. It has been started anyway.",
			"",
			" Seccomp is the part of the Linux kernel that decides which of the",
			" kernel's own operations a program is allowed to ask for. cpak gives",
			" every application a list that leaves out the operations a program",
			" uses to get at the rest of the machine. This kernel would not take",
			" that list, so every operation now reaches the kernel and only the",
			" kernel's own permission checks stand in the way.",
			"",
			" What it would have refused:",
			"   tracing other running programs, loading BPF programs or kernel",
			"   modules, joining another process's namespaces, rebooting the",
			"   machine, and building namespaces of its own, which is the usual",
			"   first step out of a sandbox like this one.",
			"",
			" How to get it back:",
			"   the kernel needs CONFIG_SECCOMP_FILTER, and whatever started cpak",
			"   has to allow prctl(PR_SET_SECCOMP). A sandbox wrapped around cpak",
			"   that blocks it takes this barrier from every application cpak",
			"   runs, not only from this one.",
		},
		remedy: "the kernel needs CONFIG_SECCOMP_FILTER and the sandbox around cpak has to allow prctl(PR_SET_SECCOMP)",
	}
}

// listedGrants is how many paths a notice names before it stops. A container
// grants a few dozen and a reader stops long before that, so the rest are
// counted: a notice nobody finishes is a notice nobody read.
const listedGrants = 12

// writableGrantLines names the paths this application was allowed to write.
//
// Those are the ones the reader can weigh: a package granted the whole home has
// lost less to a missing Landlock than one granted a single directory, and the
// paths say which of the two this is. The read-only grants are counted instead
// of listed, because most of them are the container's own filesystem.
func writableGrantLines(grants []sandbox.PathGrant) []string {
	writable := []string{}
	readable := 0
	for _, grant := range grants {
		if grant.ReadOnly && !grant.WriteFiles {
			readable++
			continue
		}
		writable = append(writable, grant.Path)
	}
	if len(writable) == 0 {
		return []string{
			" It was given nothing to write outside its own container, and the",
			fmt.Sprintf(" %d paths it may read are now bounded by its mounts alone.", readable),
		}
	}
	lines := []string{" It was given these paths to write, and they are the ones nothing is", " holding it to any more:"}
	for index, path := range writable {
		if index == listedGrants {
			lines = append(lines, fmt.Sprintf("   and %d more", len(writable)-listedGrants))
			break
		}
		lines = append(lines, "   "+path)
	}
	return lines
}

func (c *LaunchCmd) landlockGrants() []sandbox.PathGrant {
	grants := make([]sandbox.PathGrant, 0, len(c.LandlockReadOnly)+len(c.LandlockWriteFiles)+len(c.LandlockReadWrite))
	for _, path := range c.LandlockReadOnly {
		grants = append(grants, sandbox.PathGrant{Path: path, ReadOnly: true})
	}
	for _, path := range c.LandlockWriteFiles {
		grants = append(grants, sandbox.PathGrant{Path: path, ReadOnly: true, WriteFiles: true})
	}
	for _, path := range c.LandlockReadWrite {
		grants = append(grants, sandbox.PathGrant{Path: path})
	}
	return grants
}
