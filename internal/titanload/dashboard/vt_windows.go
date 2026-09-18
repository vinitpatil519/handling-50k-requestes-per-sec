//go:build windows

package dashboard

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on ANSI escape processing for legacy Windows consoles.
// Windows Terminal already has it enabled; this is a no-op there.
func enableVT() {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
