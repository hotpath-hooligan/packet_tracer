package main

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func loadRouterTopology(t *testing.T) *Simulator {
	t.Helper()
	yamlData, err := os.ReadFile("../topologies/router_icmp.yaml")
	if err != nil {
		t.Fatalf("failed to read the router topology: %v", err)
	}
	sim := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, func(e SimulationEvent) {})
	if _, err := sim.LoadTopology(string(yamlData)); err != nil {
		t.Fatalf("failed to load the router topology: %v", err)
	}
	t.Cleanup(func() { cleanup_graph_resources(sim.graph) })
	return sim
}

func lookupARP(node *Node, ip string) *MacAddr {
	var address IpAddr
	if !set_ip_addr(&address, ip) {
		return nil
	}
	return arp_table_lookup(&node.node_nw_prop.arp_table, &address)
}

// RFC 826: a node that answers an ARP request must also record the requester's
// binding, which it can read straight out of the request.
func TestARPResponderLearnsSenderBinding(t *testing.T) {
	sim := loadRouterTopology(t)
	h1 := findGraphNode(sim.graph, "H1")
	r1 := findGraphNode(sim.graph, "R1")

	h1Intf := h1.intf[0]
	h1IP := h1Intf.GetIP().String()
	r1IP := get_remote_interface(h1Intf).GetIP().String()

	if lookupARP(r1, h1IP) != nil {
		t.Fatal("R1 should not know H1 before any ARP traffic")
	}

	send_arp_broadcast_request(h1, h1Intf, r1IP)

	learned := lookupARP(r1, h1IP)
	if learned == nil {
		t.Fatalf("R1 answered the request but did not learn %s", h1IP)
	}
	if *learned != *h1Intf.GetMac() {
		t.Errorf("R1 learned %s for %s, want %s", learned, h1IP, h1Intf.GetMac())
	}
	// The requester still learns from the reply, as it always did.
	if lookupARP(h1, r1IP) == nil {
		t.Errorf("H1 did not learn %s from the reply", r1IP)
	}
}

// A request that is not addressed to this node may refresh an entry it already
// holds, but must not create one: that would let any broadcast populate the
// cache of every node on the segment.
func TestARPRequestForAnotherNodeDoesNotCreateEntry(t *testing.T) {
	sim := loadRouterTopology(t)
	h1 := findGraphNode(sim.graph, "H1")
	r1 := findGraphNode(sim.graph, "R1")

	h1Intf := h1.intf[0]
	h1IP := h1Intf.GetIP().String()

	// H1 asks for an address nobody on the segment owns.
	send_arp_broadcast_request(h1, h1Intf, "10.0.1.200")

	if learned := lookupARP(r1, h1IP); learned != nil {
		t.Errorf("R1 installed an entry for %s from a request addressed elsewhere", h1IP)
	}
}

// Once a binding exists, a request from that sender refreshes it in place.
func TestARPRequestRefreshesExistingBinding(t *testing.T) {
	sim := loadRouterTopology(t)
	h1 := findGraphNode(sim.graph, "H1")
	r1 := findGraphNode(sim.graph, "R1")

	h1Intf := h1.intf[0]
	h1IP := h1Intf.GetIP().String()
	r1IP := get_remote_interface(h1Intf).GetIP().String()

	send_arp_broadcast_request(h1, h1Intf, r1IP)
	if lookupARP(r1, h1IP) == nil {
		t.Fatal("R1 should have learned H1 from the first request")
	}

	// H1 moves to a new MAC and asks about an unrelated address.
	updated := MacAddr{0xaa, 0xbb, 0xcc, 0xde, 0xad, 0xbe}
	h1Intf.intf_nw_props.mac_addr = updated
	send_arp_broadcast_request(h1, h1Intf, "10.0.1.200")

	learned := lookupARP(r1, h1IP)
	if learned == nil {
		t.Fatal("R1 lost the binding for H1")
	}
	if *learned != updated {
		t.Errorf("R1 holds %s for %s, want the refreshed address %s", learned, h1IP, updated.String())
	}
}

