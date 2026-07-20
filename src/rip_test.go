package main

import (
	"os"
	"testing"
	"time"
)

func TestRIPConnectedRouteMetricAddsLinkCostOnce(t *testing.T) {
	response := buildRIPResponse([]L3Route{
		{
			Dest:   "192.0.2.0",
			Mask:   24,
			Metric: 0,
			Source: ROUTE_SOURCE_CONNECTED,
		},
	}, "eth0")

	if len(response.Entries) != 1 {
		t.Fatalf("expected one advertised route, got %d", len(response.Entries))
	}
	if metric := response.Entries[0].Metric; metric != 0 {
		t.Fatalf("connected route metric was incremented before receipt: got %d, want 0", metric)
	}

	receiver := &Node{}
	copy(receiver.node_name[:], "R2")
	init_node_nw_props(&receiver.node_nw_prop)

	recvIntf := &Interface{att_node: receiver}
	copy(recvIntf.if_name[:], "eth0")
	init_intf_nw_props(&recvIntf.intf_nw_props)
	receiver.intf[0] = recvIntf

	rip := InitRIPState(receiver)
	rip.enabled = true
	rip.ProcessRIPPacket(response, "198.51.100.1", recvIntf)

	learned := rip.routes["192.0.2.0/24"]
	if learned == nil {
		t.Fatal("connected neighbor route was not learned")
	}
	if learned.Metric != 1 {
		t.Fatalf("learned route metric = %d, want 1", learned.Metric)
	}

	installed := receiver.node_nw_prop.rt_table.LookupLPM(mustIP(t, "192.0.2.1"))
	if installed == nil {
		t.Fatal("connected neighbor route was not installed in the routing table")
	}
	if installed.Source != ROUTE_SOURCE_RIP || installed.Metric != 1 {
		t.Fatalf("installed route = %+v, want RIP route with metric 1", installed)
	}
}

func newRIPUnitNode(name string) *Node {
	node := &Node{}
	copy(node.node_name[:], name)
	init_node_nw_props(&node.node_nw_prop)
	node.rip_state = InitRIPState(node)
	return node
}

func enableRIPForUnitTest(rip *RIPState) <-chan struct{} {
	rip.mutex.Lock()
	defer rip.mutex.Unlock()
	rip.enabled = true
	rip.triggerUpdateCh = make(chan struct{}, 1)
	return rip.triggerUpdateCh
}

func expectTriggeredUpdate(t *testing.T, updates <-chan struct{}) {
	t.Helper()
	select {
	case <-updates:
	default:
		t.Fatal("RIP routing change did not request a triggered update")
	}
}

func expectNoTriggeredUpdate(t *testing.T, updates <-chan struct{}) {
	t.Helper()
	select {
	case <-updates:
		t.Fatal("unchanged RIP route requested a triggered update")
	default:
	}
}

func TestRIPTimeoutRemovesRouteFromFIBButRetainsPoison(t *testing.T) {
	node := newRIPUnitNode("R1")
	rip := node.rip_state
	updates := enableRIPForUnitTest(rip)

	route := &RIPRoute{
		Destination: "198.51.100.0",
		Mask:        24,
		NextHop:     "10.0.0.2",
		Interface:   "eth0",
		Metric:      2,
		LastUpdate:  time.Now().Add(-RIP_TIMEOUT - time.Second),
	}
	rip.routes["198.51.100.0/24"] = route
	if err := node.node_nw_prop.rt_table.AddRouteWithParams(
		route.Destination,
		route.Mask,
		route.NextHop,
		route.Interface,
		ROUTE_SOURCE_RIP,
		uint8(ROUTE_SOURCE_RIP),
		route.Metric,
	); err != nil {
		t.Fatalf("failed to install test RIP route: %v", err)
	}

	rip.CheckExpiredRoutes()

	if got := node.node_nw_prop.rt_table.LookupLPM(mustIP(t, "198.51.100.10")); got != nil {
		t.Fatalf("expired RIP route remained in the FIB: %+v", got)
	}
	if !route.IsExpired || route.Metric != RIP_MAX_METRIC {
		t.Fatalf("expired RIP state was not poisoned: %+v", route)
	}
	expectTriggeredUpdate(t, updates)

	advertisedPoison := false
	for _, advertised := range rip.routesForAdvertisement() {
		if advertised.Dest == route.Destination && advertised.Mask == route.Mask && advertised.Metric == RIP_MAX_METRIC {
			advertisedPoison = true
			break
		}
	}
	if !advertisedPoison {
		t.Fatal("expired route was not retained as a metric-16 advertisement")
	}

	rip.mutex.Lock()
	route.ExpiredAt = time.Now().Add(-RIP_GARBAGE_COLLECT - time.Second)
	rip.mutex.Unlock()
	rip.CheckExpiredRoutes()
	if _, exists := rip.routes["198.51.100.0/24"]; exists {
		t.Fatal("expired RIP route remained after garbage collection")
	}
}

