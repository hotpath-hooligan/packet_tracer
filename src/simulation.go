package main

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
)

const DefaultTracerouteMaxHops = 30

type InterfaceState struct {
	Name         string
	IP           string
	Mask         uint8
	Mode         string
	AccessVLAN   uint16
	NativeVLAN   uint16
	AllowedVLANs []uint16
	Up           bool
}

type VLANInterfaceState struct {
	VLAN uint16
	IP   string
	Mask uint8
}

type RouteState struct {
	Destination   string
	Mask          uint8
	Gateway       string
	Interface     string
	Source        string
	AdminDistance uint8
	Metric        uint32
	Direct        bool
}

type ARPState struct {
	IP        string
	MAC       string
	Interface string
	Pending   bool
}

type MACState struct {
	MAC       string
	VLAN      uint16
	Interface string
}

type LLDPNeighborState struct {
	SystemName     string
	Port           string
	LocalInterface string
}

// STPBridgeState summarises where a bridge sits in the spanning tree.
type STPBridgeState struct {
	Enabled      bool
	Priority     uint16
	BridgeID     string
	RootID       string
	IsRoot       bool
	RootPathCost uint32
	RootPort     string
	Ports        []STPPortStatus
}

type NodeState struct {
	Name           string
	Type           DeviceType
	Position       *PositionConfig
	Loopback       string
	LLDPEnabled    bool
	STP            STPBridgeState
	Interfaces     []InterfaceState
	VLANInterfaces []VLANInterfaceState
	Routes         []RouteState
	ARP            []ARPState
	MAC            []MACState
	LLDP           []LLDPNeighborState
}

type LinkState struct {
	From          string
	FromInterface string
	To            string
	ToInterface   string
	Cost          uint32
	Up            bool
}

type TopologyState struct {
	Name               string
	Description        string
	Summary            string
	DefaultSource      string
	DefaultDestination string
	Canvas             *CanvasConfig
	Transport          string
	Nodes              []NodeState
	Links              []LinkState
}

type PingResult struct {
	Source        string
	Destination   string
	DestinationIP string
	ReplyReceived bool
	// Errors holds any ICMP error reports the ping provoked. A failed ping
	// with an entry here tells the user which router gave up and why; an empty
	// one means the packet vanished without explanation.
	Errors []ICMPErrorReport
}

type PingTraceResult struct {
	PingResult
	Events []SimulationEvent
}

type TracerouteHop struct {
	TTL      int
	Address  string
	Reached  bool
	TimedOut bool
	Reason   string
}

type TracerouteResult struct {
	Source        string
	Destination   string
	DestinationIP string
	Reached       bool
	Hops          []TracerouteHop
}

type TracerouteTraceResult struct {
	TracerouteResult
	Events []SimulationEvent
}

type LLDPTraceResult struct {
	Advertisers []string
	Events      []SimulationEvent
}

type Simulator struct {
	mu               sync.Mutex
	graph            *Graph
	topologyYAML     []byte
	transportFactory func() FrameTransport
	eventSink        EventSink
	captureMu        sync.Mutex
	captureProtocols map[string]struct{}
	capturedEvents   []SimulationEvent
}

func NewSimulator(transportFactory func() FrameTransport, eventSink EventSink) *Simulator {
	if transportFactory == nil {
		transportFactory = func() FrameTransport { return NewMemoryTransport() }
	}
	return &Simulator{transportFactory: transportFactory, eventSink: eventSink}
}

func (simulator *Simulator) LoadTopology(yamlData string) (*TopologyState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	return simulator.loadTopologyLocked([]byte(yamlData))
}

func (simulator *Simulator) loadTopologyLocked(yamlData []byte) (*TopologyState, error) {
	graph, err := load_topology_from_bytes_with_transport(yamlData, simulator.transportFactory())
	if err != nil {
		return nil, err
	}
	graph.SetEventSink(simulator.handleEvent)
	if err := graph.Start(); err != nil {
		cleanup_graph_resources(graph)
		return nil, err
	}
	if simulator.graph != nil {
		cleanup_graph_resources(simulator.graph)
	}
	simulator.graph = graph
	simulator.topologyYAML = append([]byte(nil), yamlData...)
	state := snapshotTopology(graph)
	return &state, nil
}

