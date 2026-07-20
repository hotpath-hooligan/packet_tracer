package main

// ====== VLAN Support (802.1Q) ======

// Interface mode types
type IntfMode int

const (
	INTF_MODE_L3     IntfMode = 0 // L3 routing mode (has IP address)
	INTF_MODE_ACCESS IntfMode = 1 // L2 access mode (single VLAN, untagged)
	INTF_MODE_TRUNK  IntfMode = 2 // L2 trunk mode (multiple VLANs, tagged)
)

// VLAN constants
const (
	VLAN_TPID           uint16 = 0x8100 // 802.1Q Tag Protocol ID
	VLAN_MIN            uint16 = 1      // Minimum VLAN ID
	VLAN_MAX            uint16 = 4094   // Maximum VLAN ID (4095 is reserved)
	VLAN_DEFAULT        uint16 = 1      // Default VLAN
	VLAN_TAG_SIZE       int    = 4      // VLAN tag size in bytes
	VLAN_HEADER_SIZE    int    = 18     // Ethernet header with VLAN tag (14 + 4)
	MAX_VLAN_MEMBERSHIP int    = 10     // Maximum VLANs per trunk interface
)

// 802.1Q VLAN tag structure (4 bytes)
type VlanTag struct {
	tpid uint16 // Tag Protocol ID (0x8100)
	tci  uint16 // Tag Control Information (PCP + DEI + VID)
	// PCP (3 bits): Priority Code Point
	// DEI (1 bit): Drop Eligible Indicator
	// VID (12 bits): VLAN ID (0-4095)
}

// Helper functions for VLAN tag TCI field
func make_tci(pcp byte, dei byte, vlan_id uint16) uint16 {
	return (uint16(pcp&0x07) << 13) | (uint16(dei&0x01) << 12) | (vlan_id & 0x0FFF)
}

func extract_vlan_id(tci uint16) uint16 {
	return tci & 0x0FFF
}

func (intf *Interface) GetMac() *MacAddr {
	return &intf.intf_nw_props.mac_addr
}

func (intf *Interface) GetIP() *IpAddr {
	return &intf.intf_nw_props.ip_addr
}

func (intf *Interface) GetMask() byte {
	return intf.intf_nw_props.mask
}

func (intf *Interface) IsIPConfigured() bool {
	return intf.intf_nw_props.is_ip_addr_config
}

func (intf *Interface) SetIPConfig(ip_str string, mask byte) bool {
	if intf == nil {
		return false
	}

	// Use the comprehensive API to properly handle mode transition
	return SetInterfaceL3Mode(intf, ip_str, mask)
}

func IS_INTF_L3_MODE(intf *Interface) bool {
	return intf != nil && intf.intf_nw_props.is_ip_addr_config
}

func (intf *Interface) GetVLANMode() IntfMode {
	if intf == nil {
		return INTF_MODE_ACCESS
	}
	return intf.intf_nw_props.mode
}

func (intf *Interface) SetAccessVLAN(vlan_id uint16) bool {
	if intf == nil {
		return false
	}

	// MANDATORY: ACCESS mode requires a valid VLAN ID
	if vlan_id < VLAN_MIN || vlan_id > VLAN_MAX {
		LogError("Cannot set ACCESS mode: invalid VLAN ID %d (must be %d-%d)",
			vlan_id, VLAN_MIN, VLAN_MAX)
		return false
	}

	// Use the comprehensive API to properly handle mode transition
	return SetInterfaceL2Mode(intf, INTF_MODE_ACCESS, vlan_id, 0, nil)
}

func (intf *Interface) GetAccessVLAN() uint16 {
	if intf == nil {
		return VLAN_DEFAULT
	}
	return intf.intf_nw_props.access_vlan
}

func (intf *Interface) SetTrunkConfig(native_vlan uint16, allowed_vlans []uint16) bool {
	if intf == nil || native_vlan < VLAN_MIN || native_vlan > VLAN_MAX {
		return false
	}

	// Use the comprehensive API to properly handle mode transition
	return SetInterfaceL2Mode(intf, INTF_MODE_TRUNK, 0, native_vlan, allowed_vlans)
}

func (intf *Interface) GetNativeVLAN() uint16 {
	if intf == nil {
		return VLAN_DEFAULT
	}
	return intf.intf_nw_props.native_vlan
}

