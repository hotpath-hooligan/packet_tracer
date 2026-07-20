package main

import (
	"encoding/binary"
	"fmt"
)

// ====== VLAN Tag Operations (802.1Q) ======

// Parses an Ethernet frame and extracts VLAN tag if present
// Returns: ethernet header, VLAN tag (or nil if untagged), payload offset, error
func parse_ethernet_frame_with_vlan(pkt []byte) (*EthernetHeader, *VlanTag, int, error) {
	if len(pkt) < ETHERNET_HDR_SIZE {
		return nil, nil, 0, fmt.Errorf("packet too small for Ethernet header")
	}

	eth_hdr := &EthernetHeader{}
	offset := 0

	// Parse destination MAC
	copy(eth_hdr.dst_mac[:], pkt[offset:offset+6])
	offset += 6

	// Parse source MAC
	copy(eth_hdr.src_mac[:], pkt[offset:offset+6])
	offset += 6

	// Check if next 2 bytes are VLAN TPID (0x8100)
	ethertype_or_tpid := binary.BigEndian.Uint16(pkt[offset : offset+2])
	offset += 2

	var vlan_tag *VlanTag = nil

	if ethertype_or_tpid == VLAN_TPID {
		// VLAN tag present
		if len(pkt) < VLAN_HEADER_SIZE {
			return nil, nil, 0, fmt.Errorf("packet too small for VLAN tag")
		}

		vlan_tag = &VlanTag{
			tpid: VLAN_TPID,
			tci:  binary.BigEndian.Uint16(pkt[offset : offset+2]),
		}
		offset += 2

		// Real EtherType follows VLAN tag
		eth_hdr.ethertype = binary.BigEndian.Uint16(pkt[offset : offset+2])
		offset += 2
	} else {
		// No VLAN tag, this was the EtherType
		eth_hdr.ethertype = ethertype_or_tpid
	}

	return eth_hdr, vlan_tag, offset, nil
}

// Adds a VLAN tag to an Ethernet frame
// Returns: new frame with VLAN tag inserted
func add_vlan_tag(pkt []byte, vlan_id uint16, pcp byte, dei byte) ([]byte, error) {
	if len(pkt) < ETHERNET_HDR_SIZE {
		return nil, fmt.Errorf("packet too small")
	}

	// Check if already tagged
	ethertype_or_tpid := binary.BigEndian.Uint16(pkt[12:14])
	if ethertype_or_tpid == VLAN_TPID {
		// Already tagged, just update the VLAN ID
		tci := make_tci(pcp, dei, vlan_id)
		binary.BigEndian.PutUint16(pkt[14:16], tci)
		return pkt, nil
	}

	// Create new frame with space for VLAN tag
	new_pkt := make([]byte, len(pkt)+VLAN_TAG_SIZE)

	// Copy destination MAC (6 bytes)
	copy(new_pkt[0:6], pkt[0:6])

	// Copy source MAC (6 bytes)
	copy(new_pkt[6:12], pkt[6:12])

	// Insert VLAN tag (4 bytes)
	binary.BigEndian.PutUint16(new_pkt[12:14], VLAN_TPID)
	tci := make_tci(pcp, dei, vlan_id)
	binary.BigEndian.PutUint16(new_pkt[14:16], tci)

	// Copy rest of frame (EtherType + payload)
	copy(new_pkt[16:], pkt[12:])

	return new_pkt, nil
}

// Removes VLAN tag from an Ethernet frame
// Returns: new frame without VLAN tag
func remove_vlan_tag(pkt []byte) ([]byte, error) {
	if len(pkt) < VLAN_HEADER_SIZE {
		return nil, fmt.Errorf("packet too small for VLAN tag")
	}

	// Check if tagged
	ethertype_or_tpid := binary.BigEndian.Uint16(pkt[12:14])
	if ethertype_or_tpid != VLAN_TPID {
		// Not tagged, return as is
		return pkt, nil
	}

	// Create new frame without VLAN tag
	new_pkt := make([]byte, len(pkt)-VLAN_TAG_SIZE)

	// Copy destination MAC (6 bytes)
	copy(new_pkt[0:6], pkt[0:6])

	// Copy source MAC (6 bytes)
	copy(new_pkt[6:12], pkt[6:12])

	// Copy rest of frame (EtherType from byte 16 + payload)
	copy(new_pkt[12:], pkt[16:])

	return new_pkt, nil
}

// Checks if an Ethernet frame is VLAN tagged (802.1Q)
// This API allows routing devices to detect VLAN tags without inspecting the frame structure
// Returns true if frame has 802.1Q VLAN tag, false otherwise
func is_frame_vlan_tagged(pkt []byte) bool {
	// Need at least 14 bytes (Ethernet header) to check
	if len(pkt) < ETHERNET_HDR_SIZE {
		return false
	}

	// Check bytes at offset 12-13 (EtherType/TPID position)
	// If it's 0x8100, frame is VLAN tagged
	ethertype_or_tpid := binary.BigEndian.Uint16(pkt[12:14])
	return ethertype_or_tpid == VLAN_TPID
}

// Extracts VLAN ID from a packet (returns VLAN_DEFAULT if untagged)
func get_frame_vlan_id(pkt []byte) uint16 {
	if len(pkt) < ETHERNET_HDR_SIZE+2 {
		return VLAN_DEFAULT
	}

	ethertype_or_tpid := binary.BigEndian.Uint16(pkt[12:14])
	if ethertype_or_tpid != VLAN_TPID {
		return VLAN_DEFAULT
	}

	if len(pkt) < VLAN_HEADER_SIZE {
		return VLAN_DEFAULT
	}

	tci := binary.BigEndian.Uint16(pkt[14:16])
	return extract_vlan_id(tci)
}

// Determines the VLAN ID for a frame based on interface mode and frame content
func determine_frame_vlan(iif *Interface, pkt []byte) uint16 {
	if iif == nil {
		return VLAN_DEFAULT
	}

	// L3 mode interfaces don't participate in VLAN switching
	if IS_INTF_L3_MODE(iif) {
		return VLAN_DEFAULT
	}

	mode := iif.GetVLANMode()
	frame_vlan_id := get_frame_vlan_id(pkt)

	switch mode {
	case INTF_MODE_ACCESS:
		if is_frame_vlan_tagged(pkt) {
			return 0
		}
		return iif.GetAccessVLAN()

	case INTF_MODE_TRUNK:
		if is_frame_vlan_tagged(pkt) {
			return frame_vlan_id
		}
		return iif.GetNativeVLAN()

	default:
		return VLAN_DEFAULT
	}
}

// Prepares a frame for transmission on an interface (add/remove VLAN tags as needed)
func prepare_frame_for_interface(oif *Interface, pkt []byte, vlan_id uint16) ([]byte, error) {
	if oif == nil {
		return pkt, nil
	}

	// L3 mode interfaces forward frames as-is
	if IS_INTF_L3_MODE(oif) {
		return pkt, nil
	}

	mode := oif.GetVLANMode()

	switch mode {
	case INTF_MODE_ACCESS:
		// Access port: always remove VLAN tags
		return remove_vlan_tag(pkt)

	case INTF_MODE_TRUNK:
		native_vlan := oif.GetNativeVLAN()
		if vlan_id == native_vlan {
			// Native VLAN: remove tag
			return remove_vlan_tag(pkt)
		} else {
			// Non-native VLAN: ensure tag is present
			return add_vlan_tag(pkt, vlan_id, 0, 0)
		}

	default:
		return pkt, nil
	}
}
