package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

type Graph struct {
	topology_name   [32]byte
	render_data     TopologyRenderData
	node_list       []*Node
	transport       FrameTransport
	events          *EventBus
	traceMu         sync.Mutex
	lldpTraceRound  atomic.Bool
	stpBudget       atomic.Int64
	stpCascadeDepth atomic.Int64
	lifecycleMu     sync.Mutex
	started         bool
	closed          bool
}

// TopologyRenderData is presentation metadata authored in the topology YAML.
// The simulator retains it so browser snapshots never need topology-specific
// constants for labels, endpoints, canvas dimensions, or node placement.
type TopologyRenderData struct {
	Description        string
	Summary            string
	DefaultSource      string
	DefaultDestination string
	Canvas             *CanvasConfig
	Positions          map[string]PositionConfig
}

func get_topology_name(graph *Graph) string {
	if graph == nil {
		return ""
	}

	// Convert byte array to string, stopping at first null byte
	name := make([]byte, 0, 32)
	for _, b := range graph.topology_name {
		if b == 0 {
			break
		}
		name = append(name, b)
	}
	return string(name)
}

func cleanup_graph_resources(graph *Graph) {
	if graph == nil {
		return
	}

	LogInfo("Cleaning up resources for topology: %s", get_topology_name(graph))
	graph.Stop()

	// Note: UDP monitoring should be stopped BEFORE calling this function
	// to avoid race conditions with goroutines accessing closed sockets
	for _, node := range graph.node_list {
		if node == nil {
			continue
		}
		if node.lldp_state != nil {
			node.lldp_state.StopLLDP()
		}
		if node.rip_state != nil {
			node.rip_state.StopRIP()
		}
		if node.stp_state != nil {
			node.stp_state.StopSTP()
		}
	}

	for _, node := range graph.node_list {
		if node != nil {
			// Stop MAC table cleanup goroutine
			stop_mac_table_cleanup(node)

			stop_arp_table_cleanup(node)
			node.background_wait_group.Wait()
		}
	}
	graph.Close()

	LogInfo("Resource cleanup completed for topology: %s", get_topology_name(graph))
}

func get_nbr_node(interface_ *Interface) *Node {
	if interface_ == nil || interface_.link == nil {
		return nil
	}

	if interface_.link.intf1 == interface_ {
		return interface_.link.intf2.att_node
	}

	if interface_.link.intf2 == interface_ {
		return interface_.link.intf1.att_node
	}

	return nil
}

func get_node_intf_available_slot(node *Node) int {
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		if node.intf[i] == nil {
			return i
		}
	}
	return -1
}

func get_node_if_by_name(node *Node, if_name string) *Interface {
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		if node.intf[i] != nil {
			// Convert byte array to string and trim null bytes
			stored_name := string(node.intf[i].if_name[:])
			// Find the first null byte and truncate there
			for j, b := range node.intf[i].if_name {
				if b == 0 {
					stored_name = string(node.intf[i].if_name[:j])
					break
				}
			}
			if stored_name == if_name {
				return node.intf[i]
			}
		}
	}
	return nil
}

func init_node_nw_props(node_nw_props *NodeNwProp) {
	node_nw_props.is_loopback_addr_config = false
	// Initialize loopback_addr to all zeros
	for i := 0; i < 4; i++ {
		node_nw_props.loopback_addr[i] = 0
	}
	// Initialize ARP table
	init_arp_table(&node_nw_props.arp_table)
	// Initialize MAC table for L2 switching
	init_mac_table(&node_nw_props.mac_table)
	// Initialize routing table for L3 routing
	node_nw_props.rt_table = InitRoutingTable()
}

func init_intf_nw_props(intf_nw_props *IntfNwProps) {
	intf_nw_props.is_ip_addr_config = false
	intf_nw_props.mask = 0

	// Initialize IP address to all zeros
	for i := 0; i < 4; i++ {
		intf_nw_props.ip_addr[i] = 0
	}

	// Initialize MAC address to all zeros
	for i := 0; i < 6; i++ {
		intf_nw_props.mac_addr[i] = 0
	}

	// Initialize VLAN configuration
	intf_nw_props.mode = INTF_MODE_ACCESS    // Default to access mode
	intf_nw_props.access_vlan = VLAN_DEFAULT // Default VLAN 1
	intf_nw_props.native_vlan = VLAN_DEFAULT
	intf_nw_props.allowed_vlan_count = 0 // No VLANs allowed initially for trunk
}

func create_new_graph_with_transport(topology_name string, transport FrameTransport) *Graph {
	if transport == nil {
		transport = NewMemoryTransport()
	}
	graph := &Graph{transport: transport, events: NewEventBus()}
	copy(graph.topology_name[:], topology_name)
	return graph
}

