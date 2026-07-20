package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v2"
)

const MAX_NODES_PER_TOPOLOGY = 100

// YAML topology configuration structures
type TopologyConfig struct {
	Topology TopologyInfo `yaml:"topology"`
	Nodes    []NodeConfig `yaml:"nodes"`
	Links    []LinkConfig `yaml:"links"`
}

type TopologyInfo struct {
	Name               string        `yaml:"name"`
	Description        string        `yaml:"description"`
	Summary            string        `yaml:"summary"`
	DefaultSource      string        `yaml:"default_source"`
	DefaultDestination string        `yaml:"default_destination"`
	Canvas             *CanvasConfig `yaml:"canvas"`
}

type CanvasConfig struct {
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

type PositionConfig struct {
	X int `yaml:"x"`
	Y int `yaml:"y"`
}

type NodeConfig struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	// LLDP controls the node's initial discovery state. It stays disabled when
	// omitted and can still be changed at runtime through the simulator.
	LLDP bool `yaml:"lldp"`
	// STP enables or disables the spanning tree. Bridges run it by default, so
	// this is only needed to turn it off or to switch it on for another
	// device. Priority must be a multiple of 4096 and lowers the bridge
	// identifier, making the bridge more likely to be elected root.
	STP            *bool                 `yaml:"stp"`
	STPPriority    *int                  `yaml:"stp_priority"`
	Position       *PositionConfig       `yaml:"position"`
	Loopback       string                `yaml:"loopback"`
	Interfaces     []InterfaceConfig     `yaml:"interfaces"`
	VLANInterfaces []VLANInterfaceConfig `yaml:"vlan_interfaces"`
	Routes         []RouteConfig         `yaml:"routes"`
}

type VLANInterfaceConfig struct {
	VLAN int    `yaml:"vlan"`
	IP   string `yaml:"ip"`
	Mask int    `yaml:"mask"`
}

type RouteConfig struct {
	Destination string `yaml:"destination"`
	Mask        int    `yaml:"mask"`
	Gateway     string `yaml:"gateway"`
	Interface   string `yaml:"interface"`
}

type InterfaceConfig struct {
	Name         string `yaml:"name"`
	IP           string `yaml:"ip"`
	Mask         int    `yaml:"mask"`
	Mode         string `yaml:"mode"`          // "access" or "trunk" (optional, defaults to access)
	VLAN         int    `yaml:"vlan"`          // For access mode: single VLAN ID
	NativeVLAN   int    `yaml:"native_vlan"`   // For trunk mode: native VLAN
	AllowedVLANs []int  `yaml:"allowed_vlans"` // For trunk mode: allowed VLANs
}

type LinkConfig struct {
	FromNode      string `yaml:"from_node"`
	FromInterface string `yaml:"from_interface"`
	ToNode        string `yaml:"to_node"`
	ToInterface   string `yaml:"to_interface"`
	Cost          int    `yaml:"cost"`
}

func load_topology_from_bytes_with_transport(data []byte, transport FrameTransport) (*Graph, error) {
	var config TopologyConfig
	err := yaml.UnmarshalStrict(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML topology: %v", err)
	}

	// Validate
	if err := validate_topology_config(&config); err != nil {
		return nil, fmt.Errorf("topology validation failed: %v", err)
	}

	// Create the graph
	graph, err := build_graph_from_config_with_transport(&config, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to build graph: %v", err)
	}

	return graph, nil
}

