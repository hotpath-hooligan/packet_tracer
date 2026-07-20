package main

import (
	"encoding/binary"
	"fmt"
)

// Ethernet frame constants
const (
	MAC_ADDR_SIZE        = 6    // 6 bytes for MAC address
	ETHERNET_HDR_SIZE    = 14   // 6 (dst) + 6 (src) + 2 (ethertype)
	ETHERNET_FCS_SIZE    = 4    // Frame Check Sequence (CRC) - handled by hardware
	ETHERNET_MIN_PAYLOAD = 46   // Minimum payload size
	ETHERNET_MAX_PAYLOAD = 1500 // Maximum payload size (MTU)
)

// EtherType values (common protocols)
const (
	ETHERTYPE_IP   = 0x0800 // IPv4
	ETHERTYPE_ARP  = 0x0806 // ARP
	ETHERTYPE_IPV6 = 0x86DD // IPv6
	ETHERTYPE_VLAN = 0x8100 // VLAN tagging
	ETHERTYPE_LLDP = 0x88CC // Link Layer Discovery Protocol
)

// IP Protocol numbers (used in IP header protocol field)
const (
	IPPROTO_ICMP = 1  // ICMP
	IPPROTO_IGMP = 2  // IGMP
	IPPROTO_IPIP = 4  // IP-in-IP encapsulation
	IPPROTO_TCP  = 6  // TCP
	IPPROTO_UDP  = 17 // UDP
	IPPROTO_GRE  = 47 // GRE tunneling
	IPPROTO_ESP  = 50 // IPsec ESP
	IPPROTO_AH   = 51 // IPsec AH
	IPPROTO_OSPF = 89 // OSPF
)

// EthernetHeader represents the Ethernet II frame header
type EthernetHeader struct {
	dst_mac   MacAddr // Destination MAC address (6 bytes)
	src_mac   MacAddr // Source MAC address (6 bytes)
	ethertype uint16  // EtherType field (2 bytes) - identifies protocol
}

// ethernet_frame_t represents a complete Ethernet frame
type EthernetFrame struct {
	header  EthernetHeader
	payload []byte // Variable length payload
	fcs     uint32 // Frame Check Sequence (CRC-32) - not computed/validated (hardware responsibility)
}

// serialize_ethernet_header converts Ethernet header to bytes
// Returns byte slice of size ETHERNET_HDR_SIZE (14 bytes)
func serialize_ethernet_header(hdr *EthernetHeader) []byte {
	buffer := make([]byte, ETHERNET_HDR_SIZE)

	// Copy destination MAC (6 bytes)
	copy(buffer[0:6], hdr.dst_mac[:])

	// Copy source MAC (6 bytes)
	copy(buffer[6:12], hdr.src_mac[:])

	// Copy EtherType (2 bytes, big-endian/network byte order)
	binary.BigEndian.PutUint16(buffer[12:14], hdr.ethertype)

	return buffer
}

// deserialize_ethernet_header parses bytes into Ethernet header
func deserialize_ethernet_header(buffer []byte) (*EthernetHeader, error) {
	if len(buffer) < ETHERNET_HDR_SIZE {
		return nil, fmt.Errorf("buffer too small for Ethernet header: need %d bytes, got %d",
			ETHERNET_HDR_SIZE, len(buffer))
	}

	hdr := &EthernetHeader{}

	// Extract destination MAC (6 bytes)
	copy(hdr.dst_mac[:], buffer[0:6])

	// Extract source MAC (6 bytes)
	copy(hdr.src_mac[:], buffer[6:12])

	// Extract EtherType (2 bytes, big-endian)
	hdr.ethertype = binary.BigEndian.Uint16(buffer[12:14])

	return hdr, nil
}

// serialize_ethernet_frame converts entire frame to bytes
func serialize_ethernet_frame(frame *EthernetFrame) []byte {
	hdr_bytes := serialize_ethernet_header(&frame.header)

	// Combine header + payload
	frame_bytes := make([]byte, len(hdr_bytes)+len(frame.payload))
	copy(frame_bytes, hdr_bytes)
	copy(frame_bytes[len(hdr_bytes):], frame.payload)

	return frame_bytes
}

