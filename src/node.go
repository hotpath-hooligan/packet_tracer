package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	IF_NAME_SIZE      = 16 // Auxiliary data size for interface name
	NODE_NAME_SIZE    = 16
	MAX_INTF_PER_NODE = 10
)

// DeviceType describes the role a node plays in a topology. Packet processing
// is still selected by interface configuration: addressed interfaces are L3,
// while access and trunk interfaces are L2.
type DeviceType string

const (
	DEVICE_TYPE_HOST   DeviceType = "host"
	DEVICE_TYPE_SWITCH DeviceType = "switch"
	DEVICE_TYPE_ROUTER DeviceType = "router"
)

func parseDeviceType(value string) (DeviceType, bool) {
	switch DeviceType(strings.ToLower(strings.TrimSpace(value))) {
	case DEVICE_TYPE_HOST:
		return DEVICE_TYPE_HOST, true
	case DEVICE_TYPE_SWITCH:
		return DEVICE_TYPE_SWITCH, true
	case DEVICE_TYPE_ROUTER:
		return DEVICE_TYPE_ROUTER, true
	default:
		return "", false
	}
}

type IntfNwProps struct {
	is_ip_addr_config bool
	ip_addr           IpAddr
	mask              byte
	mac_addr          MacAddr

	// VLAN configuration
	mode        IntfMode // L3, Access, or Trunk mode
	access_vlan uint16   // VLAN ID for access mode (single VLAN)

	// Trunk mode configuration
	native_vlan        uint16                      // Native VLAN for trunk mode
	allowed_vlans      [MAX_VLAN_MEMBERSHIP]uint16 // Array of allowed VLANs on trunk
	allowed_vlan_count int                         // Number of VLANs in allowed_vlans array
}

// VlanInterface represents a Layer 3 interface for a VLAN (SVI - Switched Virtual Interface)
// This allows a switch to route between VLANs
type VlanInterface struct {
	vlan_id uint16 // VLAN ID this interface belongs to
	ip_addr IpAddr // IP address of this VLAN interface
	mask    byte   // Subnet mask for this VLAN
}

type NodeNwProp struct {
	is_loopback_addr_config bool
	loopback_addr           IpAddr
	arp_table               arp_table     // ARP table for this node
	mac_table               mac_table     // MAC table for L2 switching
	rt_table                *RoutingTable // L3 routing table
	vlan_mutex              sync.RWMutex
	vlan_interfaces         map[uint16]*VlanInterface // VLAN ID -> L3 interface (for inter-VLAN routing)
}

type Interface struct {
	if_name       [IF_NAME_SIZE]byte
	att_node      *Node
	link          *Link
	intf_nw_props IntfNwProps
}

type Link struct {
	intf1 *Interface
	intf2 *Interface
	cost  uint32
	up    atomic.Bool
}

func (link *Link) IsUp() bool {
	return link != nil && link.up.Load()
}

type Node struct {
	node_name             [NODE_NAME_SIZE]byte
	device_type           DeviceType
	intf                  [MAX_INTF_PER_NODE]*Interface
	graph                 *Graph
	node_nw_prop          NodeNwProp
	arp_cleanup_stop_ch   chan bool // Channel to stop ARP cleanup goroutine
	mac_cleanup_stop_ch   chan bool // Channel to stop MAC table cleanup goroutine
	rip_state             *RIPState // RIP protocol state
	lldp_state            *LLDPState
	stp_state             *STPState // Spanning tree state (bridges only)
	background_wait_group sync.WaitGroup
	ping_reply_count      atomic.Uint64
	icmp_error_mu         sync.Mutex
	icmp_errors           []ICMPErrorReport
}

// recordICMPError stores an error report delivered to this node so the sender
// that triggered it can be told why its packet never arrived.
func (node *Node) recordICMPError(report ICMPErrorReport) {
	if node == nil {
		return
	}
	node.icmp_error_mu.Lock()
	defer node.icmp_error_mu.Unlock()
	node.icmp_errors = append(node.icmp_errors, report)
}

// takeICMPErrors returns the reports collected since the last call and clears
// them, so each ping reads only the errors its own packets provoked.
func (node *Node) takeICMPErrors() []ICMPErrorReport {
	if node == nil {
		return nil
	}
	node.icmp_error_mu.Lock()
	defer node.icmp_error_mu.Unlock()
	reports := node.icmp_errors
	node.icmp_errors = nil
	return reports
}