func create_graph_node(graph *Graph, node_name string) *Node {
	if graph == nil {
		return nil
	}
	node := &Node{graph: graph}
	copy(node.node_name[:], node_name)
	init_node_nw_props(&node.node_nw_prop)

	// Initialize RIP state
	node.rip_state = InitRIPState(node)
	node.lldp_state = InitLLDPState(node)
	node.stp_state = InitSTPState(node)

	err := graph.transport.Register(node)
	if err != nil {
		LogError("Failed to register node %s with %s transport: %v", node_name, graph.transport.Name(), err)
		return nil
	}

	// Start ARP cleanup goroutine
	start_arp_table_cleanup(node)

	// Start MAC table cleanup goroutine
	start_mac_table_cleanup(node)

	graph.node_list = append(graph.node_list, node)
	return node
}

func (graph *Graph) Start() error {
	if graph == nil {
		return fmt.Errorf("graph cannot be nil")
	}
	graph.lifecycleMu.Lock()
	defer graph.lifecycleMu.Unlock()
	if graph.closed {
		return fmt.Errorf("graph is closed")
	}
	if graph.started {
		return nil
	}
	if err := graph.transport.Start(graph); err != nil {
		return err
	}
	graph.started = true
	return nil
}

func (graph *Graph) Stop() {
	if graph == nil {
		return
	}
	graph.lifecycleMu.Lock()
	defer graph.lifecycleMu.Unlock()
	if !graph.started {
		return
	}
	graph.transport.Stop()
	graph.started = false
}

func (graph *Graph) Close() {
	if graph == nil {
		return
	}
	graph.lifecycleMu.Lock()
	defer graph.lifecycleMu.Unlock()
	if graph.closed {
		return
	}
	if graph.started {
		graph.transport.Stop()
		graph.started = false
	}
	if err := graph.transport.Close(); err != nil {
		LogError("Failed to close %s transport: %v", graph.transport.Name(), err)
	}
	graph.closed = true
}

func (graph *Graph) SetEventSink(sink EventSink) {
	if graph != nil {
		graph.events.SetSink(sink)
	}
}

// STP_BPDU_BUDGET bounds how many BPDUs the whole topology may emit while a
// spanning tree settles. The computation converges on its own because priority
// vectors only ever improve; the budget is a backstop that keeps a malformed
// topology from looping instead of converging.
const STP_BPDU_BUDGET = 10000

func (graph *Graph) resetSTPBudget() {
	if graph != nil {
		graph.stpBudget.Store(STP_BPDU_BUDGET)
	}
}

// beginSTPCascade marks the start of a spanning tree exchange and returns the
// function that ends it. A BPDU sent from here is delivered synchronously, so
// a neighbour may relay its own before this returns; only the outermost
// exchange replenishes the budget. The returned function must be called.
func (graph *Graph) beginSTPCascade() func() {
	if graph == nil {
		return func() {}
	}
	if graph.stpCascadeDepth.Add(1) == 1 {
		graph.resetSTPBudget()
	}
	return func() { graph.stpCascadeDepth.Add(-1) }
}

// consumeSTPBudget reserves one BPDU transmission, reporting false once the
// budget for the current convergence is spent.
func (graph *Graph) consumeSTPBudget() bool {
	if graph == nil {
		return true
	}
	return graph.stpBudget.Add(-1) >= 0
}

func (graph *Graph) runBackgroundProtocol(work func()) {
	if work == nil {
		return
	}
	if graph == nil {
		work()
		return
	}
	graph.traceMu.Lock()
	defer graph.traceMu.Unlock()
	work()
}

func create_node_interface(node *Node, interface_name string) (*Interface, error) {
	if node == nil {
		return nil, fmt.Errorf("node cannot be nil")
	}
	if get_node_if_by_name(node, interface_name) != nil {
		return nil, fmt.Errorf("interface %s already exists on node %s", interface_name, get_node_name(node))
	}

	slot := get_node_intf_available_slot(node)
	if slot == -1 {
		return nil, fmt.Errorf("node %s supports at most %d interfaces", get_node_name(node), MAX_INTF_PER_NODE)
	}

	intf := &Interface{att_node: node}
	copy(intf.if_name[:], interface_name)
	init_intf_nw_props(&intf.intf_nw_props)
	intf.intf_nw_props.mac_addr = generate_unique_mac_address()
	node.intf[slot] = intf
	return intf, nil
}