func (simulator *Simulator) Ping(source, destination string) (*PingResult, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	return simulator.pingLocked(source, destination)
}

func (simulator *Simulator) pingLocked(source, destination string) (*PingResult, error) {
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}
	sourceNode := findGraphNode(simulator.graph, source)
	if sourceNode == nil {
		return nil, fmt.Errorf("source node %s was not found", source)
	}
	destinationIP, err := resolveDestination(simulator.graph, destination)
	if err != nil {
		return nil, err
	}
	before := sourceNode.ping_reply_count.Load()
	// Discard reports left over from earlier activity so the result only
	// describes this ping.
	sourceNode.takeICMPErrors()
	simulator.graph.emitEvent(SimulationEvent{
		Protocol: "ICMP",
		Node:     source,
		From:     source,
		To:       destination,
		Action:   "ping_started",
		Fields:   map[string]string{"destinationIp": destinationIP},
	})
	Layer5PingFunc(sourceNode, destinationIP)
	received := sourceNode.ping_reply_count.Load() > before
	icmpErrors := sourceNode.takeICMPErrors()
	action := "ping_no_reply"
	if received {
		action = "ping_reply_received"
	}
	fields := map[string]string{"destinationIp": destinationIP}
	if !received && len(icmpErrors) > 0 {
		failure := icmpErrors[len(icmpErrors)-1]
		fields["reason"] = failure.Reason
		fields["reportedBy"] = failure.ReporterIP
	}
	simulator.graph.emitEvent(SimulationEvent{
		Protocol: "ICMP",
		Node:     source,
		From:     destination,
		To:       source,
		Action:   action,
		Fields:   fields,
	})
	return &PingResult{Source: source, Destination: destination, DestinationIP: destinationIP, ReplyReceived: received, Errors: icmpErrors}, nil
}

func (simulator *Simulator) TracePing(source, destination string) (*PingTraceResult, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}

	simulator.graph.traceMu.Lock()
	defer simulator.graph.traceMu.Unlock()
	simulator.beginCapture("ARP", "ICMP", "ETHERNET")
	result, err := simulator.pingLocked(source, destination)
	events := simulator.endCapture()
	if err != nil {
		return nil, err
	}
	return &PingTraceResult{PingResult: *result, Events: events}, nil
}

func (simulator *Simulator) Traceroute(source, destination string, maxHops int) (*TracerouteResult, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	return simulator.tracerouteLocked(source, destination, maxHops)
}