// tag_packet_with_ethernet_hdr encapsulates data into an Ethernet frame
// This API takes raw data and wraps it with an Ethernet header, creating a complete frame.
// All header fields (dst_mac, src_mac, ethertype) are initialized to zero.
// FCS is set to zero and not computed (would be calculated by hardware NIC in real networks).
//
// Args:
//   - pkt: pointer to the data buffer to be encapsulated
//   - pkt_size: size of the data in bytes
//
// Returns: pointer to EthernetFrame with zeroed header and the data as payload
//
// Usage: This API is used whenever we need to attach a new Ethernet header to a packet.
// The caller should later set the MAC addresses and EtherType.
func tag_packet_with_ethernet_hdr(pkt []byte, pkt_size int) *EthernetFrame {
	if pkt == nil || pkt_size <= 0 {
		return nil
	}

	// Validate pkt_size doesn't exceed the provided buffer
	if pkt_size > len(pkt) {
		pkt_size = len(pkt)
	}

	// Create Ethernet frame with zeroed header
	frame := &EthernetFrame{
		header: EthernetHeader{
			dst_mac:   MacAddr{0, 0, 0, 0, 0, 0}, // Destination MAC: 00:00:00:00:00:00
			src_mac:   MacAddr{0, 0, 0, 0, 0, 0}, // Source MAC: 00:00:00:00:00:00
			ethertype: 0,                         // EtherType: 0x0000
		},
		payload: make([]byte, pkt_size), // Allocate payload buffer
		fcs:     0,                      // FCS: not computed (hardware responsibility)
	}

	// Copy the data into the payload
	copy(frame.payload, pkt[:pkt_size])

	return frame
}

// determines if an Ethernet frame should be accepted by an interface
//
// Args:
//   - intf: pointer to the receiving interface
//   - ethernet_hdr: pointer to the Ethernet header of the incoming frame
//
// Returns:
//   - true: if frame should be accepted (destination MAC matches interface MAC, is broadcast, or is multicast)
//   - false: if frame should be rejected
//
// Logic:
//   - If interface is NOT in L3 mode -> reject (return false)
//   - If interface is in L3 mode AND destination MAC matches interface MAC -> accept (return true)
//   - If interface is in L3 mode AND destination MAC is a group address (broadcast or multicast) -> accept (return true)
//   - All other cases -> reject (return false)
func l2_frame_recv_qualify_on_iface(intf *Interface, ethernet_hdr *EthernetHeader) bool {
	if intf == nil || ethernet_hdr == nil {
		return false
	}

	// Check if interface is working in L3 mode
	// If NOT, reject the frame
	if !IS_INTF_L3_MODE(intf) {
		return false
	}

	// Get interface's MAC address
	intf_mac := intf.GetMac()
	if intf_mac == nil {
		return false
	}

	// Check if destination MAC matches interface MAC (unicast to this interface)
	dst_mac := &ethernet_hdr.dst_mac
	if *dst_mac == *intf_mac {
		return true
	}

	// The low bit of the first octet is the IEEE individual/group bit. Layer 3
	// protocols use group MACs for traffic such as IPv4 and IPv6 multicast.
	// Broadcast is also a group address, so this covers both cases.
	if dst_mac[0]&0x01 != 0 {
		return true
	}

	return false
}

