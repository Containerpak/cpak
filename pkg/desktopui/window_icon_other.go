//go:build !linux

package desktopui

type desktopWindows struct{}

func captureDesktopWindows() *desktopWindows {
	return nil
}

func (d *desktopWindows) Close() {}

func (d *desktopWindows) Apply(string, []byte) {}
