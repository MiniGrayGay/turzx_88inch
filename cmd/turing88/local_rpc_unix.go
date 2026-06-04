//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func startLocalRPC(service *screenService, pipeName, unixSocketPath string) (string, error) {
	unixSocketPath = strings.TrimSpace(unixSocketPath)
	if unixSocketPath == "" {
		return "", nil
	}
	if strings.HasPrefix(unixSocketPath, "unix://") {
		unixSocketPath = strings.TrimPrefix(unixSocketPath, "unix://")
	}

	ready, err := prepareUnixSocketPath(unixSocketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unix socket %s not registered: %v\n", unixSocketPath, err)
		return "", nil
	}
	if !ready {
		return "", nil
	}

	listener, err := net.Listen("unix", unixSocketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unix socket %s not registered: %v\n", unixSocketPath, err)
		return "", nil
	}

	go acceptLocalRPC(service, listener, unixSocketPath)
	return "unix://" + unixSocketPath, nil
}

func prepareUnixSocketPath(path string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}

	info, err := os.Stat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return false, fmt.Errorf("%s exists and is not a unix socket", path)
		}

		conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return false, fmt.Errorf("%s is already in use", path)
		}
		return true, os.Remove(path)
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func acceptLocalRPC(service *screenService, listener net.Listener, path string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "unix socket %s stopped: %v\n", path, err)
			return
		}
		go handleJSONRPCConn(service, conn)
	}
}
