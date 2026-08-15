/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const maxMessageSize = 16 << 20

type messagePacket struct {
	message *dbus.Message
	data    []byte
	fds     []int
}

func (p messagePacket) closeFDs() {
	for _, fd := range p.fds {
		_ = unix.Close(fd)
	}
}

type serializedConn struct {
	*net.UnixConn
	writeMu sync.Mutex
	serial  atomic.Uint32
}

func (c *serializedConn) Write(data []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.UnixConn.Write(data)
}

func (c *serializedConn) writeMessage(message *dbus.Message) error {
	return c.writeMessageWithSerial(message, 0)
}

func (c *serializedConn) writeSyntheticMessage(message *dbus.Message) error {
	serial := c.serial.Add(1)
	if serial == 0 {
		serial = c.serial.Add(1)
	}
	return c.writeMessageWithSerial(message, serial)
}

func (c *serializedConn) writeMessageWithSerial(message *dbus.Message, serial uint32) error {
	var encoded bytes.Buffer
	fds, err := message.EncodeToWithFDs(&encoded, binary.LittleEndian)
	if err != nil {
		return err
	}
	data := encoded.Bytes()
	if serial != 0 {
		binary.LittleEndian.PutUint32(data[8:12], serial)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeWireMessage(c.UnixConn, data, fds)
}

func (c *serializedConn) writePacket(packet messagePacket) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeWireMessage(c.UnixConn, packet.data, packet.fds)
}

func writeWireMessage(connection *net.UnixConn, data []byte, fds []int) error {
	if len(fds) == 0 {
		return writeAll(connection, data)
	}
	oob := unix.UnixRights(fds...)
	n, oobn, err := connection.WriteMsgUnix(data, oob, nil)
	if err != nil {
		return err
	}
	if oobn != len(oob) {
		return io.ErrShortWrite
	}
	return writeAll(connection, data[n:])
}

func readMessage(connection *net.UnixConn) (messagePacket, error) {
	header := make([]byte, 16)
	fds, err := readMessageBytes(connection, header)
	if err != nil {
		return messagePacket{}, err
	}
	var order binary.ByteOrder
	switch header[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		closeFDs(fds)
		return messagePacket{}, errors.New("desktop bus message has invalid byte order")
	}
	bodyLength := order.Uint32(header[4:8])
	headerLength := order.Uint32(header[12:16])
	alignedHeaderLength := (headerLength + 7) &^ 7
	remaining := uint64(alignedHeaderLength) + uint64(bodyLength)
	if remaining > maxMessageSize-16 {
		closeFDs(fds)
		return messagePacket{}, errors.New("desktop bus message is too large")
	}
	data := make([]byte, 16+int(remaining))
	copy(data, header)
	moreFDs, err := readMessageBytes(connection, data[16:])
	fds = append(fds, moreFDs...)
	if err != nil {
		closeFDs(fds)
		return messagePacket{}, err
	}
	message, err := dbus.DecodeMessageWithFDs(bytes.NewReader(data), fds)
	if err != nil {
		closeFDs(fds)
		return messagePacket{}, err
	}
	return messagePacket{message: message, data: data, fds: fds}, nil
}

func readMessageBytes(connection *net.UnixConn, target []byte) ([]int, error) {
	fds := []int{}
	for offset := 0; offset < len(target); {
		oob := make([]byte, unix.CmsgSpace(64*4))
		n, oobn, flags, _, err := connection.ReadMsgUnix(target[offset:], oob)
		if err != nil {
			return fds, err
		}
		if n == 0 {
			return fds, io.EOF
		}
		if flags&unix.MSG_CTRUNC != 0 {
			return fds, errors.New("desktop bus message contains too many file descriptors")
		}
		if oobn > 0 {
			messages, parseErr := syscall.ParseSocketControlMessage(oob[:oobn])
			if parseErr != nil {
				return fds, parseErr
			}
			for _, message := range messages {
				rights, rightsErr := syscall.ParseUnixRights(&message)
				if rightsErr != nil {
					return fds, rightsErr
				}
				fds = append(fds, rights...)
			}
		}
		offset += n
	}
	return fds, nil
}

