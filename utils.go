package main

import (
	"fmt"
	"net"
)

// isPortAvailable attempts to bind to the given TCP port on loopback and
// all interfaces. If binding fails on all addresses it is considered in use.
func isPortAvailable(port int) bool {
	// try loopback first
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close()
		return true
	}

	// try all interfaces
	addr = fmt.Sprintf(":%d", port)
	ln2, err2 := net.Listen("tcp", addr)
	if err2 == nil {
		ln2.Close()
		return true
	}
	return false
}