func (simulator *Simulator) tracerouteLocked(source, destination string, maxHops int) (*TracerouteResult, error) {
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}
	if maxHops < 1 || maxHops > 255 {
		return nil, fmt.Errorf("maximum hops must be between 1 and 255")
	}
	sourceNode := findGraphNode(simulator.graph, source)
	if sourceNode == nil {
		return nil, fmt.Errorf("source node %s was not found", source)
	}
	destinationIP, err := resolveDestination(simulator.graph, destination)
	if err != nil {
		return nil, err
	}

	result := &TracerouteResult{
		Source:        source,
		Destination:   destination,
		DestinationIP: destinationIP,
		Hops:          make([]TracerouteHop, 0, maxHops),
	}
	simulator.graph.emitEvent(SimulationEvent{
		Protocol: "ICMP",
		Node:     source,
		From:     source,
		To:       destination,
		Action:   "traceroute_started",
		Fields: map[string]string{
			"destinationIp": destinationIP,
			"maxHops":       strconv.Itoa(maxHops),
		},
	})

	// Clear activity left by an earlier operation before issuing the first
	// probe. Each iteration clears again because probes are deliberately
	// synchronous in the finite simulator trace.
	sourceNode.takeICMPErrors()
	for ttl := 1; ttl <= maxHops; ttl++ {
		beforeReplies := sourceNode.ping_reply_count.Load()
		sourceNode.takeICMPErrors()
		ttlText := strconv.Itoa(ttl)
		simulator.graph.emitEvent(SimulationEvent{
			Protocol: "ICMP",
			Node:     source,
			From:     source,
			To:       destination,
			Action:   "traceroute_probe_started",
			Fields: map[string]string{
				"destinationIp": destinationIP,
				"ttl":           ttlText,
			},
		})

		Layer5TracerouteProbeFunc(sourceNode, destinationIP, uint8(ttl))
		reached := sourceNode.ping_reply_count.Load() > beforeReplies
		reports := sourceNode.takeICMPErrors()
		hop := TracerouteHop{TTL: ttl}
		action := "traceroute_probe_timed_out"
		fields := map[string]string{
			"destinationIp": destinationIP,
			"ttl":           ttlText,
		}

		if reached {
			hop.Address = destinationIP
			hop.Reached = true
			hop.Reason = "echo_reply"
			action = "traceroute_destination_reached"
			fields["address"] = destinationIP
			fields["reason"] = hop.Reason
		} else if len(reports) > 0 {
			report := reports[len(reports)-1]
			hop.Address = report.ReporterIP
			hop.Reason = report.Reason
			action = "traceroute_hop_discovered"
			fields["address"] = report.ReporterIP
			fields["reason"] = report.Reason
		} else {
			hop.TimedOut = true
			hop.Reason = "timeout"
			fields["reason"] = hop.Reason
		}

		result.Hops = append(result.Hops, hop)
		simulator.graph.emitEvent(SimulationEvent{
			Protocol: "ICMP",
			Node:     source,
			Action:   action,
			Fields:   fields,
		})
		if reached {
			result.Reached = true
			break
		}
	}

	simulator.graph.emitEvent(SimulationEvent{
		Protocol: "ICMP",
		Node:     source,
		Action:   "traceroute_completed",
		Fields: map[string]string{
			"destinationIp": destinationIP,
			"hops":          strconv.Itoa(len(result.Hops)),
			"reached":       strconv.FormatBool(result.Reached),
		},
	})
	return result, nil
}

func (simulator *Simulator) TraceTraceroute(source, destination string, maxHops int) (*TracerouteTraceResult, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}

	simulator.graph.traceMu.Lock()
	defer simulator.graph.traceMu.Unlock()
	simulator.beginCapture("ARP", "ICMP", "ETHERNET")
	result, err := simulator.tracerouteLocked(source, destination, maxHops)
	events := simulator.endCapture()
	if err != nil {
		return nil, err
	}
	return &TracerouteTraceResult{TracerouteResult: *result, Events: events}, nil
}

func (simulator *Simulator) TraceLLDP() (*LLDPTraceResult, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}

	advertisers := make([]string, 0)
	for _, node := range simulator.graph.node_list {
		if node != nil && node.lldp_state != nil && node.lldp_state.IsEnabled() {
			advertisers = append(advertisers, get_node_name(node))
		}
	}
	if len(advertisers) == 0 {
		return nil, fmt.Errorf("LLDP is not enabled on any device")
	}

	simulator.graph.traceMu.Lock()
	defer simulator.graph.traceMu.Unlock()
	simulator.graph.lldpTraceRound.Store(true)
	defer simulator.graph.lldpTraceRound.Store(false)
	simulator.beginCapture("LLDP")
	for _, node := range simulator.graph.node_list {
		if node != nil && node.lldp_state != nil && node.lldp_state.IsEnabled() {
			node.lldp_state.SendAdvertisements()
		}
	}
	events := simulator.endCapture()
	return &LLDPTraceResult{Advertisers: advertisers, Events: events}, nil
}

func (simulator *Simulator) beginCapture(protocols ...string) {
	simulator.captureMu.Lock()
	simulator.captureProtocols = make(map[string]struct{}, len(protocols))
	for _, protocol := range protocols {
		simulator.captureProtocols[protocol] = struct{}{}
	}
	simulator.capturedEvents = make([]SimulationEvent, 0)
	simulator.captureMu.Unlock()
}

