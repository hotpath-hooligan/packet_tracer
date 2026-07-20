//go:build !js

package main

func newDefaultTransport() FrameTransport {
	return NewUDPTransport()
}