func insert_link_between_two_nodes(node1 *Node, node2 *Node, from_if_name string, to_if_name string, cost uint32) error {
	intf1 := get_node_if_by_name(node1, from_if_name)
	intf2 := get_node_if_by_name(node2, to_if_name)
	if intf1 == nil || intf2 == nil {
		return fmt.Errorf("cannot link missing interfaces %s:%s and %s:%s",
			get_node_name(node1), from_if_name, get_node_name(node2), to_if_name)
	}
	if intf1.link != nil || intf2.link != nil {
		return fmt.Errorf("an interface cannot be connected to more than one link")
	}

	link := &Link{
		intf1: intf1,
		intf2: intf2,
		cost:  cost,
	}
	link.up.Store(true)

	intf1.link = link
	intf2.link = link

	return nil
}

func dump_graph_info(graph *Graph) {
	fmt.Printf("=== Graph Information ===\n")

	topology_name := string(graph.topology_name[:])
	for i, b := range graph.topology_name {
		if b == 0 {
			topology_name = string(graph.topology_name[:i])
			break
		}
	}

	fmt.Printf("Topology Name: %s\n", topology_name)
	fmt.Printf("Total Nodes: %d\n", len(graph.node_list))

	if len(graph.node_list) == 0 {
		fmt.Println("No nodes in the graph.")
		return
	}

	fmt.Println("\n--- Node Details ---")

	for i, node := range graph.node_list {
		node_name := string(node.node_name[:])
		for j, b := range node.node_name {
			if b == 0 {
				node_name = string(node.node_name[:j])
				break
			}
		}

		fmt.Printf("\nNode #%d: %s\n", i+1, node_name)

		// Display loopback information
		if node.node_nw_prop.is_loopback_addr_config {
			fmt.Printf("  Loopback: %s (configured)\n", node.node_nw_prop.loopback_addr.String())
		} else {
			fmt.Printf("  Loopback: Not configured\n")
		}

		// Count and display interfaces
		interface_count := 0
		for j := 0; j < MAX_INTF_PER_NODE; j++ {
			if node.intf[j] != nil {
				interface_count++
			}
		}

		fmt.Printf("  Interfaces: %d\n", interface_count)

		// Display interface details
		for j := 0; j < MAX_INTF_PER_NODE; j++ {
			if node.intf[j] != nil {
				intf := node.intf[j]

				// Convert interface name from byte array to string (trim null bytes)
				if_name := string(intf.if_name[:])
				for k, b := range intf.if_name {
					if b == 0 {
						if_name = string(intf.if_name[:k])
						break
					}
				}

				fmt.Printf("    Interface: %s\n", if_name)
				fmt.Printf("      MAC: %s\n", intf.intf_nw_props.mac_addr.String())

				// Display interface mode and configuration
				switch intf.intf_nw_props.mode {
				case INTF_MODE_L3:
					if intf.intf_nw_props.is_ip_addr_config {
						fmt.Printf("      Mode: L3 (Routing)\n")
						fmt.Printf("      IP: %s/%d\n",
							intf.intf_nw_props.ip_addr.String(),
							intf.intf_nw_props.mask)
					} else {
						fmt.Printf("      Mode: L3 (IP not configured)\n")
					}

				case INTF_MODE_ACCESS:
					fmt.Printf("      Mode: L2 Access\n")
					fmt.Printf("      Access VLAN: %d\n", intf.intf_nw_props.access_vlan)

				case INTF_MODE_TRUNK:
					fmt.Printf("      Mode: L2 Trunk\n")
					fmt.Printf("      Native VLAN: %d\n", intf.intf_nw_props.native_vlan)
					if intf.intf_nw_props.allowed_vlan_count > 0 {
						fmt.Printf("      Allowed VLANs: [")
						for v := 0; v < int(intf.intf_nw_props.allowed_vlan_count); v++ {
							if v > 0 {
								fmt.Printf(" ")
							}
							fmt.Printf("%d", intf.intf_nw_props.allowed_vlans[v])
						}
						fmt.Printf("]\n")
					} else {
						fmt.Printf("      Allowed VLANs: None\n")
					}

				default:
					fmt.Printf("      Mode: Unknown\n")
				}

				// Display neighbor information
				if intf.link != nil {
					neighbor := get_nbr_node(intf)
					if neighbor != nil {
						neighbor_name := string(neighbor.node_name[:])
						for k, b := range neighbor.node_name {
							if b == 0 {
								neighbor_name = string(neighbor.node_name[:k])
								break
							}
						}
						fmt.Printf("      Connected to: %s (cost: %d)\n",
							neighbor_name, intf.link.cost)
					}
				} else {
					fmt.Printf("      Connected to: None\n")
				}
			}
		}
	}

	fmt.Printf("\n=== End Graph Information ===\n")
}
