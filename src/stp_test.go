package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A triangle of bridges: every link is redundant, so without a spanning tree a
// single broadcast circulates forever.
const stpTriangleTopology = `
topology:
  name: "STP Triangle"
nodes:
  - name: SW1
    type: switch
    stp_priority: 4096
    interfaces:
      - {name: eth0, mode: access, vlan: 1}
      - {name: eth1, mode: access, vlan: 1}
      - {name: eth2, mode: access, vlan: 1}
  - name: SW2
    type: switch
    interfaces:
      - {name: eth0, mode: access, vlan: 1}
      - {name: eth1, mode: access, vlan: 1}
      - {name: eth2, mode: access, vlan: 1}
  - name: SW3
    type: switch
    interfaces:
      - {name: eth0, mode: access, vlan: 1}
      - {name: eth1, mode: access, vlan: 1}
  - name: H1
    type: host
    interfaces:
      - {name: eth0, ip: 10.0.1.1, mask: 24}
  - name: H2
    type: host
    interfaces:
      - {name: eth0, ip: 10.0.1.2, mask: 24}
links:
  - {from_node: SW1, from_interface: eth0, to_node: SW2, to_interface: eth0, cost: 1}
  - {from_node: SW2, from_interface: eth1, to_node: SW3, to_interface: eth0, cost: 1}
  - {from_node: SW3, from_interface: eth1, to_node: SW1, to_interface: eth1, cost: 1}
  - {from_node: H1, from_interface: eth0, to_node: SW1, to_interface: eth2, cost: 1}
  - {from_node: H2, from_interface: eth0, to_node: SW2, to_interface: eth2, cost: 1}
`

func loadSTPTopology(t *testing.T, yaml string) *Simulator {
	t.Helper()
	sim := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, func(e SimulationEvent) {})
	if _, err := sim.LoadTopology(yaml); err != nil {
		t.Fatalf("failed to load topology: %v", err)
	}
	t.Cleanup(func() { cleanup_graph_resources(sim.graph) })
	return sim
}

func portRoles(t *testing.T, node *Node) map[string]STPPortStatus {
	t.Helper()
	roles := make(map[string]STPPortStatus)
	for _, port := range node.stp_state.PortStatusSnapshot() {
		roles[port.Interface] = port
	}
	return roles
}

// The headline guarantee: a broadcast into a looped fabric terminates.
func TestSTPBreaksBroadcastLoop(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)
	h1 := findGraphNode(sim.graph, "H1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		send_arp_broadcast_request(h1, h1.intf[0], "10.0.1.2")
	}()
	<-done // would never fire before the spanning tree existed
}

func TestSTPElectsLowestPriorityBridgeAsRoot(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)

	sw1 := findGraphNode(sim.graph, "SW1")
	if !sw1.stp_state.IsRootBridge() {
		t.Errorf("SW1 has the lowest priority and must be the root, root is %s",
			sw1.stp_state.RootBridgeID())
	}
	expected := sw1.stp_state.BridgeIdentifier()
	for _, name := range []string{"SW2", "SW3"} {
		node := findGraphNode(sim.graph, name)
		if node.stp_state.RootBridgeID().Compare(expected) != 0 {
			t.Errorf("%s agreed on root %s, want %s", name, node.stp_state.RootBridgeID(), expected)
		}
		if node.stp_state.IsRootBridge() {
			t.Errorf("%s must not consider itself the root", name)
		}
	}
}

// Exactly one port in the triangle must block, and it must be on a non-root
// bridge: the root's ports are all designated.
func TestSTPBlocksExactlyOneRedundantPort(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)

	blocked := make([]string, 0)
	for _, name := range []string{"SW1", "SW2", "SW3"} {
		node := findGraphNode(sim.graph, name)
		for _, port := range node.stp_state.PortStatusSnapshot() {
			if port.State != "forwarding" {
				blocked = append(blocked, name+":"+port.Interface+"="+port.Role+"/"+port.State)
			}
		}
	}
	if len(blocked) != 1 {
		t.Errorf("expected exactly one blocked port in a triangle, got %d: %s",
			len(blocked), strings.Join(blocked, ", "))
	}
	if len(blocked) == 1 && !strings.Contains(blocked[0], "alternate/blocking") {
		t.Errorf("blocked port should hold the alternate role: %s", blocked[0])
	}
	if len(blocked) == 1 && strings.HasPrefix(blocked[0], "SW1:") {
		t.Errorf("the root bridge must keep every port designated, got %s", blocked[0])
	}
}

// Each non-root bridge has exactly one root port.
func TestSTPAssignsOneRootPortPerNonRootBridge(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)

	for _, name := range []string{"SW2", "SW3"} {
		node := findGraphNode(sim.graph, name)
		rootPorts := 0
		for _, port := range portRoles(t, node) {
			if port.Role == "root" {
				rootPorts++
			}
		}
		if rootPorts != 1 {
			t.Errorf("%s has %d root ports, want exactly 1", name, rootPorts)
		}
	}
	sw1 := findGraphNode(sim.graph, "SW1")
	for _, port := range portRoles(t, sw1) {
		if port.Role != "designated" {
			t.Errorf("root bridge port %s has role %s, want designated", port.Interface, port.Role)
		}
	}
}

