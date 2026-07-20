package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// MacAddr represents a MAC address as 6-byte array
type MacAddr [6]byte

func (mac *MacAddr) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

// parses a MAC address string and sets the MacAddr
func set_mac_addr(mac *MacAddr, mac_str string) bool {
	if mac == nil {
		return false
	}

	// Parse MAC address string (format: xx:xx:xx:xx:xx:xx)
	parts := strings.Split(mac_str, ":")
	if len(parts) != 6 {
		return false
	}

	for i, part := range parts {
		val, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			return false
		}
		mac[i] = byte(val)
	}

	return true
}

func mac_addr_equal(mac1, mac2 *MacAddr) bool {
	if mac1 == nil || mac2 == nil {
		return false
	}

	for i := 0; i < 6; i++ {
		if mac1[i] != mac2[i] {
			return false
		}
	}
	return true
}

// checks if MAC address is broadcast (ff:ff:ff:ff:ff:ff)
func is_broadcast_mac(mac *MacAddr) bool {
	if mac == nil {
		return false
	}

	for i := 0; i < 6; i++ {
		if mac[i] != 0xFF {
			return false
		}
	}
	return true
}

// layer2_fill_with_broadcast_mac fills byte array with broadcast MAC
func layer2_fill_with_broadcast_mac(mac_array []byte) bool {
	if len(mac_array) < 6 {
		return false
	}

	for i := 0; i < 6; i++ {
		mac_array[i] = 0xFF
	}
	return true
}

var macAddressCounter atomic.Uint64

// generates a process-unique, locally administered unicast MAC address
func generate_unique_mac_address() MacAddr {
	value := macAddressCounter.Add(1)
	return MacAddr{
		0x02,
		byte(value >> 32),
		byte(value >> 24),
		byte(value >> 16),
		byte(value >> 8),
		byte(value),
	}
}

// IpAddr represents an IPv4 address as 4-byte array
type IpAddr [4]byte

func parseIPv4(value string) (IpAddr, bool) {
	var ip IpAddr
	octet := 0
	octetIndex := 0
	digits := 0
	leadingZero := false

	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '.' {
			if digits == 0 || octetIndex >= len(ip)-1 {
				return IpAddr{}, false
			}
			ip[octetIndex] = byte(octet)
			octetIndex++
			octet = 0
			digits = 0
			leadingZero = false
			continue
		}
		if character < '0' || character > '9' || digits == 3 || (digits > 0 && leadingZero) {
			return IpAddr{}, false
		}
		if digits == 0 {
			leadingZero = character == '0'
		}
		octet = octet*10 + int(character-'0')
		if octet > 255 {
			return IpAddr{}, false
		}
		digits++
	}

	if octetIndex != len(ip)-1 || digits == 0 {
		return IpAddr{}, false
	}
	ip[octetIndex] = byte(octet)
	return ip, true
}

func (ip *IpAddr) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
}

// parses an IP address string and sets the IpAddr
func set_ip_addr(ip *IpAddr, ip_str string) bool {
	if ip == nil {
		return false
	}

	parsedIP, valid := parseIPv4(ip_str)
	if !valid {
		return false
	}
	*ip = parsedIP
	return true
}

// compares two IP addresses for equality
func ip_addr_equal(ip1, ip2 *IpAddr) bool {
	if ip1 == nil || ip2 == nil {
		return false
	}

	for i := 0; i < 4; i++ {
		if ip1[i] != ip2[i] {
			return false
		}
	}
	return true
}

// converts IP address string to 32-bit integer
func ip_addr_str_to_int32(ip_str string, result *uint32) bool {
	if result == nil {
		return false
	}

	ip, valid := parseIPv4(ip_str)
	if !valid {
		return false
	}
	*result = uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	return true
}

// converts 32-bit integer to IP address string
func ip_addr_int32_to_str(ip_int uint32, result []byte) bool {
	if len(result) < 16 { // Need at least 15 chars + null terminator
		return false
	}

	ip_str := fmt.Sprintf("%d.%d.%d.%d",
		(ip_int>>24)&0xFF,
		(ip_int>>16)&0xFF,
		(ip_int>>8)&0xFF,
		ip_int&0xFF)

	copy(result, []byte(ip_str))

	// Null-terminate if there's space
	if len(result) > len(ip_str) {
		result[len(ip_str)] = 0
	}

	return true
}

// finds interface on node matching the given IP subnet
func node_get_matching_subnet_interface(node *Node, ip_addr string) *Interface {
	if node == nil {
		return nil
	}

	targetIP, valid := parseIPv4(ip_addr)
	if !valid {
		return nil
	}

	// Check each interface on the node
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := node.intf[i]
		if intf == nil {
			continue
		}

		// Skip if interface doesn't have IP configured
		if !intf.IsIPConfigured() {
			continue
		}

		// Get interface IP and mask
		intf_ip := intf.GetIP()
		mask := intf.GetMask()

		// A /0 is useful as a routing-table default, but it cannot identify a
		// directly connected interface: allowing it here would select this
		// interface for every IPv4 destination and bypass route selection.
		if intf_ip == nil || mask == 0 || mask > 32 {
			continue
		}

		// Calculate network addresses for both IPs using the interface mask
		var intf_net, target_net uint32

		// Interface network
		intf_ip_int := uint32(intf_ip[0])<<24 | uint32(intf_ip[1])<<16 | uint32(intf_ip[2])<<8 | uint32(intf_ip[3])
		subnet_mask := ^uint32(0) << (32 - mask)
		intf_net = intf_ip_int & subnet_mask

		// Target network
		target_ip_int := uint32(targetIP[0])<<24 | uint32(targetIP[1])<<16 | uint32(targetIP[2])<<8 | uint32(targetIP[3])
		target_net = target_ip_int & subnet_mask

		// Check if they're in the same subnet
		if intf_net == target_net {
			return intf
		}
	}

	return nil
}