func TestARPResolveOrQueueIsAtomicWithLearning(t *testing.T) {
	var target IpAddr
	if !set_ip_addr(&target, "192.0.2.20") {
		t.Fatal("failed to parse test address")
	}
	resolvedMAC := MacAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x20}
	pkt := make([]byte, ETHERNET_HDR_SIZE+20)

	for iteration := 0; iteration < 200; iteration++ {
		node := &Node{graph: &Graph{events: NewEventBus()}}
		init_arp_table(&node.node_nw_prop.arp_table)
		iif := &Interface{att_node: node}

		var released atomic.Int32
		var returnedMAC *MacAddr
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)

		go func() {
			defer workers.Done()
			<-start
			returnedMAC, _, _ = arp_table_resolve_or_queue(
				&node.node_nw_prop.arp_table,
				&target,
				"eth0",
				pkt,
				len(pkt),
				func(*Node, *Interface, []byte, int) { released.Add(1) },
				nil,
				nil,
			)
		}()
		go func() {
			defer workers.Done()
			<-start
			arp_learn_binding(node, iif, &target, &resolvedMAC, false)
		}()

		close(start)
		workers.Wait()

		if returnedMAC == nil && released.Load() != 1 {
			t.Fatalf("iteration %d: packet was neither sent from the completed binding nor released from the pending queue", iteration)
		}
		if returnedMAC != nil && *returnedMAC != resolvedMAC {
			t.Fatalf("iteration %d: got MAC %s, want %s", iteration, returnedMAC, resolvedMAC.String())
		}
		if learned := arp_table_lookup(&node.node_nw_prop.arp_table, &target); learned == nil || *learned != resolvedMAC {
			t.Fatalf("iteration %d: completed binding was lost", iteration)
		}

		node.node_nw_prop.arp_table.mutex.RLock()
		entry := node.node_nw_prop.arp_table.head
		if entry == nil || entry.pending_list != nil || entry.is_sane {
			node.node_nw_prop.arp_table.mutex.RUnlock()
			t.Fatalf("iteration %d: ARP entry was left incomplete or retained a queued packet", iteration)
		}
		node.node_nw_prop.arp_table.mutex.RUnlock()
	}
}

func TestARPPendingEntryRetriesThenNotifiesTimeout(t *testing.T) {
	node := &Node{graph: &Graph{events: NewEventBus()}}
	init_arp_table(&node.node_nw_prop.arp_table)

	var eventsMu sync.Mutex
	var events []SimulationEvent
	node.graph.events.SetSink(func(event SimulationEvent) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})

	var target IpAddr
	if !set_ip_addr(&target, "198.51.100.7") {
		t.Fatal("failed to parse test address")
	}
	var retries atomic.Int32
	var failures atomic.Int32
	pkt := make([]byte, ETHERNET_HDR_SIZE+20)

	mac, requestNeeded, queued := arp_table_resolve_or_queue(
		&node.node_nw_prop.arp_table,
		&target,
		"eth0",
		pkt,
		len(pkt),
		nil,
		func() { failures.Add(1) },
		func() { retries.Add(1) },
	)
	if mac != nil || !requestNeeded || !queued {
		t.Fatalf("first unresolved packet returned mac=%v requestNeeded=%t queued=%t", mac, requestNeeded, queued)
	}
	mac, requestNeeded, queued = arp_table_resolve_or_queue(
		&node.node_nw_prop.arp_table,
		&target,
		"eth0",
		pkt,
		len(pkt),
		nil,
		func() { failures.Add(1) },
		func() { retries.Add(1) },
	)
	if mac != nil || requestNeeded || !queued {
		t.Fatalf("second unresolved packet returned mac=%v requestNeeded=%t queued=%t", mac, requestNeeded, queued)
	}

	node.node_nw_prop.arp_table.mutex.RLock()
	createdAt := node.node_nw_prop.arp_table.head.created_at
	node.node_nw_prop.arp_table.mutex.RUnlock()

	maintain_arp_pending_entries(node, createdAt.Add(ARP_REQUEST_RETRY_INTERVAL))
	maintain_arp_pending_entries(node, createdAt.Add(2*ARP_REQUEST_RETRY_INTERVAL))
	if got := retries.Load(); got != int32(ARP_REQUEST_MAX_ATTEMPTS-1) {
		t.Fatalf("got %d retries, want %d", got, ARP_REQUEST_MAX_ATTEMPTS-1)
	}
	if failures.Load() != 0 {
		t.Fatal("pending packet was failed before its resolution deadline")
	}

	maintain_arp_pending_entries(node, createdAt.Add(ARP_PENDING_TIMEOUT))
	if failures.Load() != 2 {
		t.Fatalf("failure callback ran %d times, want once per queued packet", failures.Load())
	}
	node.node_nw_prop.arp_table.mutex.RLock()
	entryStillPresent := node.node_nw_prop.arp_table.head != nil
	node.node_nw_prop.arp_table.mutex.RUnlock()
	if entryStillPresent {
		t.Fatal("timed-out incomplete entry remained in the ARP table")
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	retryEvents := 0
	failureEvents := 0
	for _, event := range events {
		switch event.Action {
		case "arp_request_retried":
			retryEvents++
		case "arp_resolution_failed":
			failureEvents++
		}
	}
	if retryEvents != ARP_REQUEST_MAX_ATTEMPTS-1 || failureEvents != 1 {
		t.Fatalf("got %d retry events and %d failure events, want %d and 1",
			retryEvents, failureEvents, ARP_REQUEST_MAX_ATTEMPTS-1)
	}
}