// Entry point into the TCP/IP stack for received frames
// This function is invoked when a node receives a packet from the network.
// It processes the Ethernet frame and dispatches it to appropriate upper layer protocols.
//
// Args:
//   - node: pointer to the receiving node
//   - intf: pointer to the interface that received the frame
//   - pkt: pointer to the packet data (after removing auxiliary data)
//   - pkt_size: size of the packet in bytes
//
// Returns: 0 on success, -1 on error
func layer_2_frame_recv(node *Node, intf *Interface, pkt []byte, pkt_size int) int {
	// Validate inputs
	if node == nil || intf == nil || pkt == nil || pkt_size <= 0 || pkt_size > len(pkt) {
		return -1
	}

	node_name := get_node_name(node)
	intf_name := get_interface_name(intf)
	LogDebug("L2: Node %s received frame on interface %s (%d bytes)",
		node_name, intf_name, pkt_size)

	if pkt_size < ETHERNET_HDR_SIZE {
		LogError("L2: Frame too small: %d bytes (minimum %d)", pkt_size, ETHERNET_HDR_SIZE)
		emitInterfaceEvent(node, intf, "ETHERNET", "frame_dropped", map[string]string{
			"reason": "frame_too_small",
		})
		return -1
	}

	eth_hdr, err := deserialize_ethernet_header(pkt)
	if err != nil {
		LogError("L2: Failed to parse Ethernet header: %v", err)
		emitInterfaceEvent(node, intf, "ETHERNET", "frame_dropped", map[string]string{
			"reason": "invalid_ethernet_header",
		})
		return -1
	}
	if eth_hdr.ethertype == ETHERTYPE_LLDP || isLLDPMulticastMAC(&eth_hdr.dst_mac) {
		return layer2FrameRecvLLDP(node, intf, eth_hdr, pkt, pkt_size)
	}
	// BPDUs are addressed to a reserved multicast address and are consumed by
	// the bridge itself. They must be handled before any port state check,
	// because a blocked port still has to listen to the spanning tree.
	if isSTPBridgeGroupMAC(&eth_hdr.dst_mac) {
		return layer2FrameRecvSTP(node, intf, eth_hdr, pkt, pkt_size)
	}

	// Check if interface is in L3 mode
	if !IS_INTF_L3_MODE(intf) {
		LogDebug("L2: Interface %s is in L2 mode - forwarding to L2 switch", intf_name)
		// Handle L2 switching for L2 mode interfaces
		l2_switch_recv_frame(node, intf, pkt, pkt_size)
		return 0
	}

	LogDebug("L2: Interface %s is in L3 mode - processing frame", intf_name)

	LogDebug("L2: Ethernet header parsed: dst=%s, src=%s, type=0x%04x",
		eth_hdr.dst_mac.String(), eth_hdr.src_mac.String(), eth_hdr.ethertype)

	// Qualify the frame - check if it should be accepted by this interface.
	// This accepts the interface MAC and Ethernet group addresses.
	if !l2_frame_recv_qualify_on_iface(intf, eth_hdr) {
		LogDebug("L2: Frame does not qualify for reception on L3 interface %s (dst MAC: %s, intf MAC: %s) - dropping",
			intf_name, eth_hdr.dst_mac.String(), intf.GetMac().String())
		emitInterfaceEvent(node, intf, frameProtocol(pkt), "frame_dropped", map[string]string{
			"destinationMac": eth_hdr.dst_mac.String(),
			"reason":         "destination_mac_not_accepted",
		})
		return 0
	}

	LogDebug("L2: Frame qualified for reception (dst MAC: %s)",
		eth_hdr.dst_mac.String())

	LogDebug("L2: EtherType: 0x%04x", eth_hdr.ethertype)

	// Dispatch based on EtherType
	switch eth_hdr.ethertype {
	case ETHERTYPE_ARP:
		// Handle ARP packet
		LogDebug("L2: Processing ARP packet")
		return layer_2_frame_recv_arp(node, intf, pkt, pkt_size)

	case ETHERTYPE_IP:
		// Handle IPv4 packet - promote to L3
		LogDebug("L2: Processing IPv4 packet, promoting to L3")
		// Extract IP packet (skip Ethernet header)
		ipPkt := pkt[ETHERNET_HDR_SIZE:]
		ipPktSize := pkt_size - ETHERNET_HDR_SIZE
		PromotePacketToLayer3(node, intf, ipPkt, ipPktSize, ETHERTYPE_IP)
		return 0

	case ETHERTYPE_IPV6:
		// Handle IPv6 packet
		LogInfo("L2: Processing IPv6 packet (not yet implemented)")
		emitInterfaceEvent(node, intf, "IPV6", "frame_dropped", map[string]string{
			"reason": "protocol_not_implemented",
		})
		// TODO: Implement IPv6 processing
		return 0

	case ETHERTYPE_VLAN:
		// Normalize the frame before dispatching the encapsulated EtherType. The
		// existing ARP and IP handlers both expect a conventional 14-byte
		// Ethernet header, so removing the tag here keeps those APIs consistent.
		if pkt_size < VLAN_HEADER_SIZE {
			LogError("L2: VLAN-tagged frame too small: %d bytes (minimum %d)", pkt_size, VLAN_HEADER_SIZE)
			emitInterfaceEvent(node, intf, "ETHERNET", "frame_dropped", map[string]string{
				"reason": "invalid_vlan_header",
			})
			return -1
		}
		vlanID := get_frame_vlan_id(pkt[:pkt_size])
		untaggedPkt, err := remove_vlan_tag(pkt[:pkt_size])
		if err != nil {
			LogError("L2: Failed to remove VLAN tag: %v", err)
			emitInterfaceEvent(node, intf, "ETHERNET", "frame_dropped", map[string]string{
				"reason": "invalid_vlan_header",
			})
			return -1
		}
		LogDebug("L2: Decapsulated 802.1Q VLAN %d frame on L3 interface %s", vlanID, intf_name)
		return layer_2_frame_recv(node, intf, untaggedPkt, len(untaggedPkt))

	default:
		LogError("L2: Unknown EtherType: 0x%04x", eth_hdr.ethertype)
		emitInterfaceEvent(node, intf, "ETHERNET", "frame_dropped", map[string]string{
			"etherType": fmt.Sprintf("0x%04x", eth_hdr.ethertype),
			"reason":    "unsupported_ethertype",
		})
		return -1
	}
}

