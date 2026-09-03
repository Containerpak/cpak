/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type testSyncBuffer struct {
	mutex sync.Mutex
	data  bytes.Buffer
}

func (b *testSyncBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.data.Write(data)
}

func (b *testSyncBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.data.String()
}

func TestX11BrokerIntegratesXephyrWithTheHostDesktop(t *testing.T) {
	if os.Getenv("DISPLAY") == "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("not an X11 session")
	}
	if _, err := exec.LookPath("Xephyr"); err != nil {
		t.Skip("Xephyr is not installed")
	}
	state := t.TempDir()
	container, err := startX11Bridge(types.Container{CpakId: "xephyr-broker-test", StatePath: state, LogPath: filepath.Join(state, "x11.log")}, types.ClipboardGrant{HostToApp: true, AppToHost: true})
	if err != nil {
		t.Fatal(err)
	}
	if container.X11HostWindowName == "" {
		t.Fatal("Xephyr has no stable host window identity")
	}
	process := exec.Command("sleep", "30")
	process.Env = append(os.Environ(), "CPAK_CONTAINER_ID="+container.CpakId)
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	container.Pid = process.Process.Pid
	container.ProcessStartTime, err = processStartTime(container.Pid)
	if err != nil {
		t.Fatal(err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readyFD := duplicateTestFileDescriptor(t, readyWriter)
	done := make(chan error, 1)
	go func() {
		done <- RunX11Broker(X11BrokerOptions{
			NestedDisplay: x11BrokerDisplay(container), NestedAuthority: container.X11AuthorityPath,
			HostDisplay: os.Getenv("DISPLAY"), HostWindow: container.X11HostWindowName,
			ServerPid: container.X11BridgePid, ServerStartTime: container.X11BridgeStartTime,
			ContainerPid: container.Pid, ContainerStartTime: container.ProcessStartTime,
			ContainerID: container.CpakId, ReadyFD: readyFD, HostToApp: true, AppToHost: true,
		})
	}()
	ready := []byte{0}
	if _, err = io.ReadFull(readyReader, ready); err != nil {
		select {
		case brokerErr := <-done:
			t.Fatalf("X11 broker readiness: %v, broker: %v", err, brokerErr)
		case <-time.After(time.Second):
			t.Fatalf("X11 broker readiness: %v", err)
		}
	}
	if ready[0] != 1 {
		t.Fatalf("X11 broker readiness response: %v", ready)
	}
	readyReader.Close()
	defer func() {
		cleanupX11Bridge(container)
		if process.Process != nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("X11 broker did not stop")
		}
	}()

	host := connectHostX11(t)
	defer host.Close()
	outer := findTestClassWindow(t, host, container.X11HostWindowName)
	if outer == xproto.WindowNone {
		t.Fatal("Xephyr host window was not found by its private identity")
	}
	nested := connectTestX11(t, container)
	defer nested.Close()
	hostSource := connectHostX11(t)
	defer hostSource.Close()
	hostOwner := publishTestSelection(t, hostSource, "host clipboard through Xephyr")
	if got := readTestSelection(t, nested, xproto.WindowNone, "host clipboard through Xephyr"); got != "host clipboard through Xephyr" {
		t.Fatalf("host to application clipboard: %q", got)
	}
	nestedSource := connectTestX11(t, container)
	defer nestedSource.Close()
	publishTestSelection(t, nestedSource, "application clipboard through Xephyr")
	if got := readTestSelection(t, host, hostOwner, "application clipboard through Xephyr"); got != "application clipboard through Xephyr" {
		t.Fatalf("application to host clipboard: %q", got)
	}
	browserValues := []string{"first browser clipboard through Xephyr", "second browser clipboard through Xephyr"}
	copyFromBrowser, stopBrowser := startTestBrowserClipboard(t, container, browserValues)
	for _, expected := range browserValues {
		copyFromBrowser()
		waitTestClipboardValue(t, host, expected, "host")
	}
	t.Log("repeated browser clipboard passed")

	screen := xproto.Setup(nested).DefaultScreen(nested)
	window, err := xproto.NewWindowId(nested)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(nested, screen.RootDepth, window, screen.Root, 0, 0, 320, 240, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	name := testAtom(t, nested, "_NET_WM_NAME")
	utf8 := testAtom(t, nested, "UTF8_STRING")
	title := []byte("Xephyr browser")
	xproto.ChangeProperty(nested, xproto.PropModeReplace, window, name, utf8, 8, uint32(len(title)), title)
	xproto.MapWindow(nested, window)
	nested.Sync()
	stopBrowser()
	waitTestCondition(t, 3*time.Second, func() bool {
		return readTestProperty(t, host, outer, "_NET_WM_NAME") == string(title)
	}, "Xephyr title was not synchronized")

	_ = xproto.ConfigureWindowChecked(host, outer, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{1024, 700}).Check()
	host.Sync()
	waitTestCondition(t, 3*time.Second, func() bool {
		root, rootErr := xproto.GetGeometry(nested, xproto.Drawable(screen.Root)).Reply()
		geometry, geometryErr := xproto.GetGeometry(nested, xproto.Drawable(window)).Reply()
		return rootErr == nil && geometryErr == nil && root.Width == 1024 && root.Height == 700 && geometry.Width == root.Width && geometry.Height == root.Height
	}, "Xephyr resize did not reach the application")

	transient, err := xproto.NewWindowId(nested)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(nested, screen.RootDepth, transient, screen.Root, 80, 90, 240, 160, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	transientFor := make([]byte, 4)
	xgb.Put32(transientFor, uint32(window))
	xproto.ChangeProperty(nested, xproto.PropModeReplace, transient, xproto.AtomWmTransientFor, xproto.AtomWindow, 32, 1, transientFor)
	xproto.MapWindow(nested, transient)
	nested.Sync()
	time.Sleep(300 * time.Millisecond)
	geometry, err := xproto.GetGeometry(nested, xproto.Drawable(transient)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if geometry.X != 80 || geometry.Y != 90 || geometry.Width != 240 || geometry.Height != 160 {
		t.Fatalf("browser transient geometry: %d,%d %dx%d", geometry.X, geometry.Y, geometry.Width, geometry.Height)
	}

	popup, err := xproto.NewWindowId(nested)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(
		nested, screen.RootDepth, popup, screen.Root, 120, 130, 180, 100, 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwOverrideRedirect, []uint32{1},
	).Check(); err != nil {
		t.Fatal(err)
	}
	xproto.MapWindow(nested, popup)
	nested.Sync()
	time.Sleep(300 * time.Millisecond)
	geometry, err = xproto.GetGeometry(nested, xproto.Drawable(popup)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if geometry.X != 120 || geometry.Y != 130 || geometry.Width != 180 || geometry.Height != 100 {
		t.Fatalf("browser popup geometry: %d,%d %dx%d", geometry.X, geometry.Y, geometry.Width, geometry.Height)
	}
	t.Log("browser transient and popup geometry passed")

	stateAtom := testAtom(t, nested, "_NET_WM_STATE")
	fullscreen := testAtom(t, nested, "_NET_WM_STATE_FULLSCREEN")
	request := xproto.ClientMessageEvent{
		Format: 32, Window: window, Type: stateAtom,
		Data: xproto.ClientMessageDataUnionData32New([]uint32{1, uint32(fullscreen), 0, 1, 0}),
	}
	xproto.SendEvent(nested, false, screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(request.Bytes()))
	nested.Sync()
	waitTestCondition(t, 3*time.Second, func() bool {
		states := readTestAtoms(host, outer, "_NET_WM_STATE")
		return containsTestAtom(states, testAtom(t, host, "_NET_WM_STATE_FULLSCREEN"))
	}, "fullscreen did not reach the host window manager")
	xproto.DestroyWindow(nested, popup)
	xproto.DestroyWindow(nested, transient)
	xproto.DestroyWindow(nested, window)
	nested.Sync()
	waitTestCondition(t, 4*time.Second, func() bool {
		return !sameContainerProcess(container, container.Pid) && !sameRecordedProcess(container.X11BridgePid, container.X11BridgeStartTime)
	}, "closing the last window did not stop the cpak instance")
}

func connectHostX11(t *testing.T) *xgb.Conn {
	t.Helper()
	connection, err := xgb.NewConnDisplay(os.Getenv("DISPLAY"))
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func startTestBrowserClipboard(t *testing.T, container types.Container, values []string) (func(), func()) {
	t.Helper()
	browser := os.Getenv("CPAK_X11_BROWSER")
	if browser == "" {
		t.Fatal("CPAK_X11_BROWSER is not set")
	}
	firefox := strings.Contains(filepath.Base(browser), "firefox")
	title := "cpak browser clipboard probe"
	activeTitle := title + " active "
	root := t.TempDir()
	pagePath := filepath.Join(root, "clipboard.html")
	quotedValues := make([]string, len(values))
	for index, value := range values {
		quotedValues[index] = strconv.Quote(value)
	}
	script := "let index=0,clicks=0;const field=document.getElementById('value');const selectNext=()=>{field.value=values[index];field.focus();field.select()};field.addEventListener('mousedown',()=>{clicks++;document.title=" + strconv.Quote(activeTitle) + "+clicks});field.addEventListener('copy',()=>{index++;if(index<values.length)setTimeout(selectNext,0)});selectNext();document.title=" + strconv.Quote(title)
	pageContents := "<title>cpak browser starting</title><style>html,body,#value{box-sizing:border-box;margin:0;width:100%;height:100%}</style><textarea id=value></textarea><script>const values=[" + strings.Join(quotedValues, ",") + "];" + script + "</script>"
	if err := os.WriteFile(pagePath, []byte(pageContents), 0600); err != nil {
		t.Fatal(err)
	}
	page := "file://" + pagePath
	if firefox {
		server := httptest.NewServer(http.FileServer(http.Dir(root)))
		t.Cleanup(server.Close)
		return startTestFirefoxClipboard(t, browser, container, server.URL+"/clipboard.html", values, activeTitle)
	}
	xdotool, err := exec.LookPath("xdotool")
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "profile")
	command := exec.Command(
		browser,
		"--user-data-dir="+profile,
		"--no-first-run",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--ozone-platform=x11",
		"--app="+page,
	)
	command.Env = append(os.Environ(),
		"DISPLAY="+container.X11Display,
		"XAUTHORITY="+container.X11AuthorityPath,
		"WAYLAND_DISPLAY=",
		"MOZ_ENABLE_WAYLAND=0",
		"DBUS_SESSION_BUS_ADDRESS=",
		"DBUS_SYSTEM_BUS_ADDRESS=",
	)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var browserOutput testSyncBuffer
	command.Stdout = &browserOutput
	command.Stderr = &browserOutput
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if command.Process != nil {
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				_, _ = command.Process.Wait()
			}
		})
	}
	t.Cleanup(stop)
	var window string
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		find := exec.Command(xdotool, "search", "--onlyvisible", "--name", title)
		find.Env = command.Env
		output, findErr := find.Output()
		if findErr == nil {
			fields := strings.Fields(string(output))
			if len(fields) > 0 {
				window = fields[0]
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if window == "" {
		stop()
		t.Fatalf("browser clipboard window did not open: %s", strings.TrimSpace(browserOutput.String()))
	}
	clicks := 0
	copyFromBrowser := func() {
		t.Helper()
		clicks++
		expectedTitle := activeTitle + strconv.Itoa(clicks)
		click := exec.Command(
			xdotool,
			"windowfocus", "--sync", window,
			"sleep", "0.2",
			"mousemove", "--window", window, "400", "400",
			"click", "1",
		)
		click.Env = command.Env
		if output, clickErr := click.CombinedOutput(); clickErr != nil {
			t.Fatalf("focus browser content: %v: %s", clickErr, output)
		}
		waitTestBrowserTitle(t, xdotool, window, expectedTitle, command.Env, 3*time.Second)
		copy := exec.Command(xdotool, "key", "--clearmodifiers", "ctrl+a", "ctrl+c", "sleep", "0.2")
		copy.Env = command.Env
		if output, copyErr := copy.CombinedOutput(); copyErr != nil {
			t.Fatalf("copy from browser: %v: %s", copyErr, output)
		}
	}
	return copyFromBrowser, stop
}

type testWebDriverResponse struct {
	Value json.RawMessage `json:"value"`
}

func startTestFirefoxClipboard(t *testing.T, browser string, container types.Container, page string, values []string, activeTitle string) (func(), func()) {
	t.Helper()
	driver := os.Getenv("CPAK_X11_DRIVER")
	if driver == "" {
		var err error
		driver, err = exec.LookPath("geckodriver")
		if err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(driver, "--host", "127.0.0.1", "--port", strings.TrimPrefix(address, "127.0.0.1:"))
	command.Env = append(os.Environ(),
		"DISPLAY="+container.X11Display,
		"XAUTHORITY="+container.X11AuthorityPath,
		"WAYLAND_DISPLAY=",
		"MOZ_ENABLE_WAYLAND=0",
		"DBUS_SESSION_BUS_ADDRESS=",
		"DBUS_SYSTEM_BUS_ADDRESS=",
	)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var driverOutput testSyncBuffer
	command.Stdout = &driverOutput
	command.Stderr = &driverOutput
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	var session string
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if session != "" {
				request, requestErr := http.NewRequest(http.MethodDelete, "http://"+address+"/session/"+session, nil)
				if requestErr == nil {
					if response, responseErr := http.DefaultClient.Do(request); responseErr == nil {
						response.Body.Close()
					}
				}
			}
			if command.Process != nil {
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				_, _ = command.Process.Wait()
			}
		})
	}
	t.Cleanup(stop)
	statusURL := "http://" + address + "/status"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, statusErr := http.Get(statusURL)
		if statusErr == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	capabilities := map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"browserName": "firefox",
				"moz:firefoxOptions": map[string]any{
					"binary": browser,
					"args":   []string{"--kiosk"},
				},
			},
		},
	}
	var sessionValue struct {
		SessionID string `json:"sessionId"`
	}
	testWebDriverRequest(t, http.MethodPost, "http://"+address+"/session", capabilities, &sessionValue)
	session = sessionValue.SessionID
	if session == "" {
		t.Fatalf("geckodriver did not create a session: %s", strings.TrimSpace(driverOutput.String()))
	}
	base := "http://" + address + "/session/" + session
	testWebDriverRequest(t, http.MethodPost, base+"/url", map[string]string{"url": page}, nil)
	var element struct {
		ID string `json:"element-6066-11e4-a52e-4f735466cecf"`
	}
	testWebDriverRequest(t, http.MethodPost, base+"/element", map[string]string{"using": "css selector", "value": "#value"}, &element)
	if element.ID == "" {
		t.Fatalf("geckodriver did not find the clipboard field: %s", strings.TrimSpace(driverOutput.String()))
	}
	clicks := 0
	copyFromBrowser := func() {
		t.Helper()
		expected := values[clicks]
		clicks++
		expectedTitle := activeTitle + strconv.Itoa(clicks)
		testWebDriverRequest(t, http.MethodPost, base+"/execute/sync", map[string]any{
			"script": "const field = document.getElementById('value'); field.value = arguments[0]; field.focus(); field.select(); document.addEventListener('copy', event => { event.clipboardData.setData('text/plain', arguments[0]); event.preventDefault(); document.title = arguments[1] }, { once: true })",
			"args":   []string{expected, expectedTitle},
		}, nil)
		testWebDriverRequest(t, http.MethodPost, base+"/element/"+element.ID+"/click", map[string]any{}, nil)
		testWebDriverRequest(t, http.MethodPost, base+"/actions", map[string]any{
			"actions": []any{map[string]any{
				"type": "key",
				"id":   "keyboard",
				"actions": []any{
					map[string]string{"type": "keyDown", "value": "\ue009"},
					map[string]string{"type": "keyDown", "value": "a"},
					map[string]string{"type": "keyUp", "value": "a"},
					map[string]string{"type": "keyUp", "value": "\ue009"},
					map[string]string{"type": "keyDown", "value": "\ue009"},
					map[string]string{"type": "keyDown", "value": "c"},
					map[string]string{"type": "keyUp", "value": "c"},
					map[string]string{"type": "keyUp", "value": "\ue009"},
				},
			}},
		}, nil)
		var actualTitle string
		testWebDriverRequest(t, http.MethodPost, base+"/execute/sync", map[string]any{"script": "return document.title", "args": []string{}}, &actualTitle)
		if actualTitle != expectedTitle {
			t.Fatalf("Firefox did not emit the browser copy event: %q", actualTitle)
		}
	}
	return copyFromBrowser, stop
}

