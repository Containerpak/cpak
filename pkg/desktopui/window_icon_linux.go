package desktopui

import (
	"image"
	"image/color"
	"os"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type desktopWindows struct {
	connection *xgb.Conn
	root       xproto.Window
	windows    map[xproto.Window]struct{}
}

func captureDesktopWindows() *desktopWindows {
	if os.Getenv("DISPLAY") == "" {
		return nil
	}
	connection, err := xgb.NewConn()
	if err != nil {
		return nil
	}
	setup := xproto.Setup(connection)
	if setup == nil || len(setup.Roots) == 0 {
		connection.Close()
		return nil
	}
	root := setup.DefaultScreen(connection).Root
	return &desktopWindows{connection: connection, root: root, windows: descendantWindows(connection, root)}
}

func (d *desktopWindows) Close() {
	if d != nil && d.connection != nil {
		d.connection.Close()
		d.connection = nil
	}
}

func (d *desktopWindows) Apply(title string, iconPNG []byte) {
	if d == nil || d.connection == nil {
		return
	}
	defer d.Close()
	name := internAtom(d.connection, "_NET_WM_NAME")
	icon := internAtom(d.connection, "_NET_WM_ICON")
	pid := internAtom(d.connection, "_NET_WM_PID")
	utf8 := internAtom(d.connection, "UTF8_STRING")
	if name == 0 || icon == 0 || pid == 0 || utf8 == 0 {
		return
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		for window := range descendantWindows(d.connection, d.root) {
			if _, existed := d.windows[window]; existed || windowTitle(d.connection, window, name, utf8) != title {
				continue
			}
			setWindowIdentity(d.connection, window, icon, pid, iconPNG)
			d.connection.Sync()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func descendantWindows(connection *xgb.Conn, root xproto.Window) map[xproto.Window]struct{} {
	windows := make(map[xproto.Window]struct{})
	queue := []xproto.Window{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		reply, err := xproto.QueryTree(connection, parent).Reply()
		if err != nil {
			continue
		}
		for _, child := range reply.Children {
			if _, seen := windows[child]; seen {
				continue
			}
			windows[child] = struct{}{}
			queue = append(queue, child)
		}
	}
	return windows
}

func internAtom(connection *xgb.Conn, name string) xproto.Atom {
	reply, err := xproto.InternAtom(connection, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0
	}
	return reply.Atom
}

func windowTitle(connection *xgb.Conn, window xproto.Window, name, utf8 xproto.Atom) string {
	reply, err := xproto.GetProperty(connection, false, window, name, utf8, 0, 1024).Reply()
	if err != nil || reply.Format != 8 {
		return ""
	}
	return string(reply.Value)
}

func setWindowIdentity(connection *xgb.Conn, window xproto.Window, icon, pid xproto.Atom, iconPNG []byte) {
	class := []byte("cpak\x00cpak\x00")
	_ = xproto.ChangePropertyChecked(connection, xproto.PropModeReplace, window, xproto.AtomWmClass, xproto.AtomString, 8, uint32(len(class)), class).Check()
	process := make([]byte, 4)
	xgb.Put32(process, uint32(os.Getpid()))
	_ = xproto.ChangePropertyChecked(connection, xproto.PropModeReplace, window, pid, xproto.AtomCardinal, 32, 1, process).Check()
	encoded := encodeWindowIcon(renderUpdateIcon(iconPNG, 128))
	if len(encoded) > 0 {
		_ = xproto.ChangePropertyChecked(connection, xproto.PropModeReplace, window, icon, xproto.AtomCardinal, 32, uint32(len(encoded)/4), encoded).Check()
	}
}

func encodeWindowIcon(source image.Image) []byte {
	if source == nil || source.Bounds().Dx() == 0 || source.Bounds().Dy() == 0 {
		return nil
	}
	bounds := source.Bounds()
	result := make([]byte, 8+bounds.Dx()*bounds.Dy()*4)
	xgb.Put32(result[0:4], uint32(bounds.Dx()))
	xgb.Put32(result[4:8], uint32(bounds.Dy()))
	offset := 8
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBAModel.Convert(source.At(x, y)).(color.NRGBA)
			value := uint32(pixel.A)<<24 | uint32(pixel.R)<<16 | uint32(pixel.G)<<8 | uint32(pixel.B)
			xgb.Put32(result[offset:offset+4], value)
			offset += 4
		}
	}
	return result
}