func (simulator *Simulator) endCapture() []SimulationEvent {
	simulator.captureMu.Lock()
	events := simulator.capturedEvents
	simulator.captureProtocols = nil
	simulator.capturedEvents = nil
	simulator.captureMu.Unlock()
	return events
}

func (simulator *Simulator) handleEvent(event SimulationEvent) {
	simulator.captureMu.Lock()
	if _, capturing := simulator.captureProtocols[event.Protocol]; capturing {
		simulator.capturedEvents = append(simulator.capturedEvents, cloneSimulationEvent(event))
	}
	sink := simulator.eventSink
	simulator.captureMu.Unlock()
	if sink != nil {
		sink(event)
	}
}

func cloneSimulationEvent(event SimulationEvent) SimulationEvent {
	clone := event
	if event.Fields != nil {
		clone.Fields = make(map[string]string, len(event.Fields))
		for key, value := range event.Fields {
			clone.Fields[key] = value
		}
	}
	if event.Table != nil {
		table := *event.Table
		table.Query = cloneStringMap(event.Table.Query)
		table.Entry = cloneStringMap(event.Table.Entry)
		clone.Table = &table
	}
	return clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func (simulator *Simulator) GetNodeState(nodeName string) (*NodeState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}
	node := findGraphNode(simulator.graph, nodeName)
	if node == nil {
		return nil, fmt.Errorf("node %s was not found", nodeName)
	}
	state := snapshotNode(node)
	return &state, nil
}

func (simulator *Simulator) SetLLDP(nodeName string, enabled bool) (*NodeState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}
	node := findGraphNode(simulator.graph, nodeName)
	if node == nil {
		return nil, fmt.Errorf("node %s was not found", nodeName)
	}
	if node.lldp_state == nil {
		return nil, fmt.Errorf("node %s has no LLDP state", nodeName)
	}
	if enabled {
		node.lldp_state.StartLLDP()
	} else {
		node.lldp_state.StopLLDP()
	}
	state := snapshotNode(node)
	return &state, nil
}

// SetSTP turns the spanning tree on or off for one bridge and settles the
// tree again, so callers see a converged topology when this returns.
func (simulator *Simulator) SetSTP(nodeName string, enabled bool) (*NodeState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}
	node := findGraphNode(simulator.graph, nodeName)
	if node == nil {
		return nil, fmt.Errorf("node %s was not found", nodeName)
	}
	if node.stp_state == nil {
		return nil, fmt.Errorf("node %s has no spanning tree state", nodeName)
	}
	if enabled {
		node.stp_state.StartSTP()
	} else {
		node.stp_state.StopSTP()
	}
	ConvergeSpanningTree(simulator.graph)
	state := snapshotNode(node)
	return &state, nil
}

// SetSTPPriority changes a bridge's priority, which is how a topology chooses
// its root bridge, and settles the tree again.
func (simulator *Simulator) SetSTPPriority(nodeName string, priority uint16) (*NodeState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}
	node := findGraphNode(simulator.graph, nodeName)
	if node == nil {
		return nil, fmt.Errorf("node %s was not found", nodeName)
	}
	if node.stp_state == nil {
		return nil, fmt.Errorf("node %s has no spanning tree state", nodeName)
	}
	if !node.stp_state.SetPriority(priority) {
		return nil, fmt.Errorf("bridge priority %d must be a multiple of %d",
			priority, STP_BRIDGE_PRIORITY_STEP)
	}
	ConvergeSpanningTree(simulator.graph)
	state := snapshotNode(node)
	return &state, nil
}

func (simulator *Simulator) SetLinkState(fromNode, fromInterface, toNode, toInterface string, up bool) (*TopologyState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if simulator.graph == nil {
		return nil, fmt.Errorf("no topology is loaded")
	}

	link := findGraphLink(simulator.graph, fromNode, fromInterface, toNode, toInterface)
	if link == nil {
		return nil, fmt.Errorf("link %s:%s to %s:%s was not found", fromNode, fromInterface, toNode, toInterface)
	}
	if link.IsUp() == up {
		state := snapshotTopology(simulator.graph)
		return &state, nil
	}

	link.up.Store(up)
	for _, node := range simulator.graph.node_list {
		if node == nil {
			continue
		}
		flush_mac_table(&node.node_nw_prop.mac_table)
		node.stp_state.resetForTopologyChange()
	}
	ConvergeSpanningTree(simulator.graph)

	state := snapshotTopology(simulator.graph)
	return &state, nil
}

