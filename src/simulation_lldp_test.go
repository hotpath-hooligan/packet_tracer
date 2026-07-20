package main

import (
	"sync"
	"testing"
)

func TestSimulatorSetLLDPDiscoversAndRemovesNeighbors(t *testing.T) {
	const yamlData = `
topology:
  name: LLDP simulator test
nodes:
  - name: R1
    type: router
    interfaces:
      - name: eth0
        ip: 10.0.0.1
        mask: 24
  - name: R2
    type: router
    interfaces:
      - name: eth0
        ip: 10.0.0.2
        mask: 24
links:
  - from_node: R1
    from_interface: eth0
    to_node: R2
    to_interface: eth0
    cost: 1
`

	var eventsMu sync.Mutex
	events := make([]SimulationEvent, 0)
	simulator := NewSimulator(
		func() FrameTransport { return NewMemoryTransport() },
		func(event SimulationEvent) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		},
	)
	topology, err := simulator.LoadTopology(yamlData)
	if err != nil {
		t.Fatalf("failed to load topology: %v", err)
	}
	defer cleanup_graph_resources(simulator.graph)

	for _, node := range topology.Nodes {
		if node.LLDPEnabled {
			t.Fatalf("LLDP unexpectedly enabled on %s", node.Name)
		}
	}

	r1, err := simulator.SetLLDP("R1", true)
	if err != nil {
		t.Fatalf("failed to enable LLDP on R1: %v", err)
	}
	if !r1.LLDPEnabled {
		t.Fatal("R1 did not report LLDP enabled")
	}
	if len(r1.LLDP) != 0 {
		t.Fatalf("R1 learned neighbors before its peer was enabled: %#v", r1.LLDP)
	}

	r2, err := simulator.SetLLDP("R2", true)
	if err != nil {
		t.Fatalf("failed to enable LLDP on R2: %v", err)
	}
	assertLLDPNeighbor(t, r2, "R1", "eth0", "eth0")

	r1, err = simulator.GetNodeState("R1")
	if err != nil {
		t.Fatalf("failed to read R1 state: %v", err)
	}
	assertLLDPNeighbor(t, r1, "R2", "eth0", "eth0")

	// Forget the initial discovery so the explicit round verifies that it does
	// not recursively add discovery-response advertisements to its snapshot.
	for _, nodeName := range []string{"R1", "R2"} {
		node := findGraphNode(simulator.graph, nodeName)
		node.lldp_state.mutex.Lock()
		node.lldp_state.neighbors = make(map[string]*LLDPNeighbor)
		node.lldp_state.mutex.Unlock()
	}
	trace, err := simulator.TraceLLDP()
	if err != nil {
		t.Fatalf("failed to trace LLDP: %v", err)
	}
	if len(trace.Advertisers) != 2 {
		t.Fatalf("unexpected LLDP advertisers: %#v", trace.Advertisers)
	}
	advertisements := 0
	for _, event := range trace.Events {
		if event.Protocol != "LLDP" {
			t.Fatalf("LLDP trace captured unrelated event: %#v", event)
		}
		if event.Action == "lldp_advertisement_created" {
			advertisements++
		}
	}
	if advertisements != 2 {
		t.Fatalf("LLDP trace captured %d advertisements, want one per connected interface on enabled devices: %#v", advertisements, trace.Events)
	}
	r1, err = simulator.GetNodeState("R1")
	if err != nil {
		t.Fatalf("failed to read R1 state after LLDP trace: %v", err)
	}
	assertLLDPNeighbor(t, r1, "R2", "eth0", "eth0")

	r2, err = simulator.SetLLDP("R2", false)
	if err != nil {
		t.Fatalf("failed to disable LLDP on R2: %v", err)
	}
	if r2.LLDPEnabled || len(r2.LLDP) != 0 {
		t.Fatalf("R2 retained LLDP state after disable: %#v", r2)
	}

	r1, err = simulator.GetNodeState("R1")
	if err != nil {
		t.Fatalf("failed to read R1 state after R2 disable: %v", err)
	}
	if !r1.LLDPEnabled {
		t.Fatal("disabling R2 also disabled R1")
	}
	if len(r1.LLDP) != 0 {
		t.Fatalf("R1 retained the withdrawn R2 neighbor: %#v", r1.LLDP)
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, action := range []string{"lldp_enabled", "lldp_neighbor_discovered", "lldp_neighbor_removed", "lldp_disabled"} {
		if !containsSimulationAction(events, action) {
			t.Errorf("missing %s event", action)
		}
	}
}

func TestSimulatorSetLLDPRejectsUnknownNode(t *testing.T) {
	const yamlData = `
topology:
  name: LLDP missing node test
nodes:
  - name: R1
    interfaces:
      - name: eth0
        ip: 10.0.0.1
        mask: 24
`
	simulator := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, nil)
	if _, err := simulator.LoadTopology(yamlData); err != nil {
		t.Fatalf("failed to load topology: %v", err)
	}
	defer cleanup_graph_resources(simulator.graph)

	if _, err := simulator.SetLLDP("missing", true); err == nil {
		t.Fatal("expected enabling LLDP on an unknown node to fail")
	}
	if _, err := simulator.TraceLLDP(); err == nil {
		t.Fatal("expected an LLDP trace with no enabled devices to fail")
	}
}

func assertLLDPNeighbor(t *testing.T, state *NodeState, systemName, port, localInterface string) {
	t.Helper()
	if !state.LLDPEnabled {
		t.Fatalf("%s did not report LLDP enabled", state.Name)
	}
	for _, neighbor := range state.LLDP {
		if neighbor.SystemName == systemName && neighbor.Port == port && neighbor.LocalInterface == localInterface {
			return
		}
	}
	t.Fatalf("%s did not learn %s on %s: %#v", state.Name, systemName, localInterface, state.LLDP)
}

func containsSimulationAction(events []SimulationEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}
