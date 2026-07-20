package main

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ====== Layer 2 Switch (MAC Learning and Forwarding) ======

// MAC table entry represents a learned MAC address
type mac_table_entry struct {
	mac_addr   MacAddr            // Learned MAC address
	vlan_id    uint16             // VLAN ID (for VLAN-aware switching)
	oif_name   [IF_NAME_SIZE]byte // Outgoing interface name
	created_at time.Time          // When entry was learned
	updated_at time.Time          // Last time entry was updated
	next       *mac_table_entry   // Next entry in linked list
}

// MAC table for L2 switch
type mac_table struct {
	head  *mac_table_entry // Head of linked list
	mutex sync.RWMutex     // Mutex for thread-safe access
}

func macTableReference(result, mac string, vlanID uint16, interfaceName string) *EventTableReference {
	reference := &EventTableReference{
		Kind:   "mac",
		Result: result,
		Query: map[string]string{
			"mac":  mac,
			"vlan": fmt.Sprintf("%d", vlanID),
		},
	}
	if interfaceName != "" {
		reference.Entry = map[string]string{
			"mac":       mac,
			"vlan":      fmt.Sprintf("%d", vlanID),
			"interface": interfaceName,
		}
	}
	return reference
}

// MAC table entry timeout (5 minutes - typical for switches)
const MAC_TABLE_ENTRY_TIMEOUT = 300 * time.Second

// MAC table cleanup interval (60 seconds)
const MAC_TABLE_CLEANUP_INTERVAL = 60 * time.Second

// MAC table refresh threshold - only update timestamp if entry is older than this
// This avoids unnecessary updates on every frame for active entries
const MAC_TABLE_REFRESH_THRESHOLD = 30 * time.Second

// checks if a MAC address is broadcast (FF:FF:FF:FF:FF:FF)
func is_mac_broadcast(mac *MacAddr) bool {
	if mac == nil {
		return false
	}
	return mac[0] == 0xFF && mac[1] == 0xFF && mac[2] == 0xFF &&
		mac[3] == 0xFF && mac[4] == 0xFF && mac[5] == 0xFF
}

// initializes the MAC table
func init_mac_table(table *mac_table) {
	if table == nil {
		return
	}
	table.head = nil
}

// looks up a MAC address in the MAC table (VLAN-aware)
// Returns the outgoing interface name if found, empty string otherwise
func mac_table_lookup_vlan(table *mac_table, mac_addr *MacAddr, vlan_id uint16) string {
	if table == nil || mac_addr == nil {
		return ""
	}

	table.mutex.RLock()
	defer table.mutex.RUnlock()

	// Search through linked list for matching MAC + VLAN
	for current := table.head; current != nil; current = current.next {
		if current.mac_addr == *mac_addr && current.vlan_id == vlan_id {
			// Extract interface name
			oif_name := string(current.oif_name[:])
			for i, b := range oif_name {
				if b == 0 {
					oif_name = oif_name[:i]
					break
				}
			}
			return oif_name
		}
	}

	return ""
}

// looks up a MAC address in the MAC table (legacy, uses default VLAN)
// Returns the outgoing interface name if found, empty string otherwise
func mac_table_lookup(table *mac_table, mac_addr *MacAddr) string {
	return mac_table_lookup_vlan(table, mac_addr, VLAN_DEFAULT)
}