func (simulator *Simulator) Reset() (*TopologyState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if len(simulator.topologyYAML) == 0 {
		if simulator.graph != nil {
			cleanup_graph_resources(simulator.graph)
			simulator.graph = nil
		}
		return nil, nil
	}
	return simulator.loadTopologyLocked(simulator.topologyYAML)
}

func findGraphNode(graph *Graph, name string) *Node {
	if graph == nil {
		return nil
	}
	for _, node := range graph.node_list {
		if get_node_name(node) == name {
			return node
		}
	}
	return nil
}

func findGraphLink(graph *Graph, fromNode, fromInterface, toNode, toInterface string) *Link {
	if graph == nil {
		return nil
	}
	for _, node := range graph.node_list {
		if node == nil {
			continue
		}
		for _, intf := range node.intf {
			if intf == nil || intf.link == nil || intf.link.intf1 != intf {
				continue
			}
			link := intf.link
			firstMatches := get_node_name(link.intf1.att_node) == fromNode &&
				get_interface_name(link.intf1) == fromInterface &&
				get_node_name(link.intf2.att_node) == toNode &&
				get_interface_name(link.intf2) == toInterface
			secondMatches := get_node_name(link.intf1.att_node) == toNode &&
				get_interface_name(link.intf1) == toInterface &&
				get_node_name(link.intf2.att_node) == fromNode &&
				get_interface_name(link.intf2) == fromInterface
			if firstMatches || secondMatches {
				return link
			}
		}
	}
	return nil
}

func resolveDestination(graph *Graph, destination string) (string, error) {
	if _, valid := parseIPv4(destination); valid {
		return destination, nil
	}
	node := findGraphNode(graph, destination)
	if node == nil {
		return "", fmt.Errorf("destination node %s was not found", destination)
	}
	for _, intf := range node.intf {
		if intf != nil && intf.intf_nw_props.is_ip_addr_config {
			return intf.intf_nw_props.ip_addr.String(), nil
		}
	}
	for _, vlanIntf := range node.GetVlanInterfacesSnapshot() {
		return vlanIntf.ip_addr.String(), nil
	}
	if node.node_nw_prop.is_loopback_addr_config {
		return node.node_nw_prop.loopback_addr.String(), nil
	}
	return "", fmt.Errorf("destination node %s has no IP address", destination)
}

func snapshotTopology(graph *Graph) TopologyState {
	state := TopologyState{
		Name:               get_topology_name(graph),
		Description:        graph.render_data.Description,
		Summary:            graph.render_data.Summary,
		DefaultSource:      graph.render_data.DefaultSource,
		DefaultDestination: graph.render_data.DefaultDestination,
		Canvas:             graph.render_data.Canvas,
		Transport:          graph.transport.Name(),
		Nodes:              make([]NodeState, 0),
		Links:              make([]LinkState, 0),
	}
	for _, node := range graph.node_list {
		state.Nodes = append(state.Nodes, snapshotNode(node))
		for _, intf := range node.intf {
			if intf == nil || intf.link == nil || intf.link.intf1 != intf {
				continue
			}
			state.Links = append(state.Links, LinkState{
				From:          get_node_name(intf.link.intf1.att_node),
				FromInterface: get_interface_name(intf.link.intf1),
				To:            get_node_name(intf.link.intf2.att_node),
				ToInterface:   get_interface_name(intf.link.intf2),
				Cost:          intf.link.cost,
				Up:            intf.link.IsUp(),
			})
		}
	}
	return state
}

