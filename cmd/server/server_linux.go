package main

import (
	"net"
	"os"
)

func getListener(addr string) (net.Listener, error) {
	os.Remove("http.sock")
	return net.ListenUnix("unix", &net.UnixAddr{"http.sock", "unix"})
}
