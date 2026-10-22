/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/runtimeproto"
)

// The Flatpak portal is how a Flatpak application starts a helper process:
// image decoders, Chromium's renderers, language servers. Under Flatpak the
// helper can be put in a narrower sandbox or even on the host. Under cpak it
// is started by the container's own runtime, beside the application and with
// exactly its restrictions: never on the host, never with more than the
// application has, whatever the call asks for.

const (
	flatpakPortalName      = "org.freedesktop.portal.Flatpak"
	flatpakPortalPath      = dbus.ObjectPath("/org/freedesktop/portal/Flatpak")
	flatpakPortalInterface = "org.freedesktop.portal.Flatpak"

	spawnFlagNotifyStart = 64
	spawnFlagWatchBus    = 16

	maxSpawnArguments = 4096
)

type spawned struct {
	connection *net.UnixConn
	watchBus   bool
}

func (b *portalBus) flatpakMethod(client *serializedConn, connection *portalConnection, call *dbus.Message, fds []int, path dbus.ObjectPath, interfaceName, member string) {
	if path != flatpakPortalPath {
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "no such object")
		return
	}
	switch interfaceName {
	case propertiesInterface:
		values := map[string]dbus.Variant{"version": dbus.MakeVariant(uint32(7)), "supports": dbus.MakeVariant(uint32(0))}
		switch member {
		case "Get":
			if len(call.Body) == 2 {
				if value, ok := values[call.Body[1].(string)]; ok && call.Body[0] == flatpakPortalInterface {
					b.reply(client, connection, call, portalUniqueName, value)
					return
				}
			}
			b.fail(client, connection, call, portalUniqueName, "org.freedesktop.DBus.Error.UnknownProperty", "unknown property")
		case "GetAll":
			b.reply(client, connection, call, portalUniqueName, values)
		default:
			b.fail(client, connection, call, portalUniqueName, errorAccessDenied, "portal properties are read-only")
		}
		return
	case flatpakPortalInterface:
	default:
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown interface")
		return
	}
	switch member {
	case "Spawn":
		b.spawn(client, connection, call, fds)
	case "SpawnSignal":
		if len(call.Body) != 3 {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "expected a process, a signal and a group flag")
			return
		}
		pid, _ := call.Body[0].(uint32)
		connection.mu.Lock()
		process := connection.spawned[pid]
		connection.mu.Unlock()
		if process == nil {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "no such process")
			return
		}
		// The runtime ends a command whose caller hangs up, which is the
		// one signal cpak delivers: anything asked for stops the process.
		_ = process.connection.Close()
		b.reply(client, connection, call, portalUniqueName)
	case "CreateUpdateMonitor":
		b.fail(client, connection, call, portalUniqueName, errorNotAllowed, "cpak updates applications itself")
	default:
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
	}
}

type spawnRequest struct {
	request     runtimeproto.Request
	descriptors []int
	flags       uint32
}

func decodeSpawn(call *dbus.Message, fds []int) (spawnRequest, error) {
	if len(call.Body) != 6 {
		return spawnRequest{}, fmt.Errorf("Spawn expects six arguments, got %d (%s)", len(call.Body), dbus.SignatureOf(call.Body...))
	}
	directory, _ := call.Body[0].([]byte)
	arguments, _ := call.Body[1].([][]byte)
	fdMap, _ := call.Body[2].(map[uint32]dbus.UnixFDIndex)
	environment, _ := call.Body[3].(map[string]string)
	flags, _ := call.Body[4].(uint32)
	if len(arguments) == 0 || len(arguments) > maxSpawnArguments {
		return spawnRequest{}, errors.New("Spawn needs a command")
	}
	spawn := spawnRequest{flags: flags}
	for _, argument := range arguments {
		spawn.request.Args = append(spawn.request.Args, string(bytes.TrimSuffix(argument, []byte{0})))
	}
	spawn.request.Directory = string(bytes.TrimSuffix(directory, []byte{0}))
	if spawn.request.Directory != "" && !strings.HasPrefix(spawn.request.Directory, "/") {
		return spawnRequest{}, errors.New("Spawn needs an absolute directory")
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.HasPrefix(name, "LD_") {
			continue
		}
		spawn.request.Env = append(spawn.request.Env, name+"="+environment[name])
	}
	targets := make([]int, 0, len(fdMap))
	for target := range fdMap {
		targets = append(targets, int(target))
	}
	sort.Ints(targets)
	if len(targets) > runtimeproto.MaxDescriptors {
		return spawnRequest{}, errors.New("Spawn passes too many descriptors")
	}
	for _, target := range targets {
		index := int(fdMap[uint32(target)])
		if index < 0 || index >= len(fds) {
			return spawnRequest{}, errors.New("Spawn names a descriptor it did not send")
		}
		spawn.request.Descriptors = append(spawn.request.Descriptors, target)
		spawn.descriptors = append(spawn.descriptors, fds[index])
	}
	return spawn, nil
}

