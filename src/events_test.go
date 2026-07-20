package main

import (
	"encoding/binary"
	"os"
	"testing"
)

func TestGeneratedMACAddressesAreUnique(t *testing.T) {
	seen := make(map[MacAddr]struct{}, 1024)
	for i := 0; i < 1024; i++ {
		mac := generate_unique_mac_address()
		if mac[0]&0x01 != 0 {
			t.Fatalf("generated multicast MAC %s", mac.String())
		}
		if mac[0]&0x02 == 0 {
			t.Fatalf("generated universally administered MAC %s", mac.String())
		}
		if _, exists := seen[mac]; exists {
			t.Fatalf("duplicate generated MAC %s", mac.String())
		}
		seen[mac] = struct{}{}
	}
}

func TestFrameEventsUseNodeLocalInterfaceNames(t *testing.T) {
	yamlData, err := os.ReadFile("../ui/scenarios/inter-vlan-routing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	simulator := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, nil)
	if _, err = simulator.LoadTopology(string(yamlData)); err != nil {
		t.Fatal(err)
	}
	defer cleanup_graph_resources(simulator.graph)

	trace, err := simulator.TracePing("Sales-PC", "Engineering-PC")
	if err != nil {
		t.Fatal(err)
	}
	seenMACs := make(map[MacAddr]string)
	for _, node := range simulator.graph.node_list {
		if node == nil {
			continue
		}
		for _, intf := range node.intf {
			if intf == nil {
				continue
			}
			mac := *intf.GetMac()
			owner := get_node_name(node) + "/" + get_interface_name(intf)
			if previous, exists := seenMACs[mac]; exists {
				t.Fatalf("MAC %s is shared by %s and %s", mac.String(), previous, owner)
			}
			seenMACs[mac] = owner
		}
	}

	routeDecisions := 0
	icmpLinkEvents := 0
	for _, event := range trace.Events {
		if event.Action == "frame_dropped" && event.Fields["reason"] == "no_eligible_egress" {
			t.Fatal("a locally processed broadcast must not be reported as a failed frame")
		}
		if event.Action == "inter_vlan_route_selected" {
			routeDecisions++
			for _, field := range []string{"sourceIp", "destinationIp", "ingressInterface", "egressInterface", "ttlBefore", "ttlAfter"} {
				if event.Fields[field] == "" {
					t.Errorf("inter-VLAN route event lacks %s: %+v", field, event)
				}
			}
		}
		if event.Action == "frame_sent" && event.Protocol == "ICMP" {
			icmpLinkEvents++
			for _, field := range []string{"sourceMac", "destinationMac", "sourceIp", "destinationIp", "ttl", "icmpType", "flowId"} {
				if event.Fields[field] == "" {
					t.Errorf("ICMP link event lacks %s: %+v", field, event)
				}
			}
		}
		if event.Action != "frame_sent" && event.Action != "frame_received" {
			continue
		}
		if event.SourceInterface == "" || event.DestinationInterface == "" {
			t.Fatalf("%s event lacks directional interfaces: %+v", event.Action, event)
		}
		if event.Action == "frame_sent" {
			if event.Interface != event.SourceInterface || event.PeerInterface != event.DestinationInterface {
				t.Fatalf("sent event local/peer interfaces are inconsistent: %+v", event)
			}
			continue
		}
		if event.Interface != event.DestinationInterface || event.PeerInterface != event.SourceInterface {
			t.Fatalf("received event local/peer interfaces are inconsistent: %+v", event)
		}
	}
	if routeDecisions != 2 {
		t.Errorf("inter-VLAN route decisions = %d, want 2", routeDecisions)
	}
	if icmpLinkEvents != 4 {
		t.Errorf("ICMP link events = %d, want 4", icmpLinkEvents)
	}
}

func TestFrameEventFieldsExposePacketFlow(t *testing.T) {
	icmp := newICMPEchoRequest()
	ipHeader := &IPHeader{}
	InitializeIPHeader(ipHeader)
	ipHeader.Protocol = PROTO_ICMP
	ipHeader.SrcIP = binary.BigEndian.Uint32([]byte{192, 0, 2, 10})
	ipHeader.DstIP = binary.BigEndian.Uint32([]byte{198, 51, 100, 20})
	ipHeader.TotalLen = uint16(GetIPHeaderLen(ipHeader) + len(icmp))
	ipPacket := append(SerializeIPHeader(ipHeader), icmp...)

	sourceMAC := MacAddr{0x02, 0, 0, 0, 0, 1}
	destinationMAC := MacAddr{0x02, 0, 0, 0, 0, 2}
	frame := build_routed_frame(&destinationMAC, &sourceMAC, ipPacket)
	fields := frameEventFields(frame, nil)

	want := map[string]string{
		"sourceMac":      sourceMAC.String(),
		"destinationMac": destinationMAC.String(),
		"sourceIp":       "192.0.2.10",
		"destinationIp":  "198.51.100.20",
		"ttl":            "64",
		"icmpType":       "echo_request",
		"icmpIdentifier": "1",
		"icmpSequence":   "1",
		"flowId":         "icmp-1-1",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("%s = %q, want %q", key, fields[key], value)
		}
	}
}