// layer_2_frame_recv_arp processes incoming ARP packets
// Args:
//   - node: pointer to the receiving node
//   - intf: pointer to the interface that received the frame
//   - pkt: pointer to the packet data (Ethernet + ARP headers)
//   - pkt_size: size of the packet in bytes
//
// Returns: 0 on success, -1 on error
func layer_2_frame_recv_arp(node *Node, intf *Interface, pkt []byte, pkt_size int) int {
	// Validate ARP packet size (Ethernet header + ARP header = 14 + 28 = 42 bytes)
	const MIN_ARP_PKT_SIZE = ETHERNET_HDR_SIZE + 28
	if pkt_size < MIN_ARP_PKT_SIZE {
		LogError("ARP: ARP packet too small: %d bytes (minimum %d)", pkt_size, MIN_ARP_PKT_SIZE)
		return -1
	}

	// Extract op_code from ARP header to determine operation type
	// ARP header starts at offset ETHERNET_HDR_SIZE
	// op_code is at offset 6 within ARP header (2 bytes for hw_type, 2 for proto_type, 1 for hw_addr_len, 1 for proto_addr_len)
	op_code_offset := ETHERNET_HDR_SIZE + 6
	if pkt_size < op_code_offset+2 {
		LogError("ARP: Packet too small to read op_code")
		return -1
	}

	// Read op_code (2 bytes, network byte order)
	op_code := binary.BigEndian.Uint16(pkt[op_code_offset : op_code_offset+2])

	LogDebug("ARP: ARP operation: %d", op_code)

	// Dispatch based on ARP operation code
	switch op_code {
	case ARP_OP_REQUEST:
		// Handle ARP request
		LogDebug("ARP: Received ARP request")
		process_arp_broadcast_request(node, intf, pkt)
		return 0

	case ARP_OP_REPLY:
		// Handle ARP reply
		LogInfo("ARP: Received ARP reply on node %s interface %s", get_node_name(node), get_interface_name(intf))
		process_arp_reply_msg(node, intf, pkt)
		return 0

	default:
		LogError("ARP: Unknown ARP operation: %d", op_code)
		return -1
	}
}

