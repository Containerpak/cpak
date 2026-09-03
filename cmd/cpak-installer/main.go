/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"golang.org/x/term"
)

type progressFunc func(string)

var (
	appArmorRestricted        = systemauthority.AppArmorUserNamespacesRestricted
	appArmorRuntimeExecutable = systemauthority.AppArmorRuntimeExecutable
	appArmorRuntimeMatches    = systemauthority.SameExecutableContents
)

func main() {
	forceTerminal := flag.Bool("terminal", false, "use the terminal interface")
	inspect := flag.Bool("inspect", false, "print verified package metadata")
	flag.Parse()

	capsule, err := readSelf()
	if err != nil {
		fail(err)
	}
	desktopui.SetBrandIcon(capsule.BrandIcon)
	if *inspect {
		encoded, _ := json.MarshalIndent(capsule.Metadata, "", "  ")
		fmt.Println(string(encoded))
		return
	}

	if !*forceTerminal && !term.IsTerminal(int(os.Stdin.Fd())) && (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") {
		backend := desktopui.SelectBackend("")
		handled, nativeErr := desktopui.Install(backend, capsule.Metadata.Name, capsule.Metadata.Description, capsule.Metadata.Origin, permissionLines(capsule.Metadata.Permissions), func(progress func(string)) error {
			return install(capsule, progress)
		})
		if handled {
			if nativeErr != nil {
				fail(nativeErr)
			}
			return
		}
		runGUI(capsule)
		return
	}
	if err = runTerminal(capsule); err != nil {
		fail(err)
	}
}

func readSelf() (bootstrap.Capsule, error) {
	path, err := os.Executable()
	if err != nil {
		return bootstrap.Capsule{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return bootstrap.Capsule{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return bootstrap.Capsule{}, err
	}
	key, err := bootstrap.InstallerPublicKey()
	if err != nil {
		return bootstrap.Capsule{}, err
	}
	return bootstrap.ReadCapsule(file, stat.Size(), key)
}

func runTerminal(capsule bootstrap.Capsule) error {
	m := capsule.Metadata
	fmt.Printf("Install %s\n\n%s\n\nOrigin: %s\n", m.Name, m.Description, m.Origin)
	if m.Ref != "" {
		fmt.Printf("Reference: %s %s\n", m.RefType, m.Ref)
	}
	if len(m.Permissions) > 0 {
		fmt.Println("\nPermissions requested:")
		for _, line := range permissionLines(m.Permissions) {
			fmt.Println("  " + line)
		}
	}
	fmt.Print("\nInstall this application? [Y/n] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "" && answer != "y" && answer != "yes" {
		return nil
	}
	return install(capsule, func(message string) {
		fmt.Println(message)
	})
}

func permissionLines(permissions []bootstrap.Permission) []string {
	result := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		result = append(result, permission.Name+": "+permission.Detail)
	}
	return result
}

func install(capsule bootstrap.Capsule, progress progressFunc) error {
	storageChanged := false
	if len(capsule.Companion) > 0 {
		changed, err := installStorageService(capsule.Companion)
		if err != nil {
			return err
		}
		if changed {
			progress("Installed the cpak storage service in ~/.local/bin")
		}
		storageChanged = changed
	}
	cpakPath, changed, err := installCpak(capsule.Payload)
	if err != nil {
		return err
	}
	if changed {
		progress("Installed cpak in ~/.local/bin")
	} else {
		progress("cpak is ready")
	}
	if appArmorRestricted() {
		target, ready := appArmorRuntimeExecutable()
		if !ready || !appArmorRuntimeMatches(cpakPath, target) {
			progress("Preparing cpak for Ubuntu's user namespace policy")
			if err := runCommand(cpakPath, []string{"system", "setup"}, progress); err != nil {
				return err
			}
		}
	}
	if changed || storageChanged {
		progress("Preparing cpak storage")
		if err := runCommand(cpakPath, []string{"storage", "migrate"}, progress); err != nil {
			return err
		}
	}

	metadata := capsule.Metadata
	args := []string{"install", "--commit", metadata.Ref, "--signed-installer"}
	args = append(args, metadata.Origin)
	progress("Resolving " + metadata.Name)
	return runCommand(cpakPath, args, progress)
}

func installCpak(payload []byte) (string, bool, error) {
	return installBinary("cpak", payload)
}

func installStorageService(payload []byte) (bool, error) {
	_, changed, err := installBinary("cpak-storaged", payload)
	return changed, err
}

func installBinary(name string, payload []byte) (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	dir := filepath.Join(home, ".local", "bin")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return "", false, err
	}
	target := filepath.Join(dir, name)
	wanted := sha256.Sum256(payload)
	if current, readErr := os.ReadFile(target); readErr == nil && sha256.Sum256(current) == wanted {
		return target, false, nil
	}

	temporary, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return "", false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0755); err == nil {
		_, err = temporary.Write(payload)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", false, err
	}
	if err = os.Rename(temporaryPath, target); err != nil {
		return "", false, err
	}
	return target, true, nil
}

func runCommand(path string, args []string, progress progressFunc) error {
	command := exec.Command(path, args...)
	reader, writer := io.Pipe()
	command.Stdout = writer
	command.Stderr = writer
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		_ = writer.Close()
	}()

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			progress(line)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	return <-done
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}
