package main

import (
	"os"
	"testing"
)

func TestRouterForwardsICMPBetweenNetworks(t *testing.T) {
	yamlData, err := os.ReadFile("../topologies/router_icmp.yaml")
	if err != nil {
		t.Fatalf("failed to read router topology: %v", err)
	}

	events := make([]SimulationEvent, 0)
	simulator := NewSimulator(
		func() FrameTransport { return NewMemoryTransport() },
		func(event SimulationEvent) { events = append(events, event) },
	)
	state, err := simulator.LoadTopology(string(yamlData))
	if err != nil {
		t.Fatalf("failed to load router topology: %v", err)
	}
	defer cleanup_graph_resources(simulator.graph)

	types := make(map[string]DeviceType)
	for _, node := range state.Nodes {
		types[node.Name] = node.Type
	}
	if types["H1"] != DEVICE_TYPE_HOST || types["R1"] != DEVICE_TYPE_ROUTER || types["H2"] != DEVICE_TYPE_HOST {
		t.Fatalf("unexpected device types: %#v", types)
	}

	result, err := simulator.Ping("H1", "H2")
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if !result.ReplyReceived {
		t.Fatal("H1 did not receive an ICMP echo reply from H2 through R1")
	}

	forwardedWithDecrementedTTL := false
	for _, event := range events {
		if event.Node == "R1" && event.Action == "packet_forwarding_started" && event.Fields["ttl"] == "63" {
			forwardedWithDecrementedTTL = true
			break
		}
	}
	if !forwardedWithDecrementedTTL {
		t.Fatal("R1 did not emit a forwarding event with a decremented TTL")
	}

	// LLDP may continue running in the background, but selecting Ping must
	// produce a Ping-only snapshot rather than folding discovery into it.
	for _, nodeName := range []string{"H1", "R1"} {
		if _, err := simulator.SetLLDP(nodeName, true); err != nil {
			t.Fatalf("failed to enable LLDP on %s: %v", nodeName, err)
		}
	}

	trace, err := simulator.TracePing("H1", "H2")
	if err != nil {
		t.Fatalf("ping trace failed: %v", err)
	}
	if !trace.ReplyReceived || len(trace.Events) == 0 {
		t.Fatalf("ping trace did not return a successful finite snapshot: %#v", trace)
	}
	for _, event := range trace.Events {
		switch event.Protocol {
		case "ARP", "ICMP", "ETHERNET":
		default:
			t.Fatalf("ping trace captured unrelated %s event while LLDP was enabled: %#v", event.Protocol, event)
		}
	}
}

func TestTracerouteDiscoversRouterAndDestination(t *testing.T) {
	yamlData, err := os.ReadFile("../topologies/router_icmp.yaml")
	if err != nil {
		t.Fatalf("failed to read router topology: %v", err)
	}

	simulator := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, nil)
	if _, err := simulator.LoadTopology(string(yamlData)); err != nil {
		t.Fatalf("failed to load router topology: %v", err)
	}
	defer cleanup_graph_resources(simulator.graph)

	trace, err := simulator.TraceTraceroute("H1", "H2", DefaultTracerouteMaxHops)
	if err != nil {
		t.Fatalf("traceroute failed: %v", err)
	}
	if !trace.Reached {
		t.Fatalf("traceroute did not reach H2: %#v", trace.Hops)
	}
	if len(trace.Hops) != 2 {
		t.Fatalf("traceroute returned %d hops, want 2: %#v", len(trace.Hops), trace.Hops)
	}
	first := trace.Hops[0]
	if first.TTL != 1 || first.Address != "10.0.1.1" || first.Reached || first.TimedOut || first.Reason != "time_exceeded" {
		t.Fatalf("first hop = %#v, want R1 Time Exceeded response", first)
	}
	second := trace.Hops[1]
	if second.TTL != 2 || second.Address != "10.0.2.2" || !second.Reached || second.TimedOut || second.Reason != "echo_reply" {
		t.Fatalf("second hop = %#v, want reached destination", second)
	}

	wantActions := map[string]bool{
		"traceroute_probe_started":       false,
		"packet_dropped":                 false,
		"icmp_error_created":             false,
		"icmp_error_received":            false,
		"traceroute_hop_discovered":      false,
		"traceroute_destination_reached": false,
	}
	for _, event := range trace.Events {
		if _, wanted := wantActions[event.Action]; wanted {
			wantActions[event.Action] = true
		}
	}
	for action, found := range wantActions {
		if !found {
			t.Errorf("traceroute trace lacks %s event", action)
		}
	}
}

func TestTracerouteHonorsMaximumHops(t *testing.T) {
	yamlData, err := os.ReadFile("../topologies/router_icmp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	simulator := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, nil)
	if _, err := simulator.LoadTopology(string(yamlData)); err != nil {
		t.Fatal(err)
	}
	defer cleanup_graph_resources(simulator.graph)

	result, err := simulator.Traceroute("H1", "H2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reached || len(result.Hops) != 1 || result.Hops[0].Address != "10.0.1.1" {
		t.Fatalf("one-hop traceroute did not stop at its limit: %#v", result)
	}
	if _, err := simulator.Traceroute("H1", "H2", 0); err == nil {
		t.Fatal("traceroute accepted a zero maximum hop count")
	}
}
