package main

import (
	"bytes"
	"testing"
)

func TestMatchingSubnetInterfaceIgnoresZeroLengthPrefix(t *testing.T) {
	node := &Node{}
	defaultIntf := &Interface{}
	defaultIntf.intf_nw_props.is_ip_addr_config = true
	defaultIntf.intf_nw_props.ip_addr = IpAddr{192, 0, 2, 1}
	defaultIntf.intf_nw_props.mask = 0
	node.intf[0] = defaultIntf

	localIntf := &Interface{}
	localIntf.intf_nw_props.is_ip_addr_config = true
	localIntf.intf_nw_props.ip_addr = IpAddr{10, 0, 0, 1}
	localIntf.intf_nw_props.mask = 24
	node.intf[1] = localIntf

	if got := node_get_matching_subnet_interface(node, "198.51.100.10"); got != nil {
		t.Fatalf("/0 interface matched an arbitrary destination: %p", got)
	}
	if got := node_get_matching_subnet_interface(node, "10.0.0.25"); got != localIntf {
		t.Fatalf("matching /24 interface = %p, want %p", got, localIntf)
	}
}

func TestRouteBetweenVLANsDoesNotMutateReceiveBuffer(t *testing.T) {
	node := &Node{
		node_nw_prop: NodeNwProp{
			rt_table: InitRoutingTable(),
			vlan_interfaces: map[uint16]*VlanInterface{
				10: {vlan_id: 10, ip_addr: IpAddr{10, 1, 1, 1}, mask: 24},
				20: {vlan_id: 20, ip_addr: IpAddr{10, 2, 2, 1}, mask: 24},
			},
		},
	}
	if err := node.node_nw_prop.rt_table.AddRouteWithParams(
		"10.2.2.0", 24, "", "vlan20", ROUTE_SOURCE_CONNECTED, 0, 0,
	); err != nil {
		t.Fatalf("add connected route: %v", err)
	}

	hdr := &IPHeader{}
	InitializeIPHeader(hdr)
	hdr.TotalLen = 20
	hdr.Protocol = PROTO_ICMP
	hdr.SrcIP = 0x0a01010a
	hdr.DstIP = 0x0a020214

	frame := make([]byte, ETHERNET_HDR_SIZE)
	frame[12] = byte(ETHERTYPE_IP >> 8)
	frame[13] = byte(ETHERTYPE_IP & 0xff)
	frame = append(frame, SerializeIPHeader(hdr)...)
	original := append([]byte(nil), frame...)

	route_between_vlans(node, &Interface{}, frame, len(frame), 10)

	if !bytes.Equal(frame, original) {
		t.Fatal("inter-VLAN routing mutated the shared receive buffer")
	}

	entry := node.node_nw_prop.arp_table.head
	if entry == nil || entry.pending_list == nil {
		t.Fatal("routed packet was not queued for forwarding")
	}
	forwarded := entry.pending_list.pkt[ETHERNET_HDR_SIZE:]
	if forwarded[8] != hdr.TTL-1 {
		t.Fatalf("forwarded TTL = %d, want %d", forwarded[8], hdr.TTL-1)
	}
	if !validIPHeaderChecksum(forwarded[:20]) {
		t.Fatal("forwarded copy has an invalid IP header checksum")
	}
}