func snapshotNode(node *Node) NodeState {
	state := NodeState{Name: get_node_name(node), Type: node.device_type, Interfaces: make([]InterfaceState, 0), VLANInterfaces: make([]VLANInterfaceState, 0), Routes: make([]RouteState, 0), ARP: make([]ARPState, 0), MAC: make([]MACState, 0), LLDP: make([]LLDPNeighborState, 0)}
	if position, ok := node.graph.render_data.Positions[state.Name]; ok {
		state.Position = &position
	}
	if node.node_nw_prop.is_loopback_addr_config {
		state.Loopback = node.node_nw_prop.loopback_addr.String()
	}
	for _, intf := range node.intf {
		if intf == nil {
			continue
		}
		intfState := InterfaceState{Name: get_interface_name(intf), Up: intf.link != nil && intf.link.IsUp()}
		switch intf.intf_nw_props.mode {
		case INTF_MODE_L3:
			intfState.Mode = "l3"
			if intf.intf_nw_props.is_ip_addr_config {
				intfState.IP = intf.intf_nw_props.ip_addr.String()
				intfState.Mask = intf.intf_nw_props.mask
			}
		case INTF_MODE_TRUNK:
			intfState.Mode = "trunk"
			intfState.NativeVLAN = intf.intf_nw_props.native_vlan
			intfState.AllowedVLANs = append(
				[]uint16(nil),
				intf.intf_nw_props.allowed_vlans[:intf.intf_nw_props.allowed_vlan_count]...,
			)
		default:
			intfState.Mode = "access"
			intfState.AccessVLAN = intf.intf_nw_props.access_vlan
		}
		state.Interfaces = append(state.Interfaces, intfState)
	}
	for vlan, intf := range node.GetVlanInterfacesSnapshot() {
		state.VLANInterfaces = append(state.VLANInterfaces, VLANInterfaceState{VLAN: vlan, IP: intf.ip_addr.String(), Mask: intf.mask})
	}
	sort.Slice(state.VLANInterfaces, func(i, j int) bool { return state.VLANInterfaces[i].VLAN < state.VLANInterfaces[j].VLAN })
	for _, route := range node.node_nw_prop.rt_table.RoutesSnapshot() {
		state.Routes = append(state.Routes, RouteState{
			Destination:   route.Dest,
			Mask:          route.Mask,
			Gateway:       route.GatewayIP,
			Interface:     route.OIF,
			Source:        RouteSourceToString(route.Source),
			AdminDistance: route.AdminDistance,
			Metric:        route.Metric,
			Direct:        route.IsDirect,
		})
	}

	node.node_nw_prop.arp_table.mutex.RLock()
	for entry := node.node_nw_prop.arp_table.head; entry != nil; entry = entry.next {
		state.ARP = append(state.ARP, ARPState{IP: entry.ip_addr.String(), MAC: entry.mac_addr.String(), Interface: byteArrayString(entry.oif_name[:]), Pending: entry.is_sane})
	}
	node.node_nw_prop.arp_table.mutex.RUnlock()

	node.node_nw_prop.mac_table.mutex.RLock()
	for entry := node.node_nw_prop.mac_table.head; entry != nil; entry = entry.next {
		state.MAC = append(state.MAC, MACState{MAC: entry.mac_addr.String(), VLAN: entry.vlan_id, Interface: byteArrayString(entry.oif_name[:])})
	}
	node.node_nw_prop.mac_table.mutex.RUnlock()

	state.STP = node.stp_state.BridgeSummary()

	if node.lldp_state != nil {
		node.lldp_state.mutex.RLock()
		state.LLDPEnabled = node.lldp_state.enabled
		for _, neighbor := range node.lldp_state.neighbors {
			state.LLDP = append(state.LLDP, LLDPNeighborState{SystemName: neighbor.SystemName, Port: neighbor.PortID, LocalInterface: neighbor.LocalInterface})
		}
		node.lldp_state.mutex.RUnlock()
	}

	return state
}

func byteArrayString(value []byte) string {
	for index, item := range value {
		if item == 0 {
			return string(value[:index])
		}
	}
	return string(value)
}
