package main

import (
	"encoding/binary"
	"fmt"
)

type FrameTransport interface {
	Name() string
	Register(node *Node) error
	Start(graph *Graph) error
	Send(localIntf *Interface, frame []byte) error
	Stop()
	Close() error
}

func send_frame(packetBuffer []byte, packetSize int, localIntf *Interface) error {
	if packetBuffer == nil {
		return fmt.Errorf("packet buffer cannot be nil")
	}
	if packetSize <= 0 || packetSize > len(packetBuffer) {
		return fmt.Errorf("invalid packet size: %d", packetSize)
	}
	if localIntf == nil || localIntf.att_node == nil {
		return fmt.Errorf("local interface is not attached to a node")
	}
	remoteIntf := get_remote_interface(localIntf)
	if remoteIntf == nil || remoteIntf.att_node == nil {
		return fmt.Errorf("interface %s has no connected peer", get_interface_name(localIntf))
	}
	graph := localIntf.att_node.graph
	if graph == nil || graph.transport == nil {
		return fmt.Errorf("node %s has no frame transport", get_node_name(localIntf.att_node))
	}

	frame := append([]byte(nil), packetBuffer[:packetSize]...)
	if !localIntf.link.IsUp() {
		graph.emitFrameEventWithFields("frame_dropped", localIntf, remoteIntf, frame, map[string]string{
			"reason": "link_down",
		})
		return fmt.Errorf("link %s:%s to %s:%s is down",
			get_node_name(localIntf.att_node), get_interface_name(localIntf),
			get_node_name(remoteIntf.att_node), get_interface_name(remoteIntf))
	}
	graph.emitFrameEvent("frame_sent", localIntf, remoteIntf, frame)
	if err := graph.transport.Send(localIntf, frame); err != nil {
		graph.emitFrameEventWithFields("frame_dropped", localIntf, remoteIntf, frame, map[string]string{
			"error":  err.Error(),
			"reason": "transport_error",
		})
		return err
	}
	log_packet_transmission(localIntf.att_node, remoteIntf.att_node, localIntf, remoteIntf, packetSize)
	return nil
}

func send_udp_packet(packetBuffer []byte, packetSize int, localIntf *Interface) error {
	return send_frame(packetBuffer, packetSize, localIntf)
}

func deliverTransportFrame(intf *Interface, frame []byte) {
	if intf == nil || intf.att_node == nil || intf.link == nil || !intf.link.IsUp() || len(frame) == 0 {
		return
	}
	remoteIntf := get_remote_interface(intf)
	if intf.att_node.graph != nil && remoteIntf != nil {
		intf.att_node.graph.emitFrameEvent("frame_received", remoteIntf, intf, frame)
	}
	layer_2_frame_recv(intf.att_node, intf, frame, len(frame))
}

func frameProtocol(frame []byte) string {
	if len(frame) < ETHERNET_HDR_SIZE {
		return "ETHERNET"
	}
	// BPDUs are 802.3 framed, so bytes 12-13 carry a length rather than an
	// EtherType. They are identified by their reserved destination address.
	var destination MacAddr
	copy(destination[:], frame[0:6])
	if isSTPBridgeGroupMAC(&destination) {
		return "STP"
	}

	etherType := binary.BigEndian.Uint16(frame[12:14])
	offset := ETHERNET_HDR_SIZE
	if etherType == ETHERTYPE_VLAN && len(frame) >= VLAN_HEADER_SIZE {
		etherType = binary.BigEndian.Uint16(frame[16:18])
		offset = VLAN_HEADER_SIZE
	}
	switch etherType {
	case ETHERTYPE_ARP:
		return "ARP"
	case ETHERTYPE_LLDP:
		return "LLDP"
	case ETHERTYPE_IP:
		if len(frame) <= offset+9 {
			return "IP"
		}
		switch frame[offset+9] {
		case PROTO_ICMP:
			return "ICMP"
		case 200:
			return "RIP"
		default:
			return "IP"
		}
	default:
		return "ETHERNET"
	}
}
