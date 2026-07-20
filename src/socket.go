//go:build !js

package main

import (
	"fmt"
	"net"
	"sync"
	"time"
)

type UDPTransport struct {
	mu      sync.RWMutex
	sockets map[*Node]*net.UDPConn
	graph   *Graph
	stopCh  chan struct{}
	started bool
	closed  bool
	wg      sync.WaitGroup
}

func NewUDPTransport() *UDPTransport {
	return &UDPTransport{sockets: make(map[*Node]*net.UDPConn)}
}

func (transport *UDPTransport) Name() string {
	return "udp"
}

func (transport *UDPTransport) Register(node *Node) error {
	if node == nil {
		return fmt.Errorf("node cannot be nil")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return fmt.Errorf("failed to bind node %s UDP socket: %w", get_node_name(node), err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		conn.Close()
		return fmt.Errorf("UDP transport is closed")
	}
	transport.sockets[node] = conn
	LogInfo("Node %s: UDP socket initialized on %s", get_node_name(node), conn.LocalAddr())
	return nil
}

func (transport *UDPTransport) Start(graph *Graph) error {
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return fmt.Errorf("UDP transport is closed")
	}
	if transport.started {
		transport.mu.Unlock()
		return nil
	}
	transport.graph = graph
	transport.stopCh = make(chan struct{})
	transport.started = true
	stopCh := transport.stopCh
	sockets := make(map[*Node]*net.UDPConn, len(transport.sockets))
	for node, conn := range transport.sockets {
		sockets[node] = conn
	}
	transport.mu.Unlock()

	for node, conn := range sockets {
		transport.wg.Add(1)
		go transport.monitor(node, conn, stopCh)
	}
	return nil
}

func (transport *UDPTransport) Send(localIntf *Interface, frame []byte) error {
	if localIntf == nil || localIntf.att_node == nil {
		return fmt.Errorf("local interface is not attached to a node")
	}
	remoteIntf := get_remote_interface(localIntf)
	if remoteIntf == nil || remoteIntf.att_node == nil {
		return fmt.Errorf("interface %s has no connected peer", get_interface_name(localIntf))
	}
	if !localIntf.link.IsUp() {
		return fmt.Errorf("link is down")
	}

	transport.mu.RLock()
	sender := transport.sockets[localIntf.att_node]
	receiver := transport.sockets[remoteIntf.att_node]
	transport.mu.RUnlock()
	if sender == nil || receiver == nil {
		return fmt.Errorf("UDP endpoint is missing for link %s", get_interface_name(localIntf))
	}
	receiverAddr, ok := receiver.LocalAddr().(*net.UDPAddr)
	if !ok {
		return fmt.Errorf("unexpected UDP address type")
	}

	payload := make([]byte, IF_NAME_SIZE+len(frame))
	copy(payload[:IF_NAME_SIZE], get_interface_name(remoteIntf))
	copy(payload[IF_NAME_SIZE:], frame)
	if _, err := sender.WriteToUDP(payload, receiverAddr); err != nil {
		return fmt.Errorf("failed to send UDP frame from %s to %s: %w", get_node_name(localIntf.att_node), get_node_name(remoteIntf.att_node), err)
	}
	return nil
}

func (transport *UDPTransport) Stop() {
	transport.mu.Lock()
	if !transport.started {
		transport.mu.Unlock()
		return
	}
	close(transport.stopCh)
	transport.started = false
	transport.stopCh = nil
	transport.mu.Unlock()
	transport.wg.Wait()
}

func (transport *UDPTransport) Close() error {
	transport.Stop()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return nil
	}
	transport.closed = true
	var closeErr error
	for node, conn := range transport.sockets {
		if err := conn.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("failed to close node %s UDP socket: %w", get_node_name(node), err)
		}
	}
	transport.sockets = nil
	return closeErr
}

func (transport *UDPTransport) monitor(node *Node, conn *net.UDPConn, stopCh <-chan struct{}) {
	defer transport.wg.Done()
	buffer := make([]byte, IF_NAME_SIZE+VLAN_HEADER_SIZE+ETHERNET_MAX_PAYLOAD)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			return
		}
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				select {
				case <-stopCh:
					return
				default:
					continue
				}
			}
			select {
			case <-stopCh:
				return
			default:
				LogError("Node %s UDP receive failed: %v", get_node_name(node), err)
				continue
			}
		}
		if n <= IF_NAME_SIZE {
			continue
		}
		intfName := string(buffer[:IF_NAME_SIZE])
		for i, b := range []byte(intfName) {
			if b == 0 {
				intfName = intfName[:i]
				break
			}
		}
		intf := node_get_matching_intf_by_name(node, intfName)
		if intf == nil {
			LogWarn("Interface %s not found on node %s", intfName, get_node_name(node))
			continue
		}
		deliverTransportFrame(intf, append([]byte(nil), buffer[IF_NAME_SIZE:n]...))
	}
}
