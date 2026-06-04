//go:build windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals lists the signals that trigger a graceful shutdown on Windows.
// os.Interrupt covers Ctrl-C and Ctrl-Break. The Go runtime delivers SIGTERM for the
// console window close button (X), as well as logoff and shutdown events.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
