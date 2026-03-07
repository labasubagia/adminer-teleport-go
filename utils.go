package main

import (
	"fmt"
	"net"
	"os"
)

// isPortAvailable attempts to bind to the given TCP port on loopback and
// all interfaces. If binding fails on all addresses it is considered in use.
func isPortAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close()
		return true
	}
	return false
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
