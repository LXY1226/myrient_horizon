package main

import "net"

func getListener(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
