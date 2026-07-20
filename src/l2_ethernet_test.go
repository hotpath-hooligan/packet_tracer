package main

import "testing"

func newL3EthernetTestInterface() *Interface {
	intf := &Interface{}
	intf.intf_nw_props.is_ip_addr_config = true
	intf.intf_nw_props.mode = INTF_MODE_L3
	intf.intf_nw_props.mac_addr = MacAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	return intf
}

func TestL2FrameReceiveQualifiesMulticastOnL3Interface(t *testing.T) {
	intf := newL3EthernetTestInterface()
	tests := []struct {
		name string
		mac  MacAddr
		want bool
	}{
		{name: "interface unicast", mac: *intf.GetMac(), want: true},
		{name: "broadcast", mac: MacAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, want: true},
		{name: "IPv4 multicast", mac: MacAddr{0x01, 0x00, 0x5e, 0x00, 0x00, 0x09}, want: true},
		{name: "IPv6 multicast", mac: MacAddr{0x33, 0x33, 0x00, 0x00, 0x00, 0x01}, want: true},
		{name: "unrelated unicast", mac: MacAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := &EthernetHeader{dst_mac: test.mac}
			if got := l2_frame_recv_qualify_on_iface(intf, header); got != test.want {
				t.Fatalf("qualification = %t, want %t", got, test.want)
			}
		})
	}
}

func TestL2FrameReceiveDispatchesTaggedIPv4OnL3Interface(t *testing.T) {
	graph := &Graph{events: NewEventBus()}
	node := &Node{graph: graph}
	copy(node.node_name[:], "R1")
	node.node_nw_prop.rt_table = InitRoutingTable()

	intf := newL3EthernetTestInterface()
	intf.att_node = node
	copy(intf.if_name[:], "eth0")

	events := make([]SimulationEvent, 0)
	graph.SetEventSink(func(event SimulationEvent) {
		events = append(events, event)
	})

	ipHeader := &IPHeader{}
	InitializeIPHeader(ipHeader)
	ipHeader.Protocol = IPPROTO_UDP
	ipHeader.SrcIP = 0xc0000201 // 192.0.2.1
	ipHeader.DstIP = 0xcb007101 // 203.0.113.1
	ipHeader.TotalLen = 20
	ipPacket := SerializeIPHeader(ipHeader)

	frame := &EthernetFrame{
		header: EthernetHeader{
			dst_mac:   *intf.GetMac(),
			src_mac:   MacAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02},
			ethertype: ETHERTYPE_IP,
		},
		payload: ipPacket,
	}
	taggedFrame, err := add_vlan_tag(serialize_ethernet_frame(frame), 20, 0, 0)
	if err != nil {
		t.Fatalf("failed to tag frame: %v", err)
	}

	if got := layer_2_frame_recv(node, intf, taggedFrame, len(taggedFrame)); got != 0 {
		t.Fatalf("receive result = %d, want 0", got)
	}

	foundL3Drop := false
	for _, event := range events {
		if event.Action == "packet_dropped" && event.Fields["reason"] == "no_route" {
			foundL3Drop = true
		}
		if event.Action == "frame_dropped" && event.Fields["reason"] == "unsupported_ethertype" {
			t.Fatal("tagged IPv4 frame was rejected as an unsupported EtherType")
		}
	}
	if !foundL3Drop {
		t.Fatal("tagged IPv4 payload was not promoted to Layer 3")
	}
}

func TestL2FrameReceiveRejectsTruncatedVLANHeader(t *testing.T) {
	graph := &Graph{events: NewEventBus()}
	node := &Node{graph: graph}
	intf := newL3EthernetTestInterface()
	intf.att_node = node

	var dropReason string
	graph.SetEventSink(func(event SimulationEvent) {
		if event.Action == "frame_dropped" {
			dropReason = event.Fields["reason"]
		}
	})

	frame := make([]byte, ETHERNET_HDR_SIZE)
	copy(frame[0:6], intf.GetMac()[:])
	frame[12] = byte(ETHERTYPE_VLAN >> 8)
	frame[13] = byte(ETHERTYPE_VLAN & 0xff)

	if got := layer_2_frame_recv(node, intf, frame, len(frame)); got != -1 {
		t.Fatalf("receive result = %d, want -1", got)
	}
	if dropReason != "invalid_vlan_header" {
		t.Fatalf("drop reason = %q, want invalid_vlan_header", dropReason)
	}
}
