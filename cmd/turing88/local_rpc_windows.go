//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
)

func startLocalRPC(service *screenService, pipeName, unixSocketPath string) (string, error) {
	pipeName = strings.TrimSpace(pipeName)
	if pipeName == "" {
		return "", nil
	}

	path := namedPipePath(pipeName)
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		InputBufferSize:  4 * 1024 * 1024,
		OutputBufferSize: 1024 * 1024,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "named pipe %s not registered: %v\n", path, err)
		return "", nil
	}

	go acceptLocalRPC(service, listener, path)
	return path, nil
}

func acceptLocalRPC(service *screenService, listener net.Listener, path string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "named pipe %s stopped: %v\n", path, err)
			return
		}
		go handleJSONRPCConn(service, conn)
	}
}

func namedPipePath(pipeName string) string {
	if strings.HasPrefix(pipeName, `\\.\pipe\`) {
		return pipeName
	}
	return `\\.\pipe\` + pipeName
}