func TestRIPRouteChangesRequestTriggeredUpdates(t *testing.T) {
	node := newRIPUnitNode("R1")
	rip := node.rip_state
	updates := enableRIPForUnitTest(rip)
	recvIntf := &Interface{att_node: node}
	copy(recvIntf.if_name[:], "eth0")

	packet := &RIPPacket{
		Command: RIP_COMMAND_RESPONSE,
		Version: RIP_VERSION,
		Entries: []RIPEntry{{
			AddressFamily: RIP_ADDRESS_FAMILY_IP,
			IPAddress:     mustIP(t, "203.0.113.0"),
			SubnetMask:    0xffffff00,
			Metric:        1,
		}},
	}

	rip.ProcessRIPPacket(packet, "10.0.0.2", recvIntf)
	expectTriggeredUpdate(t, updates)

	// A same-metric refresh only resets the timeout; it does not change the
	// routing table and therefore must not trigger another advertisement.
	rip.ProcessRIPPacket(packet, "10.0.0.2", recvIntf)
	expectNoTriggeredUpdate(t, updates)

	packet.Entries[0].Metric = 2
	rip.ProcessRIPPacket(packet, "10.0.0.2", recvIntf)
	expectTriggeredUpdate(t, updates)
}

func TestRIPUpdateIsReleasedAfterARPResolution(t *testing.T) {
	yamlData, err := os.ReadFile("../topologies/router_icmp.yaml")
	if err != nil {
		t.Fatalf("failed to read router topology: %v", err)
	}
	sim := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, func(SimulationEvent) {})
	if _, err := sim.LoadTopology(string(yamlData)); err != nil {
		t.Fatalf("failed to load router topology: %v", err)
	}
	defer cleanup_graph_resources(sim.graph)

	h1 := findGraphNode(sim.graph, "H1")
	r1 := findGraphNode(sim.graph, "R1")
	if h1 == nil || r1 == nil {
		t.Fatal("router topology is missing H1 or R1")
	}
	h1Intf := h1.intf[0]
	r1IP := get_remote_interface(h1Intf).GetIP().String()

	arp_table_clear(&h1.node_nw_prop.arp_table)
	var neighborAddress IpAddr
	if !set_ip_addr(&neighborAddress, r1IP) {
		t.Fatalf("failed to parse neighbor address %s", r1IP)
	}
	if arp_table_lookup(&h1.node_nw_prop.arp_table, &neighborAddress) != nil {
		t.Fatal("test requires an initial ARP miss")
	}

	// Enable receive processing without starting timers; the in-memory
	// transport delivers ARP and the queued RIP packet synchronously.
	r1.rip_state.mutex.Lock()
	r1.rip_state.enabled = true
	r1.rip_state.mutex.Unlock()
	defer func() {
		r1.rip_state.mutex.Lock()
		r1.rip_state.enabled = false
		r1.rip_state.mutex.Unlock()
	}()

	update := SerializeRIPPacket(&RIPPacket{
		Command: RIP_COMMAND_RESPONSE,
		Version: RIP_VERSION,
		Entries: []RIPEntry{{
			AddressFamily: RIP_ADDRESS_FAMILY_IP,
			IPAddress:     mustIP(t, "203.0.113.0"),
			SubnetMask:    0xffffff00,
			Metric:        1,
		}},
	})
	h1.rip_state.SendRIPPacket(update, h1Intf)

	if arp_table_lookup(&h1.node_nw_prop.arp_table, &neighborAddress) == nil {
		t.Fatal("ARP resolution did not complete")
	}
	learned := r1.node_nw_prop.rt_table.LookupLPM(mustIP(t, "203.0.113.10"))
	if learned == nil || learned.Source != ROUTE_SOURCE_RIP {
		t.Fatalf("RIP update was discarded during ARP resolution: %+v", learned)
	}
}