// Hosts and routers do not bridge, so they stay out of the tree.
func TestSTPRunsOnBridgesOnly(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)

	for _, name := range []string{"SW1", "SW2", "SW3"} {
		if !findGraphNode(sim.graph, name).stp_state.IsEnabled() {
			t.Errorf("%s is a switch and must run the spanning tree", name)
		}
	}
	for _, name := range []string{"H1", "H2"} {
		if findGraphNode(sim.graph, name).stp_state.IsEnabled() {
			t.Errorf("%s is a host and must not run the spanning tree", name)
		}
	}
}

// Connectivity must survive the tree: H1 and H2 sit on different bridges and
// still have to reach each other over the surviving path.
func TestSTPPreservesConnectivity(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)
	result, err := sim.Ping("H1", "H2")
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if !result.ReplyReceived {
		t.Error("H1 could not reach H2 across the spanning tree")
	}
}

// ====== BPDU encoding ======

func TestBPDUSerializationRoundTrip(t *testing.T) {
	original := &BPDU{
		ProtocolID:   STP_PROTOCOL_ID,
		Version:      STP_VERSION_ID,
		Type:         STP_BPDU_TYPE_CONFIG,
		Flags:        STP_FLAG_TOPOLOGY_CHANGE,
		RootID:       BridgeID{Priority: 4096, Address: MacAddr{0xaa, 0xbb, 0xcc, 0x01, 0x02, 0x03}},
		RootPathCost: 19,
		BridgeID:     BridgeID{Priority: 32768, Address: MacAddr{0xaa, 0xbb, 0xcc, 0x04, 0x05, 0x06}},
		PortID:       makePortID(STP_DEFAULT_PORT_PRIORITY, 2),
		MessageAge:   durationToBPDUTime(2 * time.Second),
		MaxAge:       durationToBPDUTime(STP_MAX_AGE),
		HelloTime:    durationToBPDUTime(STP_HELLO_TIME),
		ForwardDelay: durationToBPDUTime(STP_FORWARD_DELAY),
	}

	encoded := serializeBPDU(original)
	if len(encoded) != STP_BPDU_SIZE {
		t.Fatalf("configuration BPDU encoded to %d bytes, want %d", len(encoded), STP_BPDU_SIZE)
	}

	decoded, err := deserializeBPDU(encoded)
	if err != nil {
		t.Fatalf("failed to decode a BPDU we produced: %v", err)
	}
	if *decoded != *original {
		t.Errorf("round trip changed the BPDU:\n got %+v\nwant %+v", *decoded, *original)
	}
}

func TestBPDUFrameEncapsulation(t *testing.T) {
	src := MacAddr{0xaa, 0xbb, 0xcc, 0x01, 0x02, 0x03}
	payload := serializeBPDU(&BPDU{ProtocolID: STP_PROTOCOL_ID, MaxAge: durationToBPDUTime(STP_MAX_AGE)})
	frame := buildSTPFrame(src, payload)

	var destination MacAddr
	copy(destination[:], frame[0:6])
	if !isSTPBridgeGroupMAC(&destination) {
		t.Errorf("BPDU sent to %s, want the bridge group address %s",
			destination.String(), stpBridgeGroupMAC.String())
	}
	// 802.3 framing: the field after the addresses is a length, not an
	// EtherType, and it covers the LLC header plus the BPDU.
	length := int(frame[12])<<8 | int(frame[13])
	if want := STP_LLC_SIZE + len(payload); length != want {
		t.Errorf("length field is %d, want %d", length, want)
	}
	if frame[14] != STP_LLC_DSAP || frame[15] != STP_LLC_SSAP || frame[16] != STP_LLC_CONTROL {
		t.Errorf("LLC header is %02x %02x %02x, want 42 42 03", frame[14], frame[15], frame[16])
	}
	if frameProtocol(frame) != "STP" {
		t.Errorf("frame classified as %s, want STP", frameProtocol(frame))
	}

	decoded, err := parseSTPFrame(frame)
	if err != nil {
		t.Fatalf("failed to parse the frame we built: %v", err)
	}
	if len(decoded) != len(payload) {
		t.Errorf("parsed payload is %d bytes, want %d", len(decoded), len(payload))
	}
}

func TestBPDURejectsMalformedInput(t *testing.T) {
	valid := serializeBPDU(&BPDU{
		ProtocolID: STP_PROTOCOL_ID,
		MaxAge:     durationToBPDUTime(STP_MAX_AGE),
		MessageAge: durationToBPDUTime(time.Second),
	})

	cases := []struct {
		name    string
		corrupt func([]byte) []byte
	}{
		{"truncated", func(b []byte) []byte { return b[:STP_BPDU_SIZE-4] }},
		{"empty", func(b []byte) []byte { return nil }},
		{"wrong protocol id", func(b []byte) []byte { b[0] = 0xff; return b }},
		{"unknown bpdu type", func(b []byte) []byte { b[3] = 0x42; return b }},
		{"message age past max age", func(b []byte) []byte {
			copy(b[27:29], b[29:31])
			return b
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := testCase.corrupt(append([]byte(nil), valid...))
			if _, err := deserializeBPDU(input); err == nil {
				t.Error("expected the malformed BPDU to be rejected")
			}
		})
	}
}

