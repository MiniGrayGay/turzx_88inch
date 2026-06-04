package main

import (
	"net"
	"strconv"
	"testing"
)

func TestListenTCPAutoDecrementUsesLowerPortWhenRequestedPortIsBusy(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	startPort := occupied.Addr().(*net.TCPAddr).Port
	if startPort <= 1 {
		t.Skip("random listener selected a port that cannot be decremented")
	}

	listener, endpoint, err := listenTCPAutoDecrement(net.JoinHostPort("127.0.0.1", strconv.Itoa(startPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	_, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	selectedPort, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if selectedPort >= startPort {
		t.Fatalf("selected port = %d, want lower than busy port %d", selectedPort, startPort)
	}
}
