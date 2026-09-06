package cpak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	storage "github.com/containerpak/storage/pkg/driver"
	storageindex "github.com/containerpak/storage/pkg/index"
)

const storageDriverTimeout = 30 * time.Minute

func (c *Cpak) storageDriverName() (string, error) {
	name := strings.ToLower(strings.TrimSpace(c.Options.StorageDriver))
	if value := strings.TrimSpace(os.Getenv("CPAK_STORAGE_DRIVER")); value != "" {
		name = strings.ToLower(value)
	}
	if name == "" {
		name = "fvs"
	}
	if err := storage.ValidateLayerID(name); err != nil {
		return "", fmt.Errorf("unsupported storage driver %q", name)
	}
	return name, nil
}

func (c *Cpak) storageDriverRoot(name string) string {
	return c.GetInStoreDir("storage", "drivers", name)
}

func (c *Cpak) storageDriverIndex(name string) string {
	return filepath.Join(c.storageDriverRoot(name), "index.json")
}

func (c *Cpak) preparedLayerDirectories(layers []string) ([]string, error) {
	name, err := c.storageDriverName()
	if err != nil {
		return nil, err
	}
	index, err := storageindex.Load(c.storageDriverIndex(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errStoragePreparationRequired
		}
		return nil, err
	}
	if index.Driver != name {
		return nil, errStoragePreparationRequired
	}
	paths, err := index.Resolve(c.storageDriverRoot(name), layers)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errStoragePreparationRequired, err)
	}
	// The index is a user writable map of layer to directory and the
	// directories it names are user writable too, so what it resolved to is
	// measured before it is handed out to be mounted.
	if err := c.recordPreparedCheckouts(layers, paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func (c *Cpak) prepareStorageDriver(layers []string) ([]string, error) {
	if err := c.ensureFVSLayers(layers); err != nil {
		return nil, err
	}
	name, err := c.storageDriverName()
	if err != nil {
		return nil, err
	}
	request := storage.PrepareRequest{
		Layers: layers, ClearPrivilegedBits: true, OverlayWhiteouts: true, PruneLooseBlocks: true,
	}
	var prepared storage.PrepareResult
	err = c.withStorageDriver(name, func(handler storage.Handler) error {
		var prepareErr error
		prepared, prepareErr = handler.Prepare(c.Ctx, request)
		return prepareErr
	})
	if err != nil {
		return nil, err
	}
	index, err := storageindex.Load(c.storageDriverIndex(name))
	if errors.Is(err, os.ErrNotExist) {
		index = storageindex.New(name)
	} else if err != nil {
		return nil, err
	}
	if index.Driver != name {
		return nil, errors.New("storage driver index belongs to another driver")
	}
	if err := index.Set(c.storageDriverRoot(name), layers, prepared); err != nil {
		return nil, err
	}
	if err := storageindex.Write(c.storageDriverIndex(name), index); err != nil {
		return nil, err
	}
	paths, err := index.Resolve(c.storageDriverRoot(name), layers)
	if err != nil {
		return nil, err
	}
	if err := c.recordPreparedCheckouts(layers, paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func (c *Cpak) withStorageDriver(name string, run func(storage.Handler) error) error {
	if c.storageDriver != nil {
		return run(c.storageDriver)
	}
	unlock, err := c.lockFVSManager()
	if err != nil {
		return err
	}
	defer unlock()
	client, err := c.ensureStorageDriver(name)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Shutdown(ctx)
	}()
	return run(client)
}

func (c *Cpak) ensureStorageDriver(name string) (*storage.Client, error) {
	socket, err := c.storageDriverSocket(name)
	if err != nil {
		return nil, err
	}
	client := &storage.Client{SocketPath: socket, Timeout: storageDriverTimeout}
	ctx, cancel := context.WithTimeout(c.Ctx, 250*time.Millisecond)
	info, probeErr := client.Probe(ctx)
	cancel()
	if probeErr == nil {
		if info.Name != name || info.Protocol != storage.ProtocolVersion {
			return nil, errors.New("storage driver identity does not match its configuration")
		}
		return client, nil
	}
	_ = os.Remove(socket)
	binary, external, err := findStorageDriverService()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.storageDriverRoot(name), 0o700); err != nil {
		return nil, err
	}
	logPath := filepath.Join(filepath.Dir(socket), "driver.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	arguments := []string{
		"--socket", socket,
		"--source-root", c.fvsRoot(),
		"--driver-root", c.storageDriverRoot(name),
		"--driver", name,
	}
	command, err := c.storageDriverCommand(name, binary, external, socket, arguments)
	if err != nil {
		logFile.Close()
		return nil, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	go func() { _ = command.Wait() }()
	_ = logFile.Close()
	deadline := time.Now().Add(fvsManagerTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(c.Ctx, 250*time.Millisecond)
		info, err = client.Probe(ctx)
		cancel()
		if err == nil {
			if info.Name != name || info.Protocol != storage.ProtocolVersion {
				return nil, errors.New("storage driver identity does not match its configuration")
			}
			return client, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, fmt.Errorf("start storage driver: %w", err)
}

func (c *Cpak) storageDriverSocket(name string) (string, error) {
	directory, err := sharedSystemBrokerRuntimeDirectory()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(c.Options.StorePath + "\x00" + name))
	directory = filepath.Join(directory, "storage-"+hex.EncodeToString(digest[:8]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(directory, "driver.sock"), nil
}

func (c *Cpak) storageDriverCommand(name, binary string, external bool, socket string, arguments []string) (*exec.Cmd, error) {
	if !external || os.Getenv("CPAK_STORAGE_DRIVER_TRUSTED") == "1" {
		return exec.Command(binary, arguments...), nil
	}
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return nil, err
	}
	launch := []string{"launch", "--require-sandbox"}
	readOnly := []string{binary}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64", "/etc", "/dev/null", "/dev/urandom"} {
		if _, err := os.Stat(path); err == nil {
			readOnly = append(readOnly, path)
		}
	}
	for _, path := range readOnly {
		launch = append(launch, "--landlock-read-only", path)
	}
	for _, path := range []string{c.fvsRoot(), c.storageDriverRoot(name), filepath.Dir(socket)} {
		launch = append(launch, "--landlock-read-write", path)
	}
	launch = append(launch, "--", binary)
	launch = append(launch, arguments...)
	return nativeNamespaceCommand(cpakBinary, launch, namespaceOptions{IsolateNetwork: true}), nil
}

func findStorageDriverService() (string, bool, error) {
	if configured := os.Getenv("CPAK_STORAGE_DRIVER_BINARY"); configured != "" {
		configured, err := filepath.Abs(configured)
		if err != nil {
			return "", false, err
		}
		if info, err := os.Stat(configured); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return configured, true, nil
		}
		return "", false, errStorageServiceMissing
	}
	if cpakBinary, err := getCpakBinary(); err == nil {
		candidate := filepath.Join(filepath.Dir(cpakBinary), "cpak-storaged")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, false, nil
		}
	}
	if binary, err := exec.LookPath("cpak-storaged"); err == nil {
		return binary, true, nil
	}
	return "", false, errStorageServiceMissing
}
