package main

import (
	"fmt"
	"sync"
)

type queuedFrame struct {
	intf  *Interface
	frame []byte
}

type MemoryTransport struct {
	mu       sync.Mutex
	queue    []queuedFrame
	draining bool
	closed   bool
}

func NewMemoryTransport() *MemoryTransport {
	return &MemoryTransport{queue: make([]queuedFrame, 0)}
}

func (transport *MemoryTransport) Name() string {
	return "memory"
}

func (transport *MemoryTransport) Register(node *Node) error {
	return nil
}

func (transport *MemoryTransport) Start(graph *Graph) error {
	return nil
}

func (transport *MemoryTransport) Send(localIntf *Interface, frame []byte) error {
	remoteIntf := get_remote_interface(localIntf)
	if remoteIntf == nil {
		return fmt.Errorf("interface has no connected peer")
	}
	if !localIntf.link.IsUp() {
		return fmt.Errorf("link is down")
	}

	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return fmt.Errorf("memory transport is closed")
	}
	transport.queue = append(transport.queue, queuedFrame{intf: remoteIntf, frame: append([]byte(nil), frame...)})
	if transport.draining {
		transport.mu.Unlock()
		return nil
	}
	transport.draining = true
	transport.mu.Unlock()

	for {
		transport.mu.Lock()
		if len(transport.queue) == 0 {
			transport.draining = false
			transport.mu.Unlock()
			return nil
		}
		next := transport.queue[0]
		transport.queue = transport.queue[1:]
		transport.mu.Unlock()
		deliverTransportFrame(next.intf, next.frame)
	}
}

func (transport *MemoryTransport) Stop() {
}

func (transport *MemoryTransport) Close() error {
	transport.mu.Lock()
	transport.closed = true
	transport.queue = nil
	transport.mu.Unlock()
	return nil
}