// validate_topology_config performs basic validation on the topology configuration
func validate_topology_config(config *TopologyConfig) error {
	if config.Topology.Name == "" {
		return fmt.Errorf("topology name is required")
	}
	if len(config.Topology.Name) > 32 {
		return fmt.Errorf("topology name must not exceed 32 bytes")
	}

	if len(config.Nodes) == 0 {
		return fmt.Errorf("at least one node is required")
	}
	if len(config.Nodes) > MAX_NODES_PER_TOPOLOGY {
		return fmt.Errorf("topology supports at most %d devices", MAX_NODES_PER_TOPOLOGY)
	}
	if config.Topology.Canvas != nil {
		if config.Topology.Canvas.Width <= 0 || config.Topology.Canvas.Height <= 0 {
			return fmt.Errorf("topology canvas width and height must be positive")
		}
		if config.Topology.Canvas.Width > 10000 || config.Topology.Canvas.Height > 10000 {
			return fmt.Errorf("topology canvas width and height must not exceed 10000")
		}
	}

	// Create a map of node names for validation
	nodeMap := make(map[string]bool)
	interfaceMap := make(map[string]bool) // node:interface format
	linkedInterfaces := make(map[string]bool)

	// Validate nodes and build maps
	for _, node := range config.Nodes {
		if node.Name == "" {
			return fmt.Errorf("node name is required")
		}
		if len(node.Name) > NODE_NAME_SIZE {
			return fmt.Errorf("node name %s must not exceed %d bytes", node.Name, NODE_NAME_SIZE)
		}
		if len(node.Interfaces) > MAX_INTF_PER_NODE {
			return fmt.Errorf("node %s supports at most %d interfaces", node.Name, MAX_INTF_PER_NODE)
		}
		if node.Type != "" {
			if _, valid := parseDeviceType(node.Type); !valid {
				return fmt.Errorf("invalid device type %q on node %s (must be host, switch, or router)", node.Type, node.Name)
			}
		}
		if node.Position != nil {
			if config.Topology.Canvas == nil {
				return fmt.Errorf("node %s configures position without topology canvas", node.Name)
			}
			if node.Position.X < 0 || node.Position.X > config.Topology.Canvas.Width || node.Position.Y < 0 || node.Position.Y > config.Topology.Canvas.Height {
				return fmt.Errorf("position for node %s must be inside the topology canvas", node.Name)
			}
		}
		if node.Loopback != "" {
			var loopback IpAddr
			if !set_ip_addr(&loopback, node.Loopback) {
				return fmt.Errorf("invalid loopback IP %s on node %s", node.Loopback, node.Name)
			}
		}

		if nodeMap[node.Name] {
			return fmt.Errorf("duplicate node name: %s", node.Name)
		}
		nodeMap[node.Name] = true

		nodeInterfaces := make(map[string]bool)
		// Validate interfaces
		for _, intf := range node.Interfaces {
			if intf.Name == "" {
				return fmt.Errorf("interface name is required for node %s", node.Name)
			}
			if len(intf.Name) > IF_NAME_SIZE {
				return fmt.Errorf("interface name %s on node %s must not exceed %d bytes", intf.Name, node.Name, IF_NAME_SIZE)
			}

			intfKey := fmt.Sprintf("%s:%s", node.Name, intf.Name)
			if interfaceMap[intfKey] {
				return fmt.Errorf("duplicate interface name %s on node %s", intf.Name, node.Name)
			}
			interfaceMap[intfKey] = true
			nodeInterfaces[intf.Name] = true

			hasSwitchportFields := intf.Mode != "" || intf.VLAN != 0 || intf.NativeVLAN != 0 || len(intf.AllowedVLANs) > 0
			if intf.IP == "" && intf.Mask != 0 {
				return fmt.Errorf("interface %s on node %s configures mask without ip", intf.Name, node.Name)
			}
			if intf.IP != "" && hasSwitchportFields {
				return fmt.Errorf("interface %s on node %s cannot be both a routed interface (ip/mask) and a Layer 2 switchport (access/trunk configuration)", intf.Name, node.Name)
			}

			// IP and mask are optional for L2 interfaces (no IP means L2 mode)
			if intf.IP != "" {
				var ip IpAddr
				if !set_ip_addr(&ip, intf.IP) {
					return fmt.Errorf("invalid IP %s for interface %s on node %s", intf.IP, intf.Name, node.Name)
				}
				if intf.Mask < 1 || intf.Mask > 32 {
					return fmt.Errorf("invalid subnet mask %d for interface %s on node %s", intf.Mask, intf.Name, node.Name)
				}
			} else {
				mode := intf.Mode
				if mode == "" {
					mode = "access"
				}
				if mode != "access" && mode != "trunk" {
					return fmt.Errorf("invalid mode %s for interface %s on node %s", mode, intf.Name, node.Name)
				}
				if mode == "access" && (intf.NativeVLAN != 0 || len(intf.AllowedVLANs) > 0) {
					return fmt.Errorf("interface %s on node %s cannot use native_vlan or allowed_vlans in access mode", intf.Name, node.Name)
				}
				if mode == "trunk" && intf.VLAN != 0 {
					return fmt.Errorf("interface %s on node %s cannot use vlan in trunk mode", intf.Name, node.Name)
				}
				if mode == "access" && intf.VLAN != 0 && (intf.VLAN < int(VLAN_MIN) || intf.VLAN > int(VLAN_MAX)) {
					return fmt.Errorf("invalid access VLAN %d for interface %s on node %s", intf.VLAN, intf.Name, node.Name)
				}
				if mode == "trunk" {
					if intf.NativeVLAN != 0 && (intf.NativeVLAN < int(VLAN_MIN) || intf.NativeVLAN > int(VLAN_MAX)) {
						return fmt.Errorf("invalid native VLAN %d for interface %s on node %s", intf.NativeVLAN, intf.Name, node.Name)
					}
					if len(intf.AllowedVLANs) > MAX_VLAN_MEMBERSHIP {
						return fmt.Errorf("interface %s on node %s supports at most %d allowed VLANs", intf.Name, node.Name, MAX_VLAN_MEMBERSHIP)
					}
					for _, vlan := range intf.AllowedVLANs {
						if vlan < int(VLAN_MIN) || vlan > int(VLAN_MAX) {
							return fmt.Errorf("invalid allowed VLAN %d for interface %s on node %s", vlan, intf.Name, node.Name)
						}
					}
				}
			}
		}

		vlanInterfaces := make(map[int]bool)
		for _, vlanIntf := range node.VLANInterfaces {
			if vlanIntf.VLAN < int(VLAN_MIN) || vlanIntf.VLAN > int(VLAN_MAX) {
				return fmt.Errorf("invalid VLAN interface ID %d on node %s", vlanIntf.VLAN, node.Name)
			}
			if vlanInterfaces[vlanIntf.VLAN] {
				return fmt.Errorf("duplicate VLAN interface %d on node %s", vlanIntf.VLAN, node.Name)
			}
			vlanInterfaces[vlanIntf.VLAN] = true
			var ip IpAddr
			if !set_ip_addr(&ip, vlanIntf.IP) || vlanIntf.Mask < 1 || vlanIntf.Mask > 32 {
				return fmt.Errorf("invalid VLAN interface address %s/%d on node %s", vlanIntf.IP, vlanIntf.Mask, node.Name)
			}
		}

		for _, route := range node.Routes {
			var destination IpAddr
			var gateway IpAddr
			if !set_ip_addr(&destination, route.Destination) || route.Mask < 0 || route.Mask > 32 {
				return fmt.Errorf("invalid route destination %s/%d on node %s", route.Destination, route.Mask, node.Name)
			}
			if !set_ip_addr(&gateway, route.Gateway) {
				return fmt.Errorf("invalid route gateway %s on node %s", route.Gateway, node.Name)
			}
			if !nodeInterfaces[route.Interface] {
				return fmt.Errorf("route interface %s not found on node %s", route.Interface, node.Name)
			}
		}
	}
	if config.Topology.DefaultSource != "" && !nodeMap[config.Topology.DefaultSource] {
		return fmt.Errorf("topology default_source %s not found", config.Topology.DefaultSource)
	}
	if config.Topology.DefaultDestination != "" && !nodeMap[config.Topology.DefaultDestination] {
		return fmt.Errorf("topology default_destination %s not found", config.Topology.DefaultDestination)
	}

	// Validate links
	for i, link := range config.Links {
		if link.FromNode == "" || link.ToNode == "" {
			return fmt.Errorf("link %d: from_node and to_node are required", i)
		}

		if link.FromInterface == "" || link.ToInterface == "" {
			return fmt.Errorf("link %d: from_interface and to_interface are required", i)
		}

		// Check if nodes exist
		if !nodeMap[link.FromNode] {
			return fmt.Errorf("link %d: from_node %s not found", i, link.FromNode)
		}

		if !nodeMap[link.ToNode] {
			return fmt.Errorf("link %d: to_node %s not found", i, link.ToNode)
		}

		// Check if interfaces exist
		fromIntfKey := fmt.Sprintf("%s:%s", link.FromNode, link.FromInterface)
		toIntfKey := fmt.Sprintf("%s:%s", link.ToNode, link.ToInterface)

		if !interfaceMap[fromIntfKey] {
			return fmt.Errorf("link %d: from_interface %s not found on node %s", i, link.FromInterface, link.FromNode)
		}

		if !interfaceMap[toIntfKey] {
			return fmt.Errorf("link %d: to_interface %s not found on node %s", i, link.ToInterface, link.ToNode)
		}
		if linkedInterfaces[fromIntfKey] || linkedInterfaces[toIntfKey] {
			return fmt.Errorf("link %d reuses an interface that is already connected", i)
		}
		linkedInterfaces[fromIntfKey] = true
		linkedInterfaces[toIntfKey] = true

		if link.Cost < 0 {
			return fmt.Errorf("link %d: cost must be non-negative", i)
		}
	}

	return nil
}

