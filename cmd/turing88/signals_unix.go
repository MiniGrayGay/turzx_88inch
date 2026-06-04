//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals lists the signals that trigger a graceful shutdown on Linux and other
// non-Windows platforms. SIGINT is Ctrl-C, SIGQUIT is Ctrl-\, SIGTSTP is Ctrl-Z (trapped
// so it exits cleanly instead of suspending). SIGTERM and SIGHUP cover kill / terminal
// hang-up. Ctrl-D (EOF on the terminal) is handled separately via stdin.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTSTP}
}
