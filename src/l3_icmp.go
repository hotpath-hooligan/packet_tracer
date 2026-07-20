package main

import (
	"encoding/binary"
	"fmt"
)

// ICMP message types and codes. The type and code occupy the first two bytes
// of every ICMP message.
const (
	ICMP_TYPE_ECHO_REPLY       uint8 = 0
	ICMP_TYPE_DEST_UNREACHABLE uint8 = 3
	ICMP_TYPE_SOURCE_QUENCH    uint8 = 4
	ICMP_TYPE_REDIRECT         uint8 = 5
	ICMP_TYPE_ECHO_REQUEST     uint8 = 8
	ICMP_TYPE_TIME_EXCEEDED    uint8 = 11
	ICMP_TYPE_PARAM_PROBLEM    uint8 = 12

	ICMP_CODE_NET_UNREACHABLE  uint8 = 0
	ICMP_CODE_HOST_UNREACHABLE uint8 = 1
	ICMP_CODE_TTL_EXCEEDED     uint8 = 0
)

// icmpMessageName gives events and logs a readable name instead of a number.
func icmpMessageName(icmpType, icmpCode uint8) string {
	switch icmpType {
	case ICMP_TYPE_ECHO_REPLY:
		return "echo_reply"
	case ICMP_TYPE_ECHO_REQUEST:
		return "echo_request"
	case ICMP_TYPE_TIME_EXCEEDED:
		return "time_exceeded"
	case ICMP_TYPE_DEST_UNREACHABLE:
		switch icmpCode {
		case ICMP_CODE_NET_UNREACHABLE:
			return "network_unreachable"
		case ICMP_CODE_HOST_UNREACHABLE:
			return "host_unreachable"
		}
		return "destination_unreachable"
	}
	return fmt.Sprintf("type_%d_code_%d", icmpType, icmpCode)
}

// isICMPErrorType reports whether a message is itself an error report. An
// error about an error would let two misconfigured routers trade packets
// forever, so RFC 1122 forbids generating one.
func isICMPErrorType(icmpType uint8) bool {
	switch icmpType {
	case ICMP_TYPE_DEST_UNREACHABLE, ICMP_TYPE_SOURCE_QUENCH,
		ICMP_TYPE_REDIRECT, ICMP_TYPE_TIME_EXCEEDED, ICMP_TYPE_PARAM_PROBLEM:
		return true
	}
	return false
}

// isMulticastOrBroadcastIP reports whether an address names a group rather
// than a single host: class D and above, or the all-ones broadcast.
func isMulticastOrBroadcastIP(ip uint32) bool {
	return ip>>28 >= 0xE || ip == 0xFFFFFFFF
}

// icmpErrorPermitted applies the RFC 1122 §3.2.2 suppression rules. A router
// that answers every dropped packet becomes an amplifier: a single packet sent
// to a broadcast address would draw a reply from every host on the wire.
func icmpErrorPermitted(node *Node, origHdr *IPHeader, origPkt []byte) bool {
	if origHdr == nil || len(origPkt) < 20 {
		return false
	}
	// A datagram with no real source has nowhere to send the report.
	if origHdr.SrcIP == 0 || isMulticastOrBroadcastIP(origHdr.SrcIP) {
		return false
	}
	// Group traffic is dropped quietly for the same amplification reason.
	if isMulticastOrBroadcastIP(origHdr.DstIP) {
		return false
	}
	// Only the first fragment carries the transport header the report quotes;
	// later fragments would produce one error per fragment.
	if origHdr.FragOffset != 0 {
		return false
	}
	if origHdr.Protocol == PROTO_ICMP {
		headerLen := GetIPHeaderLen(origHdr)
		if len(origPkt) < headerLen+1 {
			return false
		}
		if isICMPErrorType(origPkt[headerLen]) {
			return false
		}
	}
	return true
}

// buildICMPError assembles an ICMP error message: an 8-byte header followed by
// the offending datagram's IP header and the first 8 bytes past it. Those 8
// bytes are what let the original sender match the report to a specific
// conversation — for ICMP they hold the echo identifier and sequence number,
// which is exactly how traceroute attributes a hop to a probe.
func buildICMPError(origPkt []byte, icmpType, icmpCode uint8) []byte {
	headerLen := int(origPkt[0]&0x0F) * 4
	if headerLen < 20 {
		headerLen = 20
	}
	quoteLen := headerLen + 8
	if quoteLen > len(origPkt) {
		quoteLen = len(origPkt)
	}

	msg := make([]byte, 8+quoteLen)
	msg[0] = icmpType
	msg[1] = icmpCode
	// Bytes 4-7 are unused for both Time Exceeded and Destination Unreachable
	// and stay zero.
	copy(msg[8:], origPkt[:quoteLen])
	binary.BigEndian.PutUint16(msg[2:4], internetChecksum(msg))
	return msg
}