func TestARPRetryCanResolveAndReleasePendingPacket(t *testing.T) {
	node := &Node{graph: &Graph{events: NewEventBus()}}
	init_arp_table(&node.node_nw_prop.arp_table)
	iif := &Interface{att_node: node}

	var target IpAddr
	if !set_ip_addr(&target, "198.51.100.8") {
		t.Fatal("failed to parse test address")
	}
	resolvedMAC := MacAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x08}
	var released atomic.Int32
	var failed atomic.Int32
	pkt := make([]byte, ETHERNET_HDR_SIZE+20)

	_, requestNeeded, queued := arp_table_resolve_or_queue(
		&node.node_nw_prop.arp_table,
		&target,
		"eth0",
		pkt,
		len(pkt),
		func(*Node, *Interface, []byte, int) { released.Add(1) },
		func() { failed.Add(1) },
		func() { arp_learn_binding(node, iif, &target, &resolvedMAC, false) },
	)
	if !requestNeeded || !queued {
		t.Fatal("failed to start pending ARP resolution")
	}

	node.node_nw_prop.arp_table.mutex.RLock()
	createdAt := node.node_nw_prop.arp_table.head.created_at
	node.node_nw_prop.arp_table.mutex.RUnlock()
	maintain_arp_pending_entries(node, createdAt.Add(ARP_REQUEST_RETRY_INTERVAL))

	if released.Load() != 1 {
		t.Fatalf("retry reply released %d packets, want 1", released.Load())
	}
	if failed.Load() != 0 {
		t.Fatal("resolved pending packet was reported as failed")
	}
	if learned := arp_table_lookup(&node.node_nw_prop.arp_table, &target); learned == nil || *learned != resolvedMAC {
		t.Fatal("retry reply did not complete the ARP binding")
	}

	maintain_arp_pending_entries(node, createdAt.Add(ARP_PENDING_TIMEOUT))
	if failed.Load() != 0 {
		t.Fatal("completed entry was later treated as a timed-out pending entry")
	}
}

func TestARPExpiryDoesNotSilentlyRemovePendingEntry(t *testing.T) {
	var table arp_table
	init_arp_table(&table)
	var target IpAddr
	if !set_ip_addr(&target, "203.0.113.9") {
		t.Fatal("failed to parse test address")
	}
	pkt := make([]byte, ETHERNET_HDR_SIZE+20)
	_, _, queued := arp_table_resolve_or_queue(&table, &target, "eth0", pkt, len(pkt), nil, nil, nil)
	if !queued {
		t.Fatal("failed to create pending entry")
	}

	table.mutex.Lock()
	table.head.updated_at = time.Now().Add(-2 * ARP_ENTRY_TIMEOUT)
	table.mutex.Unlock()
	if removed := arp_table_cleanup_expired(&table); removed != 0 {
		t.Fatalf("cache cleanup silently removed %d pending entries", removed)
	}
	table.mutex.RLock()
	defer table.mutex.RUnlock()
	if table.head == nil || !table.head.is_sane || table.head.pending_list == nil {
		t.Fatal("cache cleanup discarded the pending entry or its queued packet")
	}
}