func (intf *Interface) IsVLANAllowed(vlan_id uint16) bool {
	if intf == nil {
		return false
	}

	// L3 mode interfaces don't filter by VLAN
	if IS_INTF_L3_MODE(intf) {
		return true
	}

	// Access mode allows only its configured VLAN
	// MANDATORY: ACCESS mode interface MUST have a valid VLAN configured
	if intf.intf_nw_props.mode == INTF_MODE_ACCESS {
		// If access_vlan is 0, it's misconfigured - reject all frames
		if intf.intf_nw_props.access_vlan == 0 {
			LogWarn("Interface %s in ACCESS mode has no VLAN configured - dropping frame",
				get_interface_name(intf))
			return false
		}
		return vlan_id == intf.intf_nw_props.access_vlan
	}

	// Trunk mode checks allowed VLAN list
	if intf.intf_nw_props.mode == INTF_MODE_TRUNK {
		for i := 0; i < intf.intf_nw_props.allowed_vlan_count; i++ {
			if intf.intf_nw_props.allowed_vlans[i] == vlan_id {
				return true
			}
		}
		return false
	}

	return false
}

// SetInterfaceL2Mode configures an interface for L2 switching (Access or Trunk mode)
// This will disable any IP configuration and switch the interface to L2 mode
// For Access mode: provide vlan_id, set native_vlan to 0, allowed_vlans to nil
// For Trunk mode: provide native_vlan and allowed_vlans slice
func SetInterfaceL2Mode(intf *Interface, mode IntfMode, vlan_id uint16, native_vlan uint16, allowed_vlans []uint16) bool {
	if intf == nil {
		return false
	}

	// Mode must be Access or Trunk (not L3)
	if mode != INTF_MODE_ACCESS && mode != INTF_MODE_TRUNK {
		LogError("SetInterfaceL2Mode: Invalid mode %d (must be Access or Trunk)", mode)
		return false
	}
	if mode == INTF_MODE_ACCESS && (vlan_id < VLAN_MIN || vlan_id > VLAN_MAX) {
		LogError("SetInterfaceL2Mode: ACCESS mode requires valid VLAN ID (got %d, must be %d-%d)",
			vlan_id, VLAN_MIN, VLAN_MAX)
		return false
	}
	if mode == INTF_MODE_TRUNK {
		if native_vlan < VLAN_MIN || native_vlan > VLAN_MAX {
			LogError("SetInterfaceL2Mode: Invalid native VLAN %d", native_vlan)
			return false
		}
		if len(allowed_vlans) > MAX_VLAN_MEMBERSHIP {
			LogError("SetInterfaceL2Mode: Too many VLANs (%d), maximum is %d", len(allowed_vlans), MAX_VLAN_MEMBERSHIP)
			return false
		}
		seen := make(map[uint16]bool)
		for _, allowed_vlan := range allowed_vlans {
			if allowed_vlan < VLAN_MIN || allowed_vlan > VLAN_MAX || seen[allowed_vlan] {
				LogError("SetInterfaceL2Mode: Invalid or duplicate allowed VLAN %d", allowed_vlan)
				return false
			}
			seen[allowed_vlan] = true
		}
	}

	// Clear any existing IP configuration (transition to L2)
	if intf.intf_nw_props.is_ip_addr_config {
		LogInfo("Interface %s: Disabling IP configuration, switching to L2 mode",
			get_interface_name(intf))
		intf.intf_nw_props.is_ip_addr_config = false
		// Clear IP address
		for i := 0; i < 4; i++ {
			intf.intf_nw_props.ip_addr[i] = 0
		}
		intf.intf_nw_props.mask = 0
	}

	// Configure based on mode
	if mode == INTF_MODE_ACCESS {
		intf.intf_nw_props.mode = INTF_MODE_ACCESS
		intf.intf_nw_props.access_vlan = vlan_id
		intf.intf_nw_props.allowed_vlan_count = 0
		LogInfo("Interface %s: Configured in Access mode, VLAN %d",
			get_interface_name(intf), vlan_id)
		return true
	}

	intf.intf_nw_props.mode = INTF_MODE_TRUNK
	intf.intf_nw_props.native_vlan = native_vlan

	vlan_count := len(allowed_vlans)
	intf.intf_nw_props.allowed_vlan_count = vlan_count
	for i := 0; i < vlan_count; i++ {
		intf.intf_nw_props.allowed_vlans[i] = allowed_vlans[i]
	}

	LogInfo("Interface %s: Configured in Trunk mode, native VLAN %d, %d allowed VLANs",
		get_interface_name(intf), native_vlan, vlan_count)
	return true
}