// DemotePacketToLayer2 sends a packet from L3 down to L2
// This function encapsulates the L3 packet in an Ethernet frame and sends it
//
// Args:
//   - node: the sending node
//   - nextHopIP: next hop IP address (0 if unknown/broadcast)
//   - oifName: outgoing interface name (empty string for broadcast/unknown)
//   - pkt: L3 packet data (IP header + payload)
//   - pktSize: size of the L3 packet
//   - ethertype: Ethernet type (typically ETHERTYPE_IP)
func DemotePacketToLayer2(node *Node, nextHopIP uint32, oifName string, pkt []byte, pktSize int, ethertype uint16) {
	if node == nil || pkt == nil || pktSize <= 0 {
		LogError("L2: Invalid parameters for demote to L2")
		return
	}

	nodeName := get_node_name(node)
	nextHopIPStr := ""
	if nextHopIP != 0 {
		nextHopIPStr = IPUint32ToString(nextHopIP)
	}

	LogDebug("L2: Node %s demoting packet to L2: nextHop=%s, oif=%s, size=%d",
		nodeName, nextHopIPStr, oifName, pktSize)

	// Determine outgoing interface
	var oif *Interface
	if oifName != "" {
		// Use specified interface
		oif = get_node_if_by_name(node, oifName)
		if oif == nil {
			LogError("L2: Interface %s not found on node %s", oifName, nodeName)
			return
		}
	} else if nextHopIP != 0 {
		// Find interface with matching subnet
		oif = node_get_matching_subnet_interface(node, nextHopIPStr)
		if oif == nil {
			LogError("L2: No interface found for next hop %s on node %s", nextHopIPStr, nodeName)
			return
		}
	} else {
		LogError("L2: Cannot determine outgoing interface (no oif and no nextHopIP)")
		return
	}

	oifName = get_interface_name(oif)
	LogDebug("L2: Using outgoing interface: %s", oifName)

	// Check if we need to resolve MAC address via ARP
	var dstMAC *MacAddr

	if nextHopIP != 0 {
		nextHopIPAddr := IpAddr{}
		if !set_ip_addr(&nextHopIPAddr, nextHopIPStr) {
			LogError("L2: Invalid next hop IP %s", nextHopIPStr)
			return
		}

		// Build the queued form before touching the ARP table. The atomic table
		// operation below either returns a resolved MAC or takes ownership of
		// this frame by adding it to the pending queue.
		srcMAC := oif.GetMac()
		if srcMAC == nil {
			LogError("L2: Interface %s has no MAC address", oifName)
			return
		}
		pendingFrame := tag_packet_with_ethernet_hdr(pkt, pktSize)
		if pendingFrame == nil {
			LogError("L2: Failed to create Ethernet frame")
			return
		}
		pendingFrame.header.dst_mac = MacAddr{}
		pendingFrame.header.src_mac = *srcMAC
		pendingFrame.header.ethertype = ethertype
		frameBytes := serialize_ethernet_frame(pendingFrame)

		var requestNeeded, queued bool
		dstMAC, requestNeeded, queued = arp_table_resolve_or_queue(
			&node.node_nw_prop.arp_table,
			&nextHopIPAddr,
			oifName,
			frameBytes,
			len(frameBytes),
			func(n *Node, iface *Interface, packet []byte, size int) {
				if size < ETHERNET_HDR_SIZE {
					return
				}
				resolvedMAC := arp_table_lookup(&n.node_nw_prop.arp_table, &nextHopIPAddr)
				if resolvedMAC == nil {
					return
				}
				emitInterfaceEventWithTable(n, iface, "ARP", "arp_entry_selected", map[string]string{
					"ip":  nextHopIPStr,
					"mac": resolvedMAC.String(),
				}, arpTableReference("hit", nextHopIPStr, resolvedMAC.String(), get_interface_name(iface), false))
				copy(packet[0:6], resolvedMAC[:])
				if err := send_frame(packet, size, iface); err != nil {
					LogError("L2: Failed to send queued packet: %v", err)
				} else {
					LogInfo("L2: Sent queued packet via %s (%d bytes)", get_interface_name(iface), size)
				}
			},
			func() {
				emitInterfaceEvent(node, oif, frameProtocol(frameBytes), "frame_dropped", map[string]string{
					"destinationIp": nextHopIPStr,
					"reason":        "arp_resolution_timeout",
				})
			},
			func() {
				send_arp_broadcast_request(node, oif, nextHopIPStr)
			},
		)
		if dstMAC == nil {
			if !queued {
				LogError("L2: Failed to queue packet pending ARP resolution for %s", nextHopIPStr)
				return
			}
			LogDebug("L2: No ARP entry for %s, queueing packet and sending ARP request", nextHopIPStr)
			emitInterfaceEventWithTable(node, oif, "ARP", "arp_lookup_missed", map[string]string{
				"targetIp": nextHopIPStr,
			}, arpTableMissReference(nextHopIPStr))
			if requestNeeded {
				send_arp_broadcast_request(node, oif, nextHopIPStr)
			}
			return
		}

		LogDebug("L2: Found ARP entry: %s -> %s", nextHopIPStr, dstMAC.String())
		emitInterfaceEventWithTable(node, oif, "ARP", "arp_entry_selected", map[string]string{
			"ip":  nextHopIPStr,
			"mac": dstMAC.String(),
		}, arpTableReference("hit", nextHopIPStr, dstMAC.String(), oifName, false))
	} else {
		// Broadcast
		broadcastMAC := MacAddr{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
		dstMAC = &broadcastMAC
		LogDebug("L2: Using broadcast MAC")
	}

	// Get source MAC from interface
	srcMAC := oif.GetMac()
	if srcMAC == nil {
		LogError("L2: Interface %s has no MAC address", oifName)
		return
	}

	// Create Ethernet frame
	frame := tag_packet_with_ethernet_hdr(pkt, pktSize)
	if frame == nil {
		LogError("L2: Failed to create Ethernet frame")
		return
	}

	// Set Ethernet header fields
	frame.header.dst_mac = *dstMAC
	frame.header.src_mac = *srcMAC
	frame.header.ethertype = ethertype

	// Serialize frame
	frameBytes := serialize_ethernet_frame(frame)

	// Send the packet
	err := send_frame(frameBytes, len(frameBytes), oif)
	if err != nil {
		LogError("L2: Failed to send packet: %v", err)
		return
	}

	LogInfo("L2: Node %s sent packet via %s to %s (%d bytes)",
		nodeName, oifName, dstMAC.String(), len(frameBytes))
}