// adds or updates a MAC table entry (VLAN-aware)
// Returns true if entry was added/updated, false otherwise
func mac_table_add_or_update_vlan(table *mac_table, mac_addr *MacAddr, vlan_id uint16, oif_name string) bool {
	if table == nil || mac_addr == nil {
		return false
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	now := time.Now()

	// Check if entry already exists for this MAC + VLAN combination
	for current := table.head; current != nil; current = current.next {
		if current.mac_addr == *mac_addr && current.vlan_id == vlan_id {
			// Check if interface changed
			current_oif := string(current.oif_name[:])
			for i, b := range current_oif {
				if b == 0 {
					current_oif = current_oif[:i]
					break
				}
			}

			if current_oif != oif_name {
				// Interface changed - update it
				copy(current.oif_name[:], []byte(oif_name))
				current.updated_at = now
				LogDebug("L2Switch: MAC %s VLAN %d moved from %s to %s", mac_addr.String(), vlan_id, current_oif, oif_name)
				return true
			}

			// Entry exists on same interface - only refresh if it's getting old
			age := now.Sub(current.updated_at)
			if age >= MAC_TABLE_REFRESH_THRESHOLD {
				current.updated_at = now
				LogDebug("L2Switch: Refreshed MAC %s VLAN %d on %s (age was %v)",
					mac_addr.String(), vlan_id, oif_name, age)
				return true
			}

			// Entry is still fresh - no update needed
			return false
		}
	}

	// Entry doesn't exist - create new one
	entry := &mac_table_entry{
		mac_addr:   *mac_addr,
		vlan_id:    vlan_id,
		created_at: now,
		updated_at: now,
		next:       table.head,
	}
	copy(entry.oif_name[:], []byte(oif_name))

	table.head = entry
	LogInfo("L2Switch: Learned MAC %s VLAN %d on interface %s", mac_addr.String(), vlan_id, oif_name)
	return true
}

// adds or updates a MAC table entry (legacy, uses default VLAN)
func mac_table_add_or_update(table *mac_table, mac_addr *MacAddr, oif_name string) bool {
	return mac_table_add_or_update_vlan(table, mac_addr, VLAN_DEFAULT, oif_name)
}

// deletes a MAC table entry
func mac_table_delete_entry(table *mac_table, mac_addr *MacAddr) bool {
	if table == nil || mac_addr == nil {
		return false
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	// Handle deletion from head
	if table.head != nil && table.head.mac_addr == *mac_addr {
		table.head = table.head.next
		return true
	}

	// Search and delete from middle/end
	for current := table.head; current != nil && current.next != nil; current = current.next {
		if current.next.mac_addr == *mac_addr {
			current.next = current.next.next
			return true
		}
	}

	return false
}

// removes expired entries from the MAC table
func mac_table_cleanup_expired(table *mac_table) int {
	if table == nil {
		return 0
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	now := time.Now()
	removed := 0

	// Handle head deletions
	for table.head != nil && now.Sub(table.head.updated_at) > MAC_TABLE_ENTRY_TIMEOUT {
		LogDebug("L2Switch: Removing expired MAC entry for %s (age: %v)",
			table.head.mac_addr.String(),
			now.Sub(table.head.updated_at))
		table.head = table.head.next
		removed++
	}

	// Handle middle/end deletions
	for current := table.head; current != nil && current.next != nil; {
		if now.Sub(current.next.updated_at) > MAC_TABLE_ENTRY_TIMEOUT {
			LogDebug("L2Switch: Removing expired MAC entry for %s (age: %v)",
				current.next.mac_addr.String(),
				now.Sub(current.next.updated_at))
			current.next = current.next.next
			removed++
		} else {
			current = current.next
		}
	}

	if removed > 0 {
		LogInfo("L2Switch: Cleaned up %d expired MAC entries", removed)
	}

	return removed
}

// flush_mac_table drops every learned entry. The spanning tree calls this on a
// topology change so that traffic is flooded onto the new tree rather than
// being sent towards a port that no longer reaches the destination.
func flush_mac_table(table *mac_table) int {
	if table == nil {
		return 0
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	removed := 0
	for current := table.head; current != nil; current = current.next {
		removed++
	}
	table.head = nil

	if removed > 0 {
		LogInfo("L2Switch: Flushed %d MAC entries", removed)
	}
	return removed
}

// flush_mac_table_for_interface drops the entries learned on one interface,
// used when a port leaves the forwarding state.
func flush_mac_table_for_interface(table *mac_table, oif_name string) int {
	if table == nil || oif_name == "" {
		return 0
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	removed := 0
	for table.head != nil && byteArrayString(table.head.oif_name[:]) == oif_name {
		table.head = table.head.next
		removed++
	}
	for current := table.head; current != nil && current.next != nil; {
		if byteArrayString(current.next.oif_name[:]) == oif_name {
			current.next = current.next.next
			removed++
		} else {
			current = current.next
		}
	}

	if removed > 0 {
		LogInfo("L2Switch: Flushed %d MAC entries learned on %s", removed, oif_name)
	}
	return removed
}

// dumps the MAC table for debugging
func mac_table_dump(table *mac_table, node_name string) {
	if table == nil {
		return
	}

	table.mutex.RLock()
	defer table.mutex.RUnlock()

	fmt.Printf("\n=== MAC Table for Node %s ===\n", node_name)
	fmt.Printf("%-17s %-6s %-16s %s\n", "MAC Address", "VLAN", "Interface", "Age")
	fmt.Printf("%-17s %-6s %-16s %s\n", "-----------", "----", "---------", "---")

	count := 0
	now := time.Now()
	for current := table.head; current != nil; current = current.next {
		mac_str := current.mac_addr.String()

		// Extract interface name
		oif_name := string(current.oif_name[:])
		for i, b := range oif_name {
			if b == 0 {
				oif_name = oif_name[:i]
				break
			}
		}

		age := now.Sub(current.updated_at)
		fmt.Printf("%-17s %-6d %-16s %v\n", mac_str, current.vlan_id, oif_name, age.Round(time.Second))
		count++
	}

	if count == 0 {
		fmt.Printf("(empty)\n")
	}
	fmt.Printf("Total entries: %d\n\n", count)
}

// L2 switch performs MAC learning (VLAN-aware)
// Learns the source MAC address, VLAN, and the interface it came from
func l2_switch_perform_mac_learning_vlan(node *Node, src_mac *MacAddr, vlan_id uint16, iif_name string) {
	if node == nil || src_mac == nil {
		return
	}

	// Don't learn broadcast MAC
	if is_mac_broadcast(src_mac) {
		return
	}

	if mac_table_add_or_update_vlan(&node.node_nw_prop.mac_table, src_mac, vlan_id, iif_name) {
		emitNodeEventWithTable(node, "ETHERNET", "mac_table_updated", map[string]string{
			"mac":       src_mac.String(),
			"vlan":      fmt.Sprintf("%d", vlan_id),
			"interface": iif_name,
		}, macTableReference("learned", src_mac.String(), vlan_id, iif_name))
	}
}

// L2 switch forwards a frame based on destination MAC (VLAN-aware)
func l2_switch_forward_frame_vlan(node *Node, recv_intf *Interface, pkt []byte, pkt_size int, vlan_id uint16, routed bool) {
	if node == nil || recv_intf == nil || pkt == nil || pkt_size < ETHERNET_HDR_SIZE {
		return
	}

	node_name := get_node_name(node)

	// Parse Ethernet header
	eth_hdr, err := deserialize_ethernet_header(pkt)
	if err != nil {
		LogError("L2Switch: Node %s: Failed to parse Ethernet header: %v", node_name, err)
		return
	}

	// If destination is broadcast, flood to all interfaces in this VLAN
	if is_mac_broadcast(&eth_hdr.dst_mac) {
		LogDebug("L2Switch: Node %s: Flooding broadcast frame on VLAN %d", node_name, vlan_id)
		l2_switch_flood_frame_vlan(node, recv_intf, pkt, pkt_size, vlan_id)
		return
	}

	// Look up destination MAC in VLAN-aware MAC table
	oif_name := mac_table_lookup_vlan(&node.node_nw_prop.mac_table, &eth_hdr.dst_mac, vlan_id)
	if oif_name == "" {
		// MAC not found - flood to all interfaces in this VLAN
		LogDebug("L2Switch: Node %s: Unknown destination MAC %s on VLAN %d, flooding",
			node_name, eth_hdr.dst_mac.String(), vlan_id)
		l2_switch_flood_frame_vlan(node, recv_intf, pkt, pkt_size, vlan_id)
		return
	}

	// Find the outgoing interface
	oif := get_node_if_by_name(node, oif_name)
	if oif == nil {
		LogError("L2Switch: Node %s: Interface %s not found", node_name, oif_name)
		return
	}

	// Don't send back out the same interface it came in on
	if oif == recv_intf && !routed {
		LogDebug("L2Switch: Node %s: Dropping frame (output interface same as input)", node_name)
		emitInterfaceEventWithTable(node, recv_intf, frameProtocol(pkt), "frame_dropped", map[string]string{
			"destinationMac": eth_hdr.dst_mac.String(),
			"reason":         "egress_matches_ingress",
			"vlan":           fmt.Sprintf("%d", vlan_id),
		}, macTableReference("hit", eth_hdr.dst_mac.String(), vlan_id, oif_name))
		return
	}

	// Never relay onto a port the spanning tree is holding down.
	if !stp_port_can_forward(node, oif) {
		LogDebug("L2Switch: Node %s: Egress port %s is not forwarding - dropping frame",
			node_name, oif_name)
		emitInterfaceEvent(node, oif, frameProtocol(pkt), "frame_dropped", map[string]string{
			"destinationMac": eth_hdr.dst_mac.String(),
			"reason":         "stp_port_not_forwarding",
			"stpState":       stp_port_state_name(node, oif),
		})
		return
	}

	// MANDATORY: Check if output interface allows this VLAN
	// For ACCESS mode: interface MUST have a VLAN configured and match
	// For TRUNK mode: VLAN must be in allowed list
	if !oif.IsVLANAllowed(vlan_id) {
		LogDebug("L2Switch: Node %s: VLAN %d not allowed on output interface %s - dropping frame",
			node_name, vlan_id, oif_name)
		emitInterfaceEventWithTable(node, oif, frameProtocol(pkt), "frame_dropped", map[string]string{
			"destinationMac": eth_hdr.dst_mac.String(),
			"reason":         "vlan_not_allowed",
			"vlan":           fmt.Sprintf("%d", vlan_id),
		}, macTableReference("hit", eth_hdr.dst_mac.String(), vlan_id, oif_name))
		return
	}

	// Prepare frame for output interface (add/remove VLAN tags as needed)
	out_pkt, err := prepare_frame_for_interface(oif, pkt, vlan_id)
	if err != nil {
		LogError("L2Switch: Node %s: Failed to prepare frame for interface: %v", node_name, err)
		return
	}

	// Forward frame out the specific interface
	LogDebug("L2Switch: Node %s: Forwarding to %s via %s (VLAN %d)",
		node_name, eth_hdr.dst_mac.String(), oif_name, vlan_id)
	emitInterfaceEventWithTable(node, oif, frameProtocol(out_pkt), "frame_forwarding_started", map[string]string{
		"destinationMac": eth_hdr.dst_mac.String(),
		"ingress":        get_interface_name(recv_intf),
		"vlan":           fmt.Sprintf("%d", vlan_id),
	}, macTableReference("hit", eth_hdr.dst_mac.String(), vlan_id, oif_name))
	err_send := send_frame(out_pkt, len(out_pkt), oif)
	if err_send != nil {
		LogError("L2Switch: Node %s: Error forwarding frame: %v", node_name, err_send)
	}
}

// L2 switch floods a frame to all interfaces in a VLAN except the incoming one
func l2_switch_flood_frame_vlan(node *Node, exempted_intf *Interface, pkt []byte, pkt_size int, vlan_id uint16) {
	if node == nil || pkt == nil || pkt_size <= 0 {
		return
	}

	node_name := get_node_name(node)
	sent_count := 0
	flood_fields := map[string]string{
		"ingress": get_interface_name(exempted_intf),
		"vlan":    fmt.Sprintf("%d", vlan_id),
	}
	var tableReference *EventTableReference
	if eth_hdr, _, _, err := parse_ethernet_frame_with_vlan(pkt); err == nil {
		flood_fields["destinationMac"] = eth_hdr.dst_mac.String()
		if is_mac_broadcast(&eth_hdr.dst_mac) {
			flood_fields["reason"] = "broadcast_destination"
			tableReference = macTableReference("broadcast", eth_hdr.dst_mac.String(), vlan_id, "")
		} else {
			flood_fields["reason"] = "unknown_destination_mac"
			tableReference = macTableReference("miss", eth_hdr.dst_mac.String(), vlan_id, "")
		}
	}
	emitInterfaceEventWithTable(node, exempted_intf, frameProtocol(pkt), "frame_flooding_started", flood_fields, tableReference)

	// Iterate through all interfaces
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := node.intf[i]

		// Skip if interface doesn't exist
		if intf == nil {
			continue
		}

		// Skip if this is the incoming interface
		if intf == exempted_intf {
			continue
		}

		// Skip if interface is in L3 mode (switches don't forward out L3 interfaces)
		if IS_INTF_L3_MODE(intf) {
			continue
		}

		// Skip ports the spanning tree is holding down. This is what stops a
		// broadcast from circulating forever around a redundant topology.
		if !stp_port_can_forward(node, intf) {
			continue
		}

		// MANDATORY: Skip if this VLAN is not allowed on this interface
		// For ACCESS mode: only flood if VLAN matches configured VLAN
		// For TRUNK mode: only flood if VLAN is in allowed list
		if !intf.IsVLANAllowed(vlan_id) {
			continue
		}

		// Prepare frame for output interface (add/remove VLAN tags as needed)
		out_pkt, err := prepare_frame_for_interface(intf, pkt, vlan_id)
		if err != nil {
			LogError("L2Switch: Node %s: Failed to prepare frame for %s: %v",
				node_name, get_interface_name(intf), err)
			continue
		}

		// Send frame out this interface
		err = send_frame(out_pkt, len(out_pkt), intf)
		if err != nil {
			LogError("L2Switch: Node %s: Error flooding to %s: %v",
				node_name, get_interface_name(intf), err)
		} else {
			sent_count++
		}
	}

	LogDebug("L2Switch: Node %s: Flooded frame to %d interfaces (VLAN %d)", node_name, sent_count, vlan_id)
	if sent_count == 0 && flood_fields["reason"] == "broadcast_destination" {
		emitInterfaceEventWithTable(node, exempted_intf, frameProtocol(pkt), "frame_flooding_completed", map[string]string{
			"forwardedPorts": "0",
			"outcome":        "no_additional_egress",
			"vlan":           fmt.Sprintf("%d", vlan_id),
		}, tableReference)
	} else if sent_count == 0 {
		emitInterfaceEventWithTable(node, exempted_intf, frameProtocol(pkt), "frame_dropped", map[string]string{
			"reason": "no_eligible_egress",
			"vlan":   fmt.Sprintf("%d", vlan_id),
		}, tableReference)
	}
}

// L2 switch receives and processes a frame (VLAN-aware)
// This is the main entry point for L2 switching
func l2_switch_recv_frame(node *Node, iif *Interface, pkt []byte, pkt_size int) {
	if node == nil || iif == nil || pkt == nil || pkt_size < ETHERNET_HDR_SIZE {
		return
	}

	node_name := get_node_name(node)
	iif_name := get_interface_name(iif)

	LogDebug("L2Switch: Node %s: Received frame on %s (%d bytes)",
		node_name, iif_name, pkt_size)

	// A blocked, listening or disabled port discards data frames outright.
	// BPDUs never reach here: they are consumed further up the receive path,
	// which is what lets a blocked port keep tracking the spanning tree.
	if !stp_port_can_learn(node, iif) {
		LogDebug("L2Switch: Node %s: Port %s is not learning - dropping frame",
			node_name, iif_name)
		emitInterfaceEvent(node, iif, frameProtocol(pkt), "frame_dropped", map[string]string{
			"reason":   "stp_port_not_forwarding",
			"stpState": stp_port_state_name(node, iif),
		})
		return
	}

	// Determine VLAN ID based on interface mode and frame content
	vlan_id := determine_frame_vlan(iif, pkt)

	// MANDATORY: Check if VLAN is allowed on incoming interface
	// For ACCESS mode: interface MUST have a VLAN configured
	// For TRUNK mode: VLAN must be in allowed list
	if !iif.IsVLANAllowed(vlan_id) {
		LogDebug("L2Switch: Node %s: VLAN %d not allowed on interface %s - dropping frame",
			node_name, vlan_id, iif_name)
		emitInterfaceEvent(node, iif, frameProtocol(pkt), "frame_dropped", map[string]string{
			"reason": "vlan_not_allowed",
			"vlan":   fmt.Sprintf("%d", vlan_id),
		})
		return
	}

	LogDebug("L2Switch: Node %s: Frame on %s belongs to VLAN %d",
		node_name, iif_name, vlan_id)

	// Parse Ethernet header to extract source MAC
	eth_hdr, _, _, err := parse_ethernet_frame_with_vlan(pkt)
	if err != nil {
		LogError("L2Switch: Node %s: Failed to parse Ethernet header: %v", node_name, err)
		return
	}

	// Perform VLAN-aware MAC learning (learn source MAC + VLAN on incoming interface)
	l2_switch_perform_mac_learning_vlan(node, &eth_hdr.src_mac, vlan_id, iif_name)

	// A learning port populates the MAC table but does not yet relay traffic.
	if !stp_port_can_forward(node, iif) {
		LogDebug("L2Switch: Node %s: Port %s is learning - frame not forwarded",
			node_name, iif_name)
		emitInterfaceEvent(node, iif, frameProtocol(pkt), "frame_dropped", map[string]string{
			"reason":   "stp_port_learning",
			"stpState": stp_port_state_name(node, iif),
		})
		return
	}

	if eth_hdr.ethertype == ETHERTYPE_ARP {
		local_pkt := pkt
		if is_frame_vlan_tagged(pkt) {
			local_pkt, err = remove_vlan_tag(pkt)
			if err != nil {
				LogError("L2Switch: Node %s: Failed to remove VLAN tag from ARP frame: %v", node_name, err)
				return
			}
		}

		if is_mac_broadcast(&eth_hdr.dst_mac) {
			layer_2_frame_recv_arp(node, iif, local_pkt, len(local_pkt))
			l2_switch_flood_frame_vlan(node, iif, pkt, pkt_size, vlan_id)
			return
		}

		if node_owns_mac(node, &eth_hdr.dst_mac) {
			layer_2_frame_recv_arp(node, iif, local_pkt, len(local_pkt))
			return
		}

		l2_switch_forward_frame_vlan(node, iif, pkt, pkt_size, vlan_id, false)
		return
	}

	// Check if this frame needs inter-VLAN routing
	// This happens when:
	// 1. Frame is an IP packet (EtherType 0x0800)
	// 2. Destination IP belongs to a different VLAN that has an SVI configured on this node
	if eth_hdr.ethertype == ETHERTYPE_IP {
		LogDebug("L2Switch: IP packet detected on VLAN %d, checking if inter-VLAN routing needed", vlan_id)
		if should_route_between_vlans(node, pkt, vlan_id) {
			LogInfo("L2Switch: Inter-VLAN routing triggered for VLAN %d", vlan_id)
			// Route between VLANs instead of switching
			route_between_vlans(node, iif, pkt, pkt_size, vlan_id)
			return
		}
		LogDebug("L2Switch: No inter-VLAN routing needed, continuing with L2 switching")
	}

	// Forward the frame based on destination MAC within the VLAN
	l2_switch_forward_frame_vlan(node, iif, pkt, pkt_size, vlan_id, false)
}

func node_owns_mac(node *Node, mac_addr *MacAddr) bool {
	if node == nil || mac_addr == nil {
		return false
	}

	for _, intf := range node.intf {
		if intf != nil && mac_addr_equal(intf.GetMac(), mac_addr) {
			return true
		}
	}

	return false
}

// starts MAC table cleanup goroutine for a node
func start_mac_table_cleanup(node *Node) {
	if node == nil {
		return
	}

	stop_ch := make(chan bool)
	node.mac_cleanup_stop_ch = stop_ch
	node.background_wait_group.Add(1)

	node_name := get_node_name(node)
	LogInfo("L2Switch: Starting MAC table cleanup goroutine for node %s (interval: %v, timeout: %v)",
		node_name, MAC_TABLE_CLEANUP_INTERVAL, MAC_TABLE_ENTRY_TIMEOUT)

	// Start cleanup goroutine
	go func(stop_ch <-chan bool) {
		defer node.background_wait_group.Done()
		ticker := time.NewTicker(MAC_TABLE_CLEANUP_INTERVAL)
		defer ticker.Stop()

		for {
			select {
			case <-stop_ch:
				LogInfo("L2Switch: Stopping MAC table cleanup goroutine for node %s", node_name)
				return
			case <-ticker.C:
				// Run cleanup
				removed := mac_table_cleanup_expired(&node.node_nw_prop.mac_table)
				if removed > 0 {
					LogDebug("L2Switch: Cleanup removed %d MAC entries for node %s", removed, node_name)
				}
			}
		}
	}(stop_ch)
}

// stops MAC table cleanup goroutine for a node
func stop_mac_table_cleanup(node *Node) {
	if node == nil || node.mac_cleanup_stop_ch == nil {
		return
	}

	stop_ch := node.mac_cleanup_stop_ch
	node.mac_cleanup_stop_ch = nil
	close(stop_ch)
}

// ====== Inter-VLAN Routing (SVI) ======

// should_route_between_vlans checks if a frame needs inter-VLAN routing
// Returns true if:
//  1. The frame contains an IP packet
//  2. Either the destination IP belongs to a different VLAN of ours, or the
//     sender addressed the frame to one of our MACs (it is using this node as
//     its gateway) and the routing table has a way to reach the destination
//  3. This node has an SVI for the VLAN the traffic has to leave through
func should_route_between_vlans(node *Node, pkt []byte, src_vlan uint16) bool {
	if node == nil || pkt == nil {
		return false
	}

	// Extract IP header from the frame
	// Skip Ethernet header (14 bytes) or VLAN-tagged Ethernet header (18 bytes)
	ip_offset := ETHERNET_HDR_SIZE
	if len(pkt) >= VLAN_HEADER_SIZE && pkt[12] == 0x81 && pkt[13] == 0x00 {
		ip_offset = VLAN_HEADER_SIZE
	}

	if len(pkt) < ip_offset+20 {
		LogDebug("should_route: packet too small")
		return false // Not enough data for IP header
	}

	// Extract destination IP address (offset 16-19 in IP header)
	dst_ip := [4]byte{
		pkt[ip_offset+16],
		pkt[ip_offset+17],
		pkt[ip_offset+18],
		pkt[ip_offset+19],
	}

	LogDebug("should_route: checking dst_ip=%d.%d.%d.%d, src_vlan=%d",
		dst_ip[0], dst_ip[1], dst_ip[2], dst_ip[3], src_vlan)

	// Check each VLAN interface to see if destination IP belongs to it
	vlan_interfaces := node.GetVlanInterfacesSnapshot()
	if len(vlan_interfaces) == 0 {
		LogDebug("should_route: no VLAN interfaces configured")
		return false
	}

	for vlan_id, vlan_intf := range vlan_interfaces {
		// Skip source VLAN (same VLAN doesn't need routing)
		if vlan_id == src_vlan {
			LogDebug("should_route: skipping source VLAN %d", vlan_id)
			continue
		}

		LogDebug("should_route: checking VLAN %d (%s/%d)",
			vlan_id, vlan_intf.ip_addr.String(), vlan_intf.mask)

		// Check if destination IP is in this VLAN's subnet
		if ip_in_subnet(dst_ip, vlan_intf.ip_addr, vlan_intf.mask) {
			LogInfo("should_route: YES! dst_ip in VLAN %d subnet", vlan_id)
			return true
		}
	}

	// The destination sits on none of our VLANs, but a sender that addressed the
	// frame to one of our MACs is asking this node to route it onwards, which is
	// what the routing table is for. Packets aimed at our own addresses are left
	// alone: those are for this node, not through it.
	if _, has_gateway := vlan_interfaces[src_vlan]; !has_gateway {
		LogDebug("should_route: NO - no SVI on ingress VLAN %d to route on behalf of", src_vlan)
		return false
	}

	eth_hdr, err := deserialize_ethernet_header(pkt)
	if err != nil || !node_owns_mac(node, &eth_hdr.dst_mac) {
		LogDebug("should_route: NO - frame is not addressed to this node")
		return false
	}

	dst_ip_uint32 := binary.BigEndian.Uint32(dst_ip[:])
	if node_owns_ip(node, dst_ip_uint32) {
		LogDebug("should_route: NO - destination is one of our own addresses")
		return false
	}

	if node.node_nw_prop.rt_table != nil &&
		node.node_nw_prop.rt_table.LookupLPM(dst_ip_uint32) != nil {
		LogInfo("should_route: YES! routing table has a route to %d.%d.%d.%d",
			dst_ip[0], dst_ip[1], dst_ip[2], dst_ip[3])
		return true
	}

	LogDebug("should_route: NO - destination not in any other VLAN and no route")
	return false
}

// node_owns_ip reports whether an address belongs to this node, counting the
// SVIs that IsLayer3LocalDelivery does not know about.
func node_owns_ip(node *Node, ip uint32) bool {
	if node == nil {
		return false
	}

	if IsLayer3LocalDelivery(node, ip) {
		return true
	}

	var addr [4]byte
	binary.BigEndian.PutUint32(addr[:], ip)
	for _, vlan_intf := range node.GetVlanInterfacesSnapshot() {
		if vlan_intf.ip_addr == IpAddr(addr) {
			return true
		}
	}

	return false
}

// report_inter_vlan_icmp_error sends an ICMP error about a packet that failed
// inter-VLAN routing. A switch has no routing table to send the report through,
// so it goes straight back out the ingress port, sourced from the SVI of the
// VLAN the packet arrived on — the address the sender used as its gateway.
func report_inter_vlan_icmp_error(node *Node, iif *Interface, src_vlan uint16, ip_pkt []byte, icmp_type, icmp_code uint8) {
	if node == nil || iif == nil || len(ip_pkt) < 20 {
		return
	}

	src_vlan_intf, ok := node.GetVlanInterfacesSnapshot()[src_vlan]
	if !ok {
		LogDebug("InterVLAN: Node %s: No SVI for VLAN %d, cannot report ICMP error",
			get_node_name(node), src_vlan)
		return
	}

	src_ip, err := IPStringToUint32(src_vlan_intf.ip_addr.String())
	if err != nil {
		return
	}

	orig_hdr, err := DeserializeIPHeader(ip_pkt)
	if err != nil {
		return
	}

	sendICMPErrorViaInterface(node, iif, src_ip, ip_pkt, orig_hdr, icmp_type, icmp_code)
}

// ip_in_subnet checks if an IP address is in the given subnet
func ip_in_subnet(ip [4]byte, subnet_ip IpAddr, mask byte) bool {
	LogDebug("ip_in_subnet: checking if %d.%d.%d.%d is in %s/%d",
		ip[0], ip[1], ip[2], ip[3], subnet_ip.String(), mask)

	// Apply mask to both IPs and compare
	for i := 0; i < 4; i++ {
		// Calculate which bits of this byte need to be masked
		// For /24: bytes 0,1,2 are fully masked, byte 3 is not masked
		bits_used := int(mask) - (i * 8)

		var mask_bits byte
		if bits_used >= 8 {
			// Fully mask this byte
			mask_bits = 0xFF
		} else if bits_used > 0 {
			// Partially mask this byte
			mask_bits = byte(0xFF << (8 - bits_used))
		} else {
			// Don't mask this byte
			mask_bits = 0
		}

		LogDebug("ip_in_subnet: byte[%d]: ip=%d & mask_bits=%d = %d, subnet=%d & mask_bits=%d = %d",
			i, ip[i], mask_bits, ip[i]&mask_bits, subnet_ip[i], mask_bits, subnet_ip[i]&mask_bits)

		if (ip[i] & mask_bits) != (subnet_ip[i] & mask_bits) {
			LogDebug("ip_in_subnet: NO MATCH at byte %d", i)
			return false
		}
	}
	LogDebug("ip_in_subnet: MATCH!")
	return true
}

// route_between_vlans handles routing an IP packet from one VLAN to another
// This implements inter-VLAN routing (SVI functionality)
func route_between_vlans(node *Node, iif *Interface, pkt []byte, pkt_size int, src_vlan uint16) {
	if node == nil || pkt == nil {
		return
	}

	node_name := get_node_name(node)

	// Extract IP header
	ip_offset := ETHERNET_HDR_SIZE
	if len(pkt) >= VLAN_HEADER_SIZE && pkt[12] == 0x81 && pkt[13] == 0x00 {
		ip_offset = VLAN_HEADER_SIZE
	}

	if len(pkt) < ip_offset+20 {
		LogError("InterVLAN: Node %s: Packet too small for IP header", node_name)
		return
	}

	// Locate the IP packet (strip Ethernet/VLAN headers). A private forwarding
	// copy is made below, immediately before TTL and checksum are changed.
	received_ip_pkt := pkt[ip_offset:]
	ip_hdr, err := DeserializeIPHeader(received_ip_pkt)
	if err != nil {
		LogError("InterVLAN: Node %s: Failed to parse IP header: %v", node_name, err)
		return
	}
	// A frame padded out to the minimum Ethernet size carries trailing bytes
	// that are not part of the packet; forwarding them on would corrupt it.
	if total_len := int(ip_hdr.TotalLen); total_len >= 20 && total_len <= len(received_ip_pkt) {
		received_ip_pkt = received_ip_pkt[:total_len]
	}
	ip_pkt := received_ip_pkt

	dst_ip_string := IPUint32ToString(ip_hdr.DstIP)
	protocol := frameProtocol(pkt)

	// The routing table is the authority on where the packet goes: the
	// destination is often on one of our own VLANs, but it may equally sit
	// behind a gateway that only a static or learned route knows about.
	route := (*L3Route)(nil)
	if node.node_nw_prop.rt_table != nil {
		route = node.node_nw_prop.rt_table.LookupLPM(ip_hdr.DstIP)
	}
	if route == nil {
		LogDebug("InterVLAN: Node %s: No route to %s", node_name, dst_ip_string)
		emitInterfaceEventWithTable(node, iif, protocol, "packet_dropped", map[string]string{
			"destinationIp": dst_ip_string,
			"reason":        "no_route",
		}, routingTableReference(nil, dst_ip_string, "miss"))
		report_inter_vlan_icmp_error(node, iif, src_vlan, ip_pkt,
			ICMP_TYPE_DEST_UNREACHABLE, ICMP_CODE_NET_UNREACHABLE)
		return
	}

	// A direct route puts the destination itself on the wire; otherwise the
	// frame is addressed to the gateway that route names.
	next_hop_ip := ip_hdr.DstIP
	if !route.IsDirect && route.GatewayIP != "" {
		gateway_ip, gw_err := IPStringToUint32(route.GatewayIP)
		if gw_err != nil {
			LogError("InterVLAN: Node %s: Invalid gateway %s", node_name, route.GatewayIP)
			return
		}
		next_hop_ip = gateway_ip
	}
	next_hop_string := IPUint32ToString(next_hop_ip)

	// Work out how the next hop is reached before spending the TTL, so a packet
	// that turns out to be unforwardable can still be quoted as it arrived.
	dest_vlan, via_vlan := svi_egress_vlan(node, route, next_hop_ip)
	var routed_intf *Interface
	if !via_vlan {
		// The route may instead point at a routed (non-switched) port.
		routed_intf = get_node_if_by_name(node, route.OIF)
		if routed_intf == nil {
			LogError("InterVLAN: Node %s: No egress found for %s via %s",
				node_name, dst_ip_string, route.OIF)
			emitInterfaceEventWithTable(node, iif, protocol, "packet_dropped", map[string]string{
				"destinationIp": dst_ip_string,
				"reason":        "no_egress_interface",
			}, routingTableReference(route, dst_ip_string, "hit"))
			report_inter_vlan_icmp_error(node, iif, src_vlan, ip_pkt,
				ICMP_TYPE_DEST_UNREACHABLE, ICMP_CODE_HOST_UNREACHABLE)
			return
		}
	}

	route_fields := map[string]string{
		"destinationIp":    dst_ip_string,
		"egressInterface":  route.OIF,
		"ingressInterface": get_interface_name(iif),
		"nextHop":          next_hop_string,
		"route":            fmt.Sprintf("%s/%d", route.Dest, route.Mask),
		"routeSource":      RouteSourceToString(route.Source),
		"sourceIp":         IPUint32ToString(ip_hdr.SrcIP),
		"sourceVlan":       fmt.Sprintf("%d", src_vlan),
		"ttlBefore":        fmt.Sprintf("%d", ip_hdr.TTL),
	}
	if ip_hdr.TTL > 0 {
		route_fields["ttlAfter"] = fmt.Sprintf("%d", ip_hdr.TTL-1)
	}
	if via_vlan {
		route_fields["destinationVlan"] = fmt.Sprintf("%d", dest_vlan)
	} else {
		route_fields["interface"] = get_interface_name(routed_intf)
	}
	emitInterfaceEventWithTable(node, iif, protocol, "inter_vlan_route_selected",
		route_fields, routingTableReference(route, dst_ip_string, "hit"))

	// Decrement TTL
	if ip_hdr.TTL <= 1 {
		LogDebug("InterVLAN: Node %s: TTL expired, dropping packet", node_name)
		emitInterfaceEvent(node, iif, protocol, "packet_dropped", map[string]string{
			"destinationIp": dst_ip_string,
			"reason":        "ttl_expired",
		})
		// The SVI of the ingress VLAN is the gateway the sender addressed, so
		// it is the address the Time Exceeded report should come from.
		report_inter_vlan_icmp_error(node, iif, src_vlan, ip_pkt,
			ICMP_TYPE_TIME_EXCEEDED, ICMP_CODE_TTL_EXCEEDED)
		return
	}
	// The receive buffer can still be observed by the ingress path and other
	// consumers. Route a private copy so TTL/checksum updates do not mutate it.
	ip_pkt = append([]byte(nil), ip_pkt...)
	ip_pkt[8]--
	// The TTL is covered by the header checksum, so the checksum has to be
	// recomputed or the next hop will discard the packet as corrupt.
	setIPHeaderChecksum(ip_pkt[:GetIPHeaderLen(ip_hdr)])

	if !via_vlan {
		emitInterfaceEvent(node, routed_intf, protocol, "inter_vlan_forwarding_started", map[string]string{
			"destinationIp": dst_ip_string,
			"nextHop":       next_hop_string,
			"sourceVlan":    fmt.Sprintf("%d", src_vlan),
		})
		DemotePacketToLayer2(node, next_hop_ip, route.OIF, ip_pkt, len(ip_pkt), ETHERTYPE_IP)
		return
	}

	emitNodeEvent(node, protocol, "inter_vlan_forwarding_started", map[string]string{
		"destinationIp":   dst_ip_string,
		"destinationVlan": fmt.Sprintf("%d", dest_vlan),
		"egressInterface": route.OIF,
		"nextHop":         next_hop_string,
		"sourceIp":        IPUint32ToString(ip_hdr.SrcIP),
		"sourceVlan":      fmt.Sprintf("%d", src_vlan),
		"ttl":             fmt.Sprintf("%d", ip_pkt[8]),
	})
	forward_routed_packet_in_vlan(node, iif, dest_vlan, next_hop_ip, ip_pkt)
}

// svi_egress_vlan works out which VLAN a routed packet has to be sent into.
// A route installed by an SVI names its interface "vlan<id>"; for anything else
// the next hop is matched against the SVI subnets.
func svi_egress_vlan(node *Node, route *L3Route, next_hop_ip uint32) (uint16, bool) {
	if node == nil || route == nil {
		return 0, false
	}

	vlan_interfaces := node.GetVlanInterfacesSnapshot()

	if strings.HasPrefix(route.OIF, "vlan") {
		if vlan_id, err := strconv.ParseUint(route.OIF[len("vlan"):], 10, 16); err == nil {
			if _, ok := vlan_interfaces[uint16(vlan_id)]; ok {
				return uint16(vlan_id), true
			}
		}
	}

	var next_hop [4]byte
	binary.BigEndian.PutUint32(next_hop[:], next_hop_ip)
	for vlan_id, vlan_intf := range vlan_interfaces {
		if ip_in_subnet(next_hop, vlan_intf.ip_addr, vlan_intf.mask) {
			return vlan_id, true
		}
	}

	return 0, false
}

// forward_routed_packet_in_vlan puts a routed IP packet back on the wire inside
// the destination VLAN. The egress port comes from the MAC table via the normal
// switching path, so a VLAN with several access ports reaches the port the
// next hop actually lives behind rather than whichever port is configured first.
func forward_routed_packet_in_vlan(node *Node, iif *Interface, dest_vlan uint16, next_hop_ip uint32, ip_pkt []byte) {
	if node == nil || len(ip_pkt) == 0 {
		return
	}

	node_name := get_node_name(node)
	next_hop_string := IPUint32ToString(next_hop_ip)
	var next_hop_addr IpAddr
	if !set_ip_addr(&next_hop_addr, next_hop_string) {
		return
	}

	frame := build_routed_frame(nil, vlan_source_mac(node, dest_vlan, ""), ip_pkt)
	if frame == nil {
		return
	}

	dst_mac, request_needed, queued := arp_table_resolve_or_queue(
		&node.node_nw_prop.arp_table,
		&next_hop_addr,
		fmt.Sprintf("vlan%d", dest_vlan),
		frame,
		len(frame),
		func(n *Node, iface *Interface, packet []byte, size int) {
			if size < ETHERNET_HDR_SIZE {
				return
			}
			resolved_mac := arp_table_lookup(&n.node_nw_prop.arp_table, &next_hop_addr)
			if resolved_mac == nil {
				return
			}
			copy(packet[0:6], resolved_mac[:])
			if iface != nil {
				if src_mac := iface.GetMac(); src_mac != nil {
					copy(packet[6:12], src_mac[:])
				}
			}
			// Go back through the switching path so the frame is tagged for the
			// egress port and the MAC table gets the final say on which one.
			l2_switch_forward_frame_vlan(n, iif, packet[:size], size, dest_vlan, true)
		},
		func() {
			emitNodeEvent(node, frameProtocol(frame), "frame_dropped", map[string]string{
				"destinationIp": next_hop_string,
				"reason":        "arp_resolution_timeout",
				"vlan":          fmt.Sprintf("%d", dest_vlan),
			})
		},
		func() {
			send_vlan_arp_request(node, dest_vlan, next_hop_string)
		},
	)
	if dst_mac != nil {
		oif_name := mac_table_lookup_vlan(&node.node_nw_prop.mac_table, dst_mac, dest_vlan)
		resolved_frame := build_routed_frame(dst_mac, vlan_source_mac(node, dest_vlan, oif_name), ip_pkt)
		if resolved_frame == nil {
			return
		}
		// A MAC table miss floods within the VLAN, exactly as it would for a
		// switched frame; the reply then teaches us the port.
		l2_switch_forward_frame_vlan(node, iif, resolved_frame, len(resolved_frame), dest_vlan, true)
		return
	}
	if !queued {
		LogError("InterVLAN: Node %s: Failed to queue packet pending ARP resolution for %s",
			node_name, next_hop_string)
		return
	}

	// The next hop is not in the ARP cache yet. Queue the packet and ask for the
	// MAC across the whole VLAN: the reply arrives on the one port the next hop
	// is behind, and that is the port the queued packet is released on.
	LogDebug("InterVLAN: Node %s: No ARP entry for %s, queueing packet on VLAN %d",
		node_name, next_hop_string, dest_vlan)
	emitNodeEventWithTable(node, "ARP", "arp_lookup_missed", map[string]string{
		"targetIp": next_hop_string,
		"vlan":     fmt.Sprintf("%d", dest_vlan),
	}, arpTableMissReference(next_hop_string))

	if request_needed {
		send_vlan_arp_request(node, dest_vlan, next_hop_string)
	}
}

// build_routed_frame wraps a routed IP packet in an untagged Ethernet header.
// Any VLAN tag is added later, per egress port, by the switching path.
func build_routed_frame(dst_mac, src_mac *MacAddr, ip_pkt []byte) []byte {
	frame := tag_packet_with_ethernet_hdr(ip_pkt, len(ip_pkt))
	if frame == nil {
		return nil
	}

	if dst_mac != nil {
		frame.header.dst_mac = *dst_mac
	}
	if src_mac != nil {
		frame.header.src_mac = *src_mac
	}
	frame.header.ethertype = ETHERTYPE_IP

	return serialize_ethernet_frame(frame)
}

// vlan_source_mac picks the address a routed frame is sent from: the egress port
// when it is already known, otherwise any port carrying the VLAN.
func vlan_source_mac(node *Node, vlan_id uint16, oif_name string) *MacAddr {
	if node == nil {
		return nil
	}

	if oif_name != "" {
		if oif := get_node_if_by_name(node, oif_name); oif != nil {
			return oif.GetMac()
		}
	}

	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := node.intf[i]
		if intf == nil || IS_INTF_L3_MODE(intf) {
			continue
		}
		if intf.IsVLANAllowed(vlan_id) {
			return intf.GetMac()
		}
	}

	return nil
}

// send_vlan_arp_request asks for a next hop's MAC on every port of a VLAN.
// The request is sourced from the SVI, which is the address the answering host
// knows as its gateway, and from each port's own MAC so that the reply comes
// back addressed to the port it is answering on.
func send_vlan_arp_request(node *Node, vlan_id uint16, target_ip string) {
	if node == nil {
		return
	}

	vlan_intf, ok := node.GetVlanInterfacesSnapshot()[vlan_id]
	if !ok {
		LogError("ARP: Node %s: No SVI for VLAN %d to source a request from",
			get_node_name(node), vlan_id)
		return
	}

	var target_addr IpAddr
	if !set_ip_addr(&target_addr, target_ip) {
		return
	}
	if vlan_intf.ip_addr == target_addr {
		LogError("ARP: Node %s: Cannot resolve own SVI address %s", get_node_name(node), target_ip)
		return
	}

	src_ip, err := IPStringToUint32(vlan_intf.ip_addr.String())
	if err != nil {
		return
	}
	dst_ip, err := IPStringToUint32(target_ip)
	if err != nil {
		return
	}

	var broadcast_mac MacAddr
	layer2_fill_with_broadcast_mac(broadcast_mac[:])

	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := node.intf[i]
		if intf == nil || IS_INTF_L3_MODE(intf) {
			continue
		}
		if !intf.IsVLANAllowed(vlan_id) || !stp_port_can_forward(node, intf) {
			continue
		}

		src_mac := intf.GetMac()
		if src_mac == nil {
			continue
		}

		arp_hdr := &arp_hdr_t{
			hw_type:        ARP_HW_TYPE_ETHERNET,
			proto_type:     ARP_PROTO_TYPE_IP,
			hw_addr_len:    ARP_HW_ADDR_LEN,
			proto_addr_len: ARP_PROTO_ADDR_LEN,
			op_code:        ARP_OP_REQUEST,
			src_mac:        *src_mac,
			src_ip:         src_ip,
			dst_mac:        MacAddr{},
			dst_ip:         dst_ip,
		}

		frame := build_routed_frame(&broadcast_mac, src_mac, serialize_arp_header(arp_hdr))
		if frame == nil {
			continue
		}
		frame[12] = byte(ETHERTYPE_ARP >> 8)
		frame[13] = byte(ETHERTYPE_ARP & 0xff)

		out_pkt, prep_err := prepare_frame_for_interface(intf, frame, vlan_id)
		if prep_err != nil {
			LogError("ARP: Node %s: Failed to prepare request for %s: %v",
				get_node_name(node), get_interface_name(intf), prep_err)
			continue
		}

		emitInterfaceEventWithTable(node, intf, "ARP", "arp_request_started", map[string]string{
			"senderIp":  vlan_intf.ip_addr.String(),
			"senderMac": src_mac.String(),
			"targetIp":  target_ip,
			"vlan":      fmt.Sprintf("%d", vlan_id),
		}, arpTableReferenceFromTable(&node.node_nw_prop.arp_table, target_ip, "pending"))

		if send_err := send_frame(out_pkt, len(out_pkt), intf); send_err != nil {
			LogError("ARP: Node %s: Failed to send request on %s: %v",
				get_node_name(node), get_interface_name(intf), send_err)
		}
	}
}