// SetInterfaceL3Mode configures an interface for L3 routing
// This will clear any L2 VLAN configuration
func SetInterfaceL3Mode(intf *Interface, ip_str string, mask byte) bool {
	if intf == nil {
		return false
	}
	if mask > 32 {
		LogError("SetInterfaceL3Mode: Invalid mask %d", mask)
		return false
	}
	var ip_addr IpAddr
	if !set_ip_addr(&ip_addr, ip_str) {
		LogError("SetInterfaceL3Mode: Invalid IP address %s", ip_str)
		return false
	}

	// Clear L2 configuration
	intf.intf_nw_props.mode = INTF_MODE_L3
	intf.intf_nw_props.access_vlan = VLAN_DEFAULT
	intf.intf_nw_props.native_vlan = VLAN_DEFAULT
	intf.intf_nw_props.allowed_vlan_count = 0

	intf.intf_nw_props.ip_addr = ip_addr
	intf.intf_nw_props.mask = mask
	intf.intf_nw_props.is_ip_addr_config = true

	LogInfo("Interface %s: Configured in L3 mode, IP %s/%d",
		get_interface_name(intf), ip_str, mask)
	return true
}

// node_set_intf_ip_address sets IP address on a node's interface
func node_set_intf_ip_address(node *Node, local_if string, ip_addr string, mask byte) bool {
	if node == nil {
		return false
	}

	// Find the interface by name
	intf := get_node_if_by_name(node, local_if)
	if intf == nil {
		LogError("Interface %s not found on node %s", local_if, get_node_name(node))
		return false
	}

	// Set the IP configuration
	if !intf.SetIPConfig(ip_addr, mask) {
		return false
	}

	// Automatically add direct route for the subnet with interface name
	if node.node_nw_prop.rt_table != nil {
		// For direct routes, we store the interface name for reference
		// Gateway IP is empty for direct routes
		err := node.node_nw_prop.rt_table.AddRouteWithParams(
			ip_addr,
			mask,
			"",
			local_if,
			ROUTE_SOURCE_CONNECTED,
			uint8(ROUTE_SOURCE_CONNECTED),
			0,
		)
		if err != nil {
			LogError("Failed to add direct route for %s/%d on node %s: %v",
				ip_addr, mask, get_node_name(node), err)
			// Don't fail the whole operation just because route add failed
		} else {
			LogInfo("Added direct route for %s/%d on interface %s on node %s",
				ip_addr, mask, local_if, get_node_name(node))
		}
	}

	return true
}

// Node method implementations for loopback configuration

// get_interface_name extracts interface name from Interface struct
func get_interface_name(intf *Interface) string {
	if intf == nil {
		return ""
	}

	name := make([]byte, 0, IF_NAME_SIZE)
	for _, b := range intf.if_name {
		if b == 0 {
			break
		}
		name = append(name, b)
	}
	return string(name)
}

// get_remote_interface gets the interface on the other side of the link
func get_remote_interface(local_intf *Interface) *Interface {
	if local_intf == nil || local_intf.link == nil {
		return nil
	}

	link := local_intf.link
	if link.intf1 == local_intf {
		return link.intf2
	} else if link.intf2 == local_intf {
		return link.intf1
	}

	return nil
}

// get_node_name is a helper function to extract node name from the byte array
func get_node_name(node *Node) string {
	if node == nil {
		return ""
	}

	// Convert byte array to string, stopping at first null byte
	name := make([]byte, 0, NODE_NAME_SIZE)
	for _, b := range node.node_name {
		if b == 0 {
			break
		}
		name = append(name, b)
	}
	return string(name)
}

// node_get_matching_intf_by_name finds an interface on a node by name
func node_get_matching_intf_by_name(node *Node, intf_name string) *Interface {
	if node == nil || intf_name == "" {
		return nil
	}

	// Search through all interfaces on the node
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := node.intf[i]
		if intf == nil {
			continue
		}

		// Compare interface name
		if get_interface_name(intf) == intf_name {
			return intf
		}
	}

	return nil
}