func build_graph_from_config_with_transport(config *TopologyConfig, transport FrameTransport) (*Graph, error) {
	graph := create_new_graph_with_transport(config.Topology.Name, transport)
	graph.render_data = TopologyRenderData{
		Description:        config.Topology.Description,
		Summary:            config.Topology.Summary,
		DefaultSource:      config.Topology.DefaultSource,
		DefaultDestination: config.Topology.DefaultDestination,
		Canvas:             config.Topology.Canvas,
		Positions:          make(map[string]PositionConfig),
	}
	for _, nodeConfig := range config.Nodes {
		if nodeConfig.Position != nil {
			graph.render_data.Positions[nodeConfig.Name] = *nodeConfig.Position
		}
	}
	built := false
	defer func() {
		if !built {
			cleanup_graph_resources(graph)
		}
	}()

	// Create a map to store created nodes for easy lookup
	nodeMap := make(map[string]*Node)

	// Create all nodes first
	for _, nodeConfig := range config.Nodes {
		node := create_graph_node(graph, nodeConfig.Name)
		if node == nil {
			return nil, fmt.Errorf("failed to create node %s", nodeConfig.Name)
		}
		node.device_type = resolveDeviceType(nodeConfig)
		nodeMap[nodeConfig.Name] = node
		for _, intfConfig := range nodeConfig.Interfaces {
			if _, err := create_node_interface(node, intfConfig.Name); err != nil {
				return nil, err
			}
		}

		// Set loopback address if provided
		if nodeConfig.Loopback != "" {
			node_set_loopback_address(node, nodeConfig.Loopback)
		}

		for _, vlanIntfConfig := range nodeConfig.VLANInterfaces {
			if !node.AddVlanInterface(uint16(vlanIntfConfig.VLAN), vlanIntfConfig.IP, byte(vlanIntfConfig.Mask)) {
				return nil, fmt.Errorf("failed to configure VLAN interface %d on node %s", vlanIntfConfig.VLAN, nodeConfig.Name)
			}
		}

		for _, routeConfig := range nodeConfig.Routes {
			if err := node.node_nw_prop.rt_table.AddRoute(
				routeConfig.Destination,
				uint8(routeConfig.Mask),
				routeConfig.Gateway,
				routeConfig.Interface,
			); err != nil {
				return nil, fmt.Errorf("failed to configure route %s/%d on node %s: %w",
					routeConfig.Destination, routeConfig.Mask, nodeConfig.Name, err)
			}
		}
	}

	// Create links between nodes
	for _, linkConfig := range config.Links {
		fromNode := nodeMap[linkConfig.FromNode]
		toNode := nodeMap[linkConfig.ToNode]

		if fromNode == nil || toNode == nil {
			return nil, fmt.Errorf("failed to find nodes for link %s:%s -> %s:%s",
				linkConfig.FromNode, linkConfig.FromInterface,
				linkConfig.ToNode, linkConfig.ToInterface)
		}

		if err := insert_link_between_two_nodes(
			fromNode, toNode,
			linkConfig.FromInterface, linkConfig.ToInterface,
			uint32(linkConfig.Cost)); err != nil {
			return nil, fmt.Errorf("failed to create link: %w", err)
		}
	}

	// Configure interface IP addresses and VLAN settings
	for _, nodeConfig := range config.Nodes {
		node := nodeMap[nodeConfig.Name]
		if node == nil {
			continue
		}

		for _, intfConfig := range nodeConfig.Interfaces {
			intf := get_node_if_by_name(node, intfConfig.Name)
			if intf == nil {
				continue
			}

			// Configure IP address if provided (L3 mode)
			if intfConfig.IP != "" {
				success := node_set_intf_ip_address(node, intfConfig.Name, intfConfig.IP, byte(intfConfig.Mask))
				if !success {
					return nil, fmt.Errorf("failed to set IP address %s/%d on interface %s of node %s",
						intfConfig.IP, intfConfig.Mask, intfConfig.Name, nodeConfig.Name)
				}
				// IP configuration automatically sets interface to L3 mode
			} else {
				// No IP configured - configure VLAN settings for L2 mode
				mode := intfConfig.Mode
				if mode == "" {
					mode = "access" // Default to access mode
				}

				switch mode {
				case "access":
					vlan_id := uint16(intfConfig.VLAN)
					if vlan_id == 0 {
						vlan_id = VLAN_DEFAULT // Default to VLAN 1
					}
					if !intf.SetAccessVLAN(vlan_id) {
						return nil, fmt.Errorf("failed to set access VLAN %d on interface %s of node %s",
							vlan_id, intfConfig.Name, nodeConfig.Name)
					}
					LogInfo("Interface %s on %s: Access mode, VLAN %d", intfConfig.Name, nodeConfig.Name, vlan_id)

				case "trunk":
					native_vlan := uint16(intfConfig.NativeVLAN)
					if native_vlan == 0 {
						native_vlan = VLAN_DEFAULT
					}

					// Convert allowed VLANs from []int to []uint16
					allowed_vlans := make([]uint16, len(intfConfig.AllowedVLANs))
					for i, vlan := range intfConfig.AllowedVLANs {
						allowed_vlans[i] = uint16(vlan)
					}

					if !intf.SetTrunkConfig(native_vlan, allowed_vlans) {
						return nil, fmt.Errorf("failed to set trunk config on interface %s of node %s",
							intfConfig.Name, nodeConfig.Name)
					}
					LogInfo("Interface %s on %s: Trunk mode, Native VLAN %d, Allowed VLANs %v",
						intfConfig.Name, nodeConfig.Name, native_vlan, allowed_vlans)

				default:
					return nil, fmt.Errorf("invalid interface mode '%s' on interface %s of node %s (must be 'access' or 'trunk')",
						mode, intfConfig.Name, nodeConfig.Name)
				}
			}
		}
	}

	// Bring up the spanning tree once every interface has its final mode and
	// every link exists, so bridge identifiers and port costs are settled.
	for _, nodeConfig := range config.Nodes {
		node := nodeMap[nodeConfig.Name]
		if node == nil || node.stp_state == nil {
			continue
		}
		if nodeConfig.STPPriority != nil {
			priority := *nodeConfig.STPPriority
			if priority < 0 || priority > int(^uint16(0)) {
				return nil, fmt.Errorf("node %s: stp_priority %d is out of range", nodeConfig.Name, priority)
			}
			if !node.stp_state.SetPriority(uint16(priority)) {
				return nil, fmt.Errorf("node %s: stp_priority %d must be a multiple of %d",
					nodeConfig.Name, priority, STP_BRIDGE_PRIORITY_STEP)
			}
		}
		if stpEnabledForNode(nodeConfig, node) {
			node.stp_state.StartSTP()
		}
	}
	ConvergeSpanningTree(graph)

	// Enable YAML-configured LLDP only after every link and interface has its
	// final configuration, so the initial advertisements describe the complete
	// topology. Nodes without `lldp: true` remain disabled by default.
	for _, nodeConfig := range config.Nodes {
		if !nodeConfig.LLDP {
			continue
		}
		node := nodeMap[nodeConfig.Name]
		if node != nil && node.lldp_state != nil {
			node.lldp_state.StartLLDP()
		}
	}

	built = true
	return graph, nil
}

// stpEnabledForNode decides whether a node runs the spanning tree. Bridges do
// by default, because a switched topology with a redundant path floods
// forever without one. Hosts and routers do not bridge frames, so they stay
// out of the tree unless the topology asks for them.
func stpEnabledForNode(config NodeConfig, node *Node) bool {
	if config.STP != nil {
		return *config.STP
	}
	return node.device_type == DEVICE_TYPE_SWITCH
}

func resolveDeviceType(config NodeConfig) DeviceType {
	if configured, valid := parseDeviceType(config.Type); valid {
		return configured
	}

	if len(config.VLANInterfaces) > 0 {
		return DEVICE_TYPE_SWITCH
	}

	routedInterfaces := 0
	for _, intf := range config.Interfaces {
		if intf.IP == "" {
			return DEVICE_TYPE_SWITCH
		}
		routedInterfaces++
	}
	if routedInterfaces > 1 || hasRouterName(config.Name) {
		return DEVICE_TYPE_ROUTER
	}
	return DEVICE_TYPE_HOST
}

func hasRouterName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(name, "router") {
		return true
	}
	if len(name) < 2 || name[0] != 'r' {
		return false
	}
	for _, char := range name[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