// sendICMPErrorRouted reports a forwarding failure back to the sender, routing
// the report through this node's own routing table.
//
// RFC 1812 §4.3.2.4 lets a router use any of its addresses as the source, and
// recommends the interface the offending packet arrived on. That choice is
// what makes traceroute useful: each hop is named by the address facing the
// probe, so the printed path matches the links actually traversed.
func sendICMPErrorRouted(node *Node, ingress *Interface, origPkt []byte, origHdr *IPHeader, icmpType, icmpCode uint8) {
	if node == nil || !icmpErrorPermitted(node, origHdr, origPkt) {
		return
	}

	var srcIP uint32
	if ingress != nil && ingress.IsIPConfigured() {
		if ip, err := IPStringToUint32(ingress.GetIP().String()); err == nil {
			srcIP = ip
		}
	}

	msg := buildICMPError(origPkt, icmpType, icmpCode)
	emitInterfaceEvent(node, ingress, "ICMP", "icmp_error_created", map[string]string{
		"destinationIp": IPUint32ToString(origHdr.SrcIP),
		"icmpType":      icmpMessageName(icmpType, icmpCode),
		"sourceIp":      IPUint32ToString(srcIP),
	})
	LogInfo("ICMP: Node %s: Reporting %s to %s", get_node_name(node),
		icmpMessageName(icmpType, icmpCode), IPUint32ToString(origHdr.SrcIP))

	demotePacketToLayer3WithSource(node, msg, len(msg), PROTO_ICMP, origHdr.SrcIP, srcIP)
}

// sendICMPErrorViaInterface reports a failure out one named interface with an
// explicit source address, bypassing the routing table. Inter-VLAN routing on
// a switch needs this: the switch has no routing table, but the SVI of the
// ingress VLAN is precisely the gateway address the sender was aiming at.
func sendICMPErrorViaInterface(node *Node, egress *Interface, srcIP uint32, origPkt []byte, origHdr *IPHeader, icmpType, icmpCode uint8) {
	if node == nil || egress == nil || srcIP == 0 {
		return
	}
	if !icmpErrorPermitted(node, origHdr, origPkt) {
		return
	}

	msg := buildICMPError(origPkt, icmpType, icmpCode)

	ipHdr := &IPHeader{}
	InitializeIPHeader(ipHdr)
	ipHdr.Protocol = PROTO_ICMP
	ipHdr.SrcIP = srcIP
	ipHdr.DstIP = origHdr.SrcIP
	ipHdr.TotalLen = uint16(GetIPHeaderLen(ipHdr) + len(msg))

	pkt := append(SerializeIPHeader(ipHdr), msg...)

	emitInterfaceEvent(node, egress, "ICMP", "icmp_error_created", map[string]string{
		"destinationIp": IPUint32ToString(origHdr.SrcIP),
		"icmpType":      icmpMessageName(icmpType, icmpCode),
		"sourceIp":      IPUint32ToString(srcIP),
	})
	LogInfo("ICMP: Node %s: Reporting %s to %s via %s", get_node_name(node),
		icmpMessageName(icmpType, icmpCode), IPUint32ToString(origHdr.SrcIP),
		get_interface_name(egress))

	DemotePacketToLayer2(node, origHdr.SrcIP, get_interface_name(egress), pkt, len(pkt), ETHERTYPE_IP)
}

// ICMPErrorReport is what a sender learns when its packet could not be
// delivered: which router gave up, and why.
type ICMPErrorReport struct {
	Type       uint8
	Code       uint8
	Reason     string
	ReporterIP string
	// OriginalDstIP is read back out of the quoted header, so a sender with
	// several outstanding packets can tell which one failed.
	OriginalDstIP string
}

// handleICMPError processes an error report addressed to this node. The quoted
// datagram identifies the packet that failed.
func handleICMPError(node *Node, intf *Interface, ipHdr *IPHeader, payload []byte) {
	icmpType := payload[0]
	icmpCode := payload[1]
	reporter := IPUint32ToString(ipHdr.SrcIP)

	report := ICMPErrorReport{
		Type:       icmpType,
		Code:       icmpCode,
		Reason:     icmpMessageName(icmpType, icmpCode),
		ReporterIP: reporter,
	}

	// The quoted header starts 8 bytes into the message when one is present.
	if len(payload) >= 8+20 {
		if quoted, err := DeserializeIPHeader(payload[8:]); err == nil {
			report.OriginalDstIP = IPUint32ToString(quoted.DstIP)
		}
	}

	node.recordICMPError(report)

	LogWarn("ICMP: Node %s: %s reported %s for traffic to %s", get_node_name(node),
		reporter, report.Reason, report.OriginalDstIP)
	emitInterfaceEvent(node, intf, "ICMP", "icmp_error_received", map[string]string{
		"destinationIp": report.OriginalDstIP,
		"icmpType":      report.Reason,
		"sourceIp":      reporter,
		"ttl":           fmt.Sprintf("%d", ipHdr.TTL),
	})
}