func authenticateClient(connection *net.UnixConn) error {
	ucred, err := peerCredentials(connection)
	if err != nil {
		return err
	}
	if ucred.Uid != uint32(os.Getuid()) {
		return errors.New("desktop bus denied the caller")
	}
	first := []byte{0}
	if _, err = io.ReadFull(connection, first); err != nil {
		return err
	}
	if first[0] != 0 {
		return errors.New("desktop bus authentication did not start with a null byte")
	}
	guidBytes := make([]byte, 16)
	if _, err = io.ReadFull(rand.Reader, guidBytes); err != nil {
		return err
	}
	guid := hex.EncodeToString(guidBytes)
	authorized := false
	waitingForIdentity := false
	for {
		line, readErr := readAuthLine(connection)
		if readErr != nil {
			return readErr
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			return errors.New("desktop bus authentication command is empty")
		}
		switch parts[0] {
		case "AUTH":
			if len(parts) == 1 {
				if err = writeAuthLine(connection, "REJECTED EXTERNAL"); err != nil {
					return err
				}
				continue
			}
			if parts[1] != "EXTERNAL" {
				if err = writeAuthLine(connection, "REJECTED EXTERNAL"); err != nil {
					return err
				}
				continue
			}
			if len(parts) == 2 {
				authorized = false
				waitingForIdentity = true
				if err = writeAuthLine(connection, "DATA"); err != nil {
					return err
				}
				continue
			}
			if len(parts) != 3 {
				return errors.New("desktop bus authentication identity is invalid")
			}
			authorized = externalIdentityMatches(parts[2], ucred.Uid)
			if !authorized {
				if err = writeAuthLine(connection, "REJECTED EXTERNAL"); err != nil {
					return err
				}
				continue
			}
			if err = writeAuthLine(connection, "OK "+guid); err != nil {
				return err
			}
		case "DATA":
			if !waitingForIdentity || len(parts) > 2 {
				return errors.New("desktop bus authentication data is invalid")
			}
			waitingForIdentity = false
			authorized = len(parts) == 1 || externalIdentityMatches(parts[1], ucred.Uid)
			if !authorized {
				if err = writeAuthLine(connection, "REJECTED EXTERNAL"); err != nil {
					return err
				}
				continue
			}
			if err = writeAuthLine(connection, "OK "+guid); err != nil {
				return err
			}
		case "NEGOTIATE_UNIX_FD":
			if !authorized {
				return errors.New("desktop bus file descriptor negotiation precedes authentication")
			}
			if err = writeAuthLine(connection, "AGREE_UNIX_FD"); err != nil {
				return err
			}
		case "BEGIN":
			if !authorized {
				return errors.New("desktop bus session precedes authentication")
			}
			return nil
		case "CANCEL":
			authorized = false
			waitingForIdentity = false
			if err = writeAuthLine(connection, "REJECTED EXTERNAL"); err != nil {
				return err
			}
		default:
			if err = writeAuthLine(connection, "ERROR Unsupported authentication command"); err != nil {
				return err
			}
		}
	}
}

func authenticateUpstream(connection *net.UnixConn) error {
	if _, err := connection.Write([]byte{0}); err != nil {
		return err
	}
	identity := hex.EncodeToString([]byte(strconv.Itoa(os.Geteuid())))
	if err := writeAuthLine(connection, "AUTH EXTERNAL "+identity); err != nil {
		return err
	}
	line, err := readAuthLine(connection)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "OK ") {
		return fmt.Errorf("desktop bus upstream rejected authentication: %s", line)
	}
	if err = writeAuthLine(connection, "NEGOTIATE_UNIX_FD"); err != nil {
		return err
	}
	line, err = readAuthLine(connection)
	if err != nil {
		return err
	}
	if line != "AGREE_UNIX_FD" && !strings.HasPrefix(line, "ERROR") {
		return errors.New("desktop bus upstream returned an invalid file descriptor negotiation response")
	}
	return writeAuthLine(connection, "BEGIN")
}

func peerCredentials(connection *net.UnixConn) (*unix.Ucred, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credentials *unix.Ucred
	var socketErr error
	if err = raw.Control(func(fd uintptr) {
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return credentials, socketErr
}

func externalIdentityMatches(encoded string, uid uint32) bool {
	identity, err := hex.DecodeString(encoded)
	return err == nil && string(identity) == strconv.FormatUint(uint64(uid), 10)
}

func readAuthLine(reader io.Reader) (string, error) {
	buffer := make([]byte, 0, 128)
	last := byte(0)
	for len(buffer) < 4096 {
		current := []byte{0}
		if _, err := io.ReadFull(reader, current); err != nil {
			return "", err
		}
		buffer = append(buffer, current[0])
		if last == '\r' && current[0] == '\n' {
			return string(buffer[:len(buffer)-2]), nil
		}
		last = current[0]
	}
	return "", errors.New("desktop bus authentication line is too long")
}

func writeAuthLine(writer io.Writer, line string) error {
	return writeAll(writer, []byte(line+"\r\n"))
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}