func (b *portalBus) spawn(client *serializedConn, connection *portalConnection, call *dbus.Message, fds []int) {
	if b.proxy.options.ExecSocketPath == "" {
		b.fail(client, connection, call, portalUniqueName, errorNotAllowed, "starting processes is not available in this cpak")
		return
	}
	spawn, err := decodeSpawn(call, fds)
	if err != nil {
		b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, err.Error())
		return
	}
	runtime, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: b.proxy.options.ExecSocketPath, Net: "unix"})
	if err == nil {
		err = runtimeproto.WriteRequest(runtime, spawn.request, spawn.descriptors)
		if err != nil {
			runtime.Close()
		}
	}
	if err != nil {
		b.fail(client, connection, call, portalUniqueName, "org.freedesktop.DBus.Error.Failed", "the process could not be started")
		return
	}
	pid := b.nextPID.Add(1) + 1000
	connection.mu.Lock()
	if connection.spawned == nil {
		connection.spawned = map[uint32]*spawned{}
	}
	connection.spawned[pid] = &spawned{connection: runtime, watchBus: spawn.flags&spawnFlagWatchBus != 0}
	destination := connection.unique
	connection.mu.Unlock()
	b.reply(client, connection, call, portalUniqueName, pid)
	if spawn.flags&spawnFlagNotifyStart != 0 {
		b.flatpakSignal(client, destination, "SpawnStarted", pid, pid)
	}
	go func() {
		status := uint32(syscall.SIGKILL)
		for {
			kind, payload, err := runtimeproto.Read(runtime)
			if err != nil {
				break
			}
			if kind != runtimeproto.FrameExit {
				continue
			}
			if code, err := runtimeproto.DecodeExit(payload); err == nil && code >= 0 && code < 256 {
				// The portal reports a wait status, in which an exit code
				// sits in the second byte.
				status = uint32(code) << 8
			}
			break
		}
		runtime.Close()
		connection.mu.Lock()
		delete(connection.spawned, pid)
		connection.mu.Unlock()
		b.flatpakSignal(client, destination, "SpawnExited", pid, status)
	}()
}

func (b *portalBus) flatpakSignal(client *serializedConn, destination, member string, values ...uint32) {
	body := make([]any, 0, len(values))
	for _, value := range values {
		body = append(body, value)
	}
	_ = client.writeSyntheticMessage(&dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:        dbus.MakeVariant(flatpakPortalPath),
			dbus.FieldInterface:   dbus.MakeVariant(flatpakPortalInterface),
			dbus.FieldMember:      dbus.MakeVariant(member),
			dbus.FieldSender:      dbus.MakeVariant(portalUniqueName),
			dbus.FieldDestination: dbus.MakeVariant(destination),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
		},
		Body: body,
	})
}

// endSpawned stops the processes a departing caller asked to be tied to its
// bus connection, as Flatpak does for --watch-bus.
func (c *portalConnection) endSpawned() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, process := range c.spawned {
		if process.watchBus {
			_ = process.connection.Close()
		}
	}
}
