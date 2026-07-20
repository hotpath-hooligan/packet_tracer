package main

import (
	"time"
)

// sends a packet out of all interfaces of a node except the exempted interface
// Returns the number of interfaces the packet was sent out on, or -1 on error
func send_pkt_flood(node *Node, exempted_intf *Interface, pkt []byte, pkt_size int) int {
	if node == nil {
		LogError("Error: Node cannot be nil")
		return -1
	}

	if pkt == nil {
		LogError("Error: Packet buffer cannot be nil")
		return -1
	}

	if pkt_size <= 0 || pkt_size > len(pkt) {
		LogError("Error: Invalid packet size: %d", pkt_size)
		return -1
	}

	sent_count := 0
	node_name := get_node_name(node)

	LogDebug("FLOOD: Node %s: Starting packet flood (size: %d bytes)", node_name, pkt_size)

	// Iterate through all interfaces of the node
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := node.intf[i]

		// Skip if interface doesn't exist
		if intf == nil {
			continue
		}

		// Skip if this is the exempted interface
		if exempted_intf != nil && intf == exempted_intf {
			LogDebug("FLOOD: Node %s: Skipping exempted interface %s",
				node_name, get_interface_name(intf))
			continue
		}

		// Skip if interface has no attached node (shouldn't happen, but safety check)
		if intf.att_node == nil {
			LogDebug("FLOOD: Node %s: Skipping interface %s (no attached node)",
				node_name, get_interface_name(intf))
			continue
		}

		// Check if interface has a neighbor
		nbr_node := get_nbr_node(intf)
		if nbr_node == nil {
			LogDebug("FLOOD: Node %s: Skipping interface %s (no neighbor)",
				node_name, get_interface_name(intf))
			continue
		}

		// Send packet out through this interface
		LogDebug("FLOOD: Node %s: Sending packet via %s -> %s",
			node_name, get_interface_name(intf), get_node_name(nbr_node))

		err := send_frame(pkt, pkt_size, intf)
		if err != nil {
			LogError("FLOOD: Error sending packet via %s: %v",
				get_interface_name(intf), err)
			// Continue with other interfaces even if one fails
			continue
		}

		sent_count++
	}

	if sent_count == 0 {
		LogWarn("FLOOD: Node %s: No packets sent (no valid interfaces)", node_name)
	} else {
		LogDebug("FLOOD: Node %s: Successfully sent packet to %d interfaces",
			node_name, sent_count)
	}

	return sent_count
}

// log packet transmission for debugging/statistics
func log_packet_transmission(src_node, dst_node *Node, src_intf, dst_intf *Interface, packet_size int) {
	timestamp := getCurrentTimestamp()
	LogDebug("LOG: %d: Packet - Source: %s[%s] -> Destination: %s[%s], Size: %d bytes",
		timestamp,
		get_node_name(src_node), get_interface_name(src_intf),
		get_node_name(dst_node), get_interface_name(dst_intf),
		packet_size)
}

func getCurrentTimestamp() int64 {
	return time.Now().Unix()
}
