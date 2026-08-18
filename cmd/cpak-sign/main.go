/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Command cpak-sign signs the two things a publisher can determine off the
// machine that installs their software: the capsule an installer ships as, and
// the state a package is in at one moment, which is its manifest, the image
// that manifest resolved to and a generation.
//
// A state is never signed over a tag. A tag can be repointed at another image
// after it is signed, so the tag is resolved here and the digest it resolved to
// is what enters the payload; the signature is the pin.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	arguments := os.Args[1:]
	command := ""
	if len(arguments) > 0 && (!strings.HasPrefix(arguments[0], "-") || arguments[0] == "-h" || arguments[0] == "--help") {
		command = arguments[0]
		arguments = arguments[1:]
	}

	var err error
	switch command {
	case "", "capsule":
		err = signCapsule(arguments)
	case "state":
		err = buildState(arguments)
	case "attach":
		err = attachSignature(arguments)
	case "approve":
		err = approveState(arguments)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		err = fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		fail(err)
	}
}

func usage(writer io.Writer) {
	fmt.Fprint(writer, `usage: cpak-sign <command> [options]

  capsule  sign a packed installer capsule with an Ed25519 key
  state    build the package state a publisher signs, with the image tag
           resolved to the digest the payload carries
  attach   attach a signed state to its image as an OCI referrer
  approve  attach an organisation's counter-signature over a state its
           publisher already signed

Called with no command, cpak-sign signs a capsule.
`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