// ====== Identifier ordering ======

func TestBridgeIDOrdersByPriorityThenAddress(t *testing.T) {
	low := BridgeID{Priority: 4096, Address: MacAddr{0xff, 0, 0, 0, 0, 0}}
	high := BridgeID{Priority: 32768, Address: MacAddr{0x00, 0, 0, 0, 0, 0}}
	if low.Compare(high) >= 0 {
		t.Error("priority must outrank the address when choosing a root bridge")
	}

	sameLow := BridgeID{Priority: 32768, Address: MacAddr{0xaa, 0, 0, 0, 0, 1}}
	sameHigh := BridgeID{Priority: 32768, Address: MacAddr{0xaa, 0, 0, 0, 0, 2}}
	if sameLow.Compare(sameHigh) >= 0 {
		t.Error("with equal priorities the lower address must win")
	}
	if sameLow.Compare(sameLow) != 0 {
		t.Error("a bridge identifier must compare equal to itself")
	}
}

// ====== Re-election ======

// Lowering a bridge's priority must move the root and reconverge the tree.
func TestSTPReelectsRootOnPriorityChange(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)

	sw1 := findGraphNode(sim.graph, "SW1")
	sw3 := findGraphNode(sim.graph, "SW3")
	if !sw1.stp_state.IsRootBridge() {
		t.Fatalf("expected SW1 to start as the root")
	}

	state, err := sim.SetSTPPriority("SW3", 0)
	if err != nil {
		t.Fatalf("failed to change the bridge priority: %v", err)
	}
	if state.STP.Priority != 0 {
		t.Errorf("snapshot priority = %d, want 0", state.STP.Priority)
	}

	if !sw3.stp_state.IsRootBridge() {
		t.Errorf("SW3 has priority 0 and must become the root, root is %s",
			sw3.stp_state.RootBridgeID())
	}
	expected := sw3.stp_state.BridgeIdentifier()
	for _, name := range []string{"SW1", "SW2"} {
		node := findGraphNode(sim.graph, name)
		if node.stp_state.RootBridgeID().Compare(expected) != 0 {
			t.Errorf("%s still believes the root is %s, want %s",
				name, node.stp_state.RootBridgeID(), expected)
		}
	}

	// The tree must still block exactly one port and still carry traffic.
	blocked := 0
	for _, name := range []string{"SW1", "SW2", "SW3"} {
		for _, port := range findGraphNode(sim.graph, name).stp_state.PortStatusSnapshot() {
			if port.State != "forwarding" {
				blocked++
			}
		}
	}
	if blocked != 1 {
		t.Errorf("after re-election %d ports are blocked, want exactly 1", blocked)
	}
	result, err := sim.Ping("H1", "H2")
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if !result.ReplyReceived {
		t.Error("connectivity was lost after the root bridge changed")
	}
}

// A bridge with the spanning tree switched off must forward on every port.
func TestSTPDisabledLeavesPortsForwarding(t *testing.T) {
	sim := loadSTPTopology(t, stpTriangleTopology)

	if _, err := sim.SetSTP("SW2", false); err != nil {
		t.Fatalf("failed to disable the spanning tree: %v", err)
	}
	sw2 := findGraphNode(sim.graph, "SW2")
	if sw2.stp_state.IsEnabled() {
		t.Error("SW2 should report the spanning tree as disabled")
	}
	for _, intf := range sw2.intf {
		if intf == nil {
			continue
		}
		if !stp_port_can_forward(sw2, intf) {
			t.Errorf("port %s must forward once the spanning tree is off", get_interface_name(intf))
		}
	}
}

// The shipped example topology must converge and carry traffic.
func TestSTPLoopTopologyFileConverges(t *testing.T) {
	yamlData, err := os.ReadFile("../topologies/stp-loop.yaml")
	if err != nil {
		t.Fatalf("failed to read the spanning tree topology: %v", err)
	}
	sim := loadSTPTopology(t, string(yamlData))

	sw1 := findGraphNode(sim.graph, "SW1")
	if !sw1.stp_state.IsRootBridge() {
		t.Errorf("SW1 has the best priority and must be the root, root is %s",
			sw1.stp_state.RootBridgeID())
	}

	blocked := 0
	for _, name := range []string{"SW1", "SW2", "SW3"} {
		for _, port := range findGraphNode(sim.graph, name).stp_state.PortStatusSnapshot() {
			if port.State != "forwarding" {
				blocked++
			}
		}
	}
	if blocked != 1 {
		t.Errorf("%d ports are blocked, want exactly 1 to break the single loop", blocked)
	}

	result, err := sim.Ping("H1", "H2")
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if !result.ReplyReceived {
		t.Error("H1 could not reach H2 over the spanning tree")
	}
}
