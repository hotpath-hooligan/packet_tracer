package main

import (
	"encoding/binary"
	"fmt"
	"testing"
)

type recordingFrameTransport struct {
	interfaces []*Interface
	frames     [][]byte
}

func (transport *recordingFrameTransport) Name() string         { return "recording" }
func (transport *recordingFrameTransport) Register(*Node) error { return nil }
func (transport *recordingFrameTransport) Start(*Graph) error   { return nil }
func (transport *recordingFrameTransport) Stop()                {}
func (transport *recordingFrameTransport) Close() error         { return nil }
func (transport *recordingFrameTransport) Send(intf *Interface, frame []byte) error {
	transport.interfaces = append(transport.interfaces, intf)
	transport.frames = append(transport.frames, append([]byte(nil), frame...))
	return nil
}

func TestInterVLANRoutingUsesRouteAndDestinationMAC(t *testing.T) {
	tests := []struct {
		name          string
		destinationIP string
		nextHopIP     string
		addRoute      bool
	}{
		{
			name:          "connected host behind second access port",
			destinationIP: "10.2.2.20",
			nextHopIP:     "10.2.2.20",
		},
		{
			name:          "off-net destination through SVI next hop",
			destinationIP: "203.0.113.9",
			nextHopIP:     "10.2.2.254",
			addRoute:      true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node, ingress, firstVLAN20Port, secondVLAN20Port, transport := newInterVLANRoutingFixture(t)

			if test.addRoute {
				if err := node.node_nw_prop.rt_table.AddRoute(
					"203.0.113.0", 24, test.nextHopIP, "vlan20",
				); err != nil {
					t.Fatalf("add off-net route: %v", err)
				}
			}

			nextHopAddr := IpAddr{}
			if !set_ip_addr(&nextHopAddr, test.nextHopIP) {
				t.Fatalf("parse next-hop IP %s", test.nextHopIP)
			}
			nextHopMAC := MacAddr{0x02, 0, 0, 0, 0x20, 0xfe}
			if !arp_table_add_entry(&node.node_nw_prop.arp_table, &nextHopAddr, &nextHopMAC, get_interface_name(secondVLAN20Port)) {
				t.Fatal("seed next-hop ARP entry")
			}
			if !mac_table_add_or_update_vlan(
				&node.node_nw_prop.mac_table, &nextHopMAC, 20, get_interface_name(secondVLAN20Port),
			) {
				t.Fatal("seed destination MAC entry")
			}

			frame := interVLANTestFrame(t, ingress, test.destinationIP)
			l2_switch_recv_frame(node, ingress, frame, len(frame))

			if len(transport.interfaces) != 1 {
				t.Fatalf("sent %d frames, want exactly one", len(transport.interfaces))
			}
			if got := transport.interfaces[0]; got != secondVLAN20Port {
				t.Fatalf("egress = %s, want MAC-table port %s (first VLAN 20 port was %s)",
					get_interface_name(got), get_interface_name(secondVLAN20Port), get_interface_name(firstVLAN20Port))
			}

			sent := transport.frames[0]
			if len(sent) < ETHERNET_HDR_SIZE+20 {
				t.Fatalf("forwarded frame is too short: %d", len(sent))
			}
			var gotDestinationMAC MacAddr
			copy(gotDestinationMAC[:], sent[:6])
			if gotDestinationMAC != nextHopMAC {
				t.Fatalf("destination MAC = %s, want next-hop MAC %s", gotDestinationMAC.String(), nextHopMAC.String())
			}
			if gotTTL := sent[ETHERNET_HDR_SIZE+8]; gotTTL != 63 {
				t.Fatalf("forwarded TTL = %d, want 63", gotTTL)
			}
			if gotDestinationIP := binary.BigEndian.Uint32(sent[ETHERNET_HDR_SIZE+16 : ETHERNET_HDR_SIZE+20]); gotDestinationIP != mustIP(t, test.destinationIP) {
				t.Fatalf("forwarded destination IP = %s, want %s", IPUint32ToString(gotDestinationIP), test.destinationIP)
			}
		})
	}
}

func newInterVLANRoutingFixture(t *testing.T) (*Node, *Interface, *Interface, *Interface, *recordingFrameTransport) {
	t.Helper()

	transport := &recordingFrameTransport{}
	graph := create_new_graph_with_transport("inter-vlan routing test", transport)
	node := &Node{graph: graph}
	copy(node.node_name[:], "SW1")
	init_node_nw_props(&node.node_nw_prop)

	ingress := addInterVLANTestPort(t, node, "eth0/0", 10)
	firstVLAN20Port := addInterVLANTestPort(t, node, "eth0/1", 20)
	secondVLAN20Port := addInterVLANTestPort(t, node, "eth0/2", 20)
	ingress.intf_nw_props.mac_addr = MacAddr{0x02, 0, 0, 0, 0x10, 1}
	firstVLAN20Port.intf_nw_props.mac_addr = MacAddr{0x02, 0, 0, 0, 0x20, 1}
	secondVLAN20Port.intf_nw_props.mac_addr = MacAddr{0x02, 0, 0, 0, 0x20, 2}

	if !node.AddVlanInterface(10, "10.1.1.1", 24) {
		t.Fatal("add VLAN 10 SVI")
	}
	if !node.AddVlanInterface(20, "10.2.2.1", 24) {
		t.Fatal("add VLAN 20 SVI")
	}

	return node, ingress, firstVLAN20Port, secondVLAN20Port, transport
}

func addInterVLANTestPort(t *testing.T, node *Node, name string, vlanID uint16) *Interface {
	t.Helper()

	intf, err := create_node_interface(node, name)
	if err != nil {
		t.Fatalf("create switch interface %s: %v", name, err)
	}
	peer := &Node{graph: node.graph}
	copy(peer.node_name[:], fmt.Sprintf("peer-%s", name))
	_, err = create_node_interface(peer, "eth0")
	if err != nil {
		t.Fatalf("create peer for %s: %v", name, err)
	}
	if err := insert_link_between_two_nodes(node, peer, name, "eth0", 1); err != nil {
		t.Fatalf("link %s: %v", name, err)
	}
	if !intf.SetAccessVLAN(vlanID) {
		t.Fatalf("set %s to access VLAN %d", name, vlanID)
	}
	return intf
}

func interVLANTestFrame(t *testing.T, ingress *Interface, destinationIP string) []byte {
	t.Helper()

	hdr := &IPHeader{}
	InitializeIPHeader(hdr)
	hdr.TotalLen = 20
	hdr.Protocol = PROTO_ICMP
	hdr.SrcIP = mustIP(t, "10.1.1.10")
	hdr.DstIP = mustIP(t, destinationIP)

	frame := make([]byte, ETHERNET_HDR_SIZE, ETHERNET_HDR_SIZE+20)
	copy(frame[:6], ingress.GetMac()[:])
	sourceMAC := MacAddr{0x02, 0, 0, 0, 0x10, 10}
	copy(frame[6:12], sourceMAC[:])
	binary.BigEndian.PutUint16(frame[12:14], ETHERTYPE_IP)
	return append(frame, SerializeIPHeader(hdr)...)
}