func testWebDriverRequest(t *testing.T, method, url string, payload any, result any) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("webdriver %s %s: %s", method, url, strings.TrimSpace(string(contents)))
	}
	if result == nil {
		return
	}
	var envelope testWebDriverResponse
	if err = json.Unmarshal(contents, &envelope); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(envelope.Value, result); err != nil {
		t.Fatal(err)
	}
}

func waitTestBrowserTitle(t *testing.T, xdotool, window, expected string, environment []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		name := exec.Command(xdotool, "getwindowname", window)
		name.Env = environment
		output, err := name.Output()
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), expected) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	name := exec.Command(xdotool, "getwindowname", window)
	name.Env = environment
	output, err := name.Output()
	t.Fatalf("browser clipboard action did not complete: %v: %s", err, strings.TrimSpace(string(output)))
}

func waitTestClipboardValue(t *testing.T, connection *xgb.Conn, expected, source string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	got := ""
	for time.Now().Before(deadline) {
		value, ok := tryReadTestSelection(t, connection, xproto.WindowNone, 500*time.Millisecond)
		if ok {
			got = value
		}
		if ok && got == expected {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s clipboard: got %q, expected %q", source, got, expected)
}

func findTestClassWindow(t *testing.T, connection *xgb.Conn, class string) xproto.Window {
	t.Helper()
	root := xproto.Setup(connection).DefaultScreen(connection).Root
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		queue := []xproto.Window{root}
		for len(queue) > 0 {
			window := queue[0]
			queue = queue[1:]
			property, err := xproto.GetProperty(connection, false, window, xproto.AtomWmClass, xproto.AtomString, 0, 1024).Reply()
			if err == nil {
				for _, value := range splitTestClass(property.Value) {
					if value == class {
						return window
					}
				}
			}
			tree, err := xproto.QueryTree(connection, window).Reply()
			if err == nil {
				queue = append(queue, tree.Children...)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return xproto.WindowNone
}

func splitTestClass(value []byte) []string {
	result := []string{}
	start := 0
	for index, item := range value {
		if item == 0 {
			result = append(result, string(value[start:index]))
			start = index + 1
		}
	}
	if start < len(value) {
		result = append(result, string(value[start:]))
	}
	return result
}

func readTestAtoms(connection *xgb.Conn, window xproto.Window, name string) []xproto.Atom {
	atom, err := internTestAtom(connection, name)
	if err != nil {
		return nil
	}
	reply, err := xproto.GetProperty(connection, false, window, atom, xproto.AtomAtom, 0, 1024).Reply()
	if err != nil || reply.Format != 32 {
		return nil
	}
	result := make([]xproto.Atom, len(reply.Value)/4)
	for index := range result {
		result[index] = xproto.Atom(xgb.Get32(reply.Value[index*4:]))
	}
	return result
}

func containsTestAtom(atoms []xproto.Atom, wanted xproto.Atom) bool {
	for _, atom := range atoms {
		if atom == wanted {
			return true
		}
	}
	return false
}

func waitTestCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(message)
}