func (node *Node) SetLoopbackIP(ip_str string) bool {
	if node == nil {
		return false
	}

	// Set loopback IP address
	if !set_ip_addr(&node.node_nw_prop.loopback_addr, ip_str) {
		return false
	}

	// Mark as configured
	node.node_nw_prop.is_loopback_addr_config = true
	return true
}

// node_set_loopback_address sets loopback address on a node
func node_set_loopback_address(node *Node, ip_addr string) bool {
	if node == nil {
		return false
	}

	return node.SetLoopbackIP(ip_addr)
}

// ====== VLAN Interface Management (SVI - Switched Virtual Interface) ======

// AddVlanInterface configures a Layer 3 interface for a VLAN (SVI)
// This enables inter-VLAN routing on the node
func (node *Node) AddVlanInterface(vlan_id uint16, ip_str string, mask byte) bool {
	if node == nil {
		return false
	}

	// Validate VLAN ID
	if vlan_id < VLAN_MIN || vlan_id > VLAN_MAX {
		LogError("AddVlanInterface: Invalid VLAN ID %d (must be %d-%d)", vlan_id, VLAN_MIN, VLAN_MAX)
		return false
	}
	if mask > 32 {
		LogError("AddVlanInterface: Invalid mask %d", mask)
		return false
	}
	node.node_nw_prop.vlan_mutex.Lock()
	defer node.node_nw_prop.vlan_mutex.Unlock()

	// Initialize map if needed
	if node.node_nw_prop.vlan_interfaces == nil {
		node.node_nw_prop.vlan_interfaces = make(map[uint16]*VlanInterface)
	}

	// Check if VLAN interface already exists
	if _, exists := node.node_nw_prop.vlan_interfaces[vlan_id]; exists {
		LogWarn("AddVlanInterface: VLAN %d interface already exists on node %s", vlan_id, get_node_name(node))
		return false
	}

	// Create VLAN interface
	vlan_intf := &VlanInterface{
		vlan_id: vlan_id,
		mask:    mask,
	}

	// Set IP address
	if !set_ip_addr(&vlan_intf.ip_addr, ip_str) {
		LogError("AddVlanInterface: Invalid IP address %s", ip_str)
		return false
	}

	// Add to node
	node.node_nw_prop.vlan_interfaces[vlan_id] = vlan_intf
	if err := node.node_nw_prop.rt_table.AddRouteWithParams(
		ip_str,
		mask,
		"",
		fmt.Sprintf("vlan%d", vlan_id),
		ROUTE_SOURCE_CONNECTED,
		uint8(ROUTE_SOURCE_CONNECTED),
		0,
	); err != nil {
		delete(node.node_nw_prop.vlan_interfaces, vlan_id)
		LogError("AddVlanInterface: Failed to add connected route: %v", err)
		return false
	}

	LogInfo("Node %s: Added VLAN %d interface with IP %s/%d",
		get_node_name(node), vlan_id, ip_str, mask)

	return true
}

// GetVlanInterface returns the VLAN interface for a given VLAN ID
func (node *Node) GetVlanInterface(vlan_id uint16) *VlanInterface {
	if node == nil {
		return nil
	}
	node.node_nw_prop.vlan_mutex.RLock()
	defer node.node_nw_prop.vlan_mutex.RUnlock()
	vlan_intf := node.node_nw_prop.vlan_interfaces[vlan_id]
	if vlan_intf == nil {
		return nil
	}
	copy := *vlan_intf
	return &copy
}

// HasVlanInterface checks if a node has a VLAN interface configured
func (node *Node) HasVlanInterface(vlan_id uint16) bool {
	return node.GetVlanInterface(vlan_id) != nil
}

// GetVlanInterfaceCount returns the number of VLAN interfaces configured
func (node *Node) GetVlanInterfaceCount() int {
	if node == nil {
		return 0
	}
	node.node_nw_prop.vlan_mutex.RLock()
	defer node.node_nw_prop.vlan_mutex.RUnlock()
	return len(node.node_nw_prop.vlan_interfaces)
}

func (node *Node) GetVlanInterfacesSnapshot() map[uint16]VlanInterface {
	if node == nil {
		return nil
	}
	node.node_nw_prop.vlan_mutex.RLock()
	defer node.node_nw_prop.vlan_mutex.RUnlock()
	interfaces := make(map[uint16]VlanInterface, len(node.node_nw_prop.vlan_interfaces))
	for vlanID, vlanIntf := range node.node_nw_prop.vlan_interfaces {
		interfaces[vlanID] = *vlanIntf
	}
	return interfaces
}
