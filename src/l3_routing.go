package main

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// Protocol numbers (from tcpconst.h)
const (
	PROTO_ICMP     = 1
	PROTO_MTCP     = 20
	PROTO_USERAPP1 = 21
	PROTO_IP_IN_IP = 4
)

// IPHeader represents the IPv4 header (20 bytes without options)
type IPHeader struct {
	Version    uint8  // 4 bits: IP version (4 for IPv4)
	IHL        uint8  // 4 bits: Header length in 32-bit words (5 for 20-byte header without options)
	TOS        uint8  // Type of Service
	TotalLen   uint16 // Total length of IP packet (header + payload)
	ID         uint16 // Identification
	Flags      uint8  // 3 bits: Unused, DF, MF flags
	FragOffset uint16 // 13 bits: Fragment offset
	TTL        uint8  // Time to Live
	Protocol   uint8  // Protocol (1=ICMP, 6=TCP, 17=UDP, etc.)
	Checksum   uint16 // Header checksum
	SrcIP      uint32 // Source IP address
	DstIP      uint32 // Destination IP address
}

// InitializeIPHeader initializes an IP header with default values
func InitializeIPHeader(hdr *IPHeader) {
	hdr.Version = 4
	hdr.IHL = 5 // 5 * 4 = 20 bytes (no options)
	hdr.TOS = 0
	hdr.TotalLen = 0 // To be filled by caller
	hdr.ID = 0
	hdr.Flags = 0x02 // DF flag set (Don't Fragment)
	hdr.FragOffset = 0
	hdr.TTL = 64
	hdr.Protocol = 0 // To be filled by caller
	hdr.Checksum = 0 // Computed by SerializeIPHeader
	hdr.SrcIP = 0    // To be filled by caller
	hdr.DstIP = 0    // To be filled by caller
}

// ipHeaderChecksum computes the RFC 1071 header checksum over an IPv4 header,
// treating the checksum field itself as zero as the standard requires.
func ipHeaderChecksum(header []byte) uint16 {
	if len(header) < 20 {
		return 0
	}
	scratch := append([]byte(nil), header...)
	binary.BigEndian.PutUint16(scratch[10:12], 0)
	return internetChecksum(scratch)
}

// setIPHeaderChecksum recomputes and stores the checksum of a serialized
// header. It must be called after any field changes, notably the TTL
// decrement performed on every forwarding hop.
func setIPHeaderChecksum(header []byte) {
	if len(header) < 20 {
		return
	}
	binary.BigEndian.PutUint16(header[10:12], ipHeaderChecksum(header))
}

// validIPHeaderChecksum reports whether a received header carries a correct
// checksum. Summing a valid header including its checksum field yields zero.
func validIPHeaderChecksum(header []byte) bool {
	if len(header) < 20 {
		return false
	}
	return internetChecksum(header) == 0
}

// GetIPHeaderLen returns the IP header length in bytes
func GetIPHeaderLen(hdr *IPHeader) int {
	return int(hdr.IHL) * 4
}

// SerializeIPHeader converts IP header to bytes (20 bytes for basic header)
func SerializeIPHeader(hdr *IPHeader) []byte {
	buf := make([]byte, 20)

	// Byte 0: Version (4 bits) + IHL (4 bits)
	buf[0] = (hdr.Version << 4) | (hdr.IHL & 0x0F)

	// Byte 1: TOS
	buf[1] = hdr.TOS

	// Bytes 2-3: Total Length
	binary.BigEndian.PutUint16(buf[2:4], hdr.TotalLen)

	// Bytes 4-5: Identification
	binary.BigEndian.PutUint16(buf[4:6], hdr.ID)

	// Bytes 6-7: Flags (3 bits) + Fragment Offset (13 bits)
	flagsAndOffset := (uint16(hdr.Flags) << 13) | (hdr.FragOffset & 0x1FFF)
	binary.BigEndian.PutUint16(buf[6:8], flagsAndOffset)

	// Byte 8: TTL
	buf[8] = hdr.TTL

	// Byte 9: Protocol
	buf[9] = hdr.Protocol

	// Bytes 10-11: Checksum
	binary.BigEndian.PutUint16(buf[10:12], hdr.Checksum)

	// Bytes 12-15: Source IP
	binary.BigEndian.PutUint32(buf[12:16], hdr.SrcIP)

	// Bytes 16-19: Destination IP
	binary.BigEndian.PutUint32(buf[16:20], hdr.DstIP)

	// The checksum covers the finished header, so it is always computed here
	// rather than trusted from the caller.
	setIPHeaderChecksum(buf)

	return buf
}

// DeserializeIPHeader parses bytes into IP header
func DeserializeIPHeader(buf []byte) (*IPHeader, error) {
	if len(buf) < 20 {
		return nil, fmt.Errorf("buffer too small for IP header: need 20 bytes, got %d", len(buf))
	}

	hdr := &IPHeader{}

	// Byte 0: Version (4 bits) + IHL (4 bits)
	hdr.Version = (buf[0] >> 4) & 0x0F
	hdr.IHL = buf[0] & 0x0F

	// Byte 1: TOS
	hdr.TOS = buf[1]

	// Bytes 2-3: Total Length
	hdr.TotalLen = binary.BigEndian.Uint16(buf[2:4])

	// Bytes 4-5: Identification
	hdr.ID = binary.BigEndian.Uint16(buf[4:6])

	// Bytes 6-7: Flags (3 bits) + Fragment Offset (13 bits)
	flagsAndOffset := binary.BigEndian.Uint16(buf[6:8])
	hdr.Flags = uint8((flagsAndOffset >> 13) & 0x07)
	hdr.FragOffset = flagsAndOffset & 0x1FFF

	// Byte 8: TTL
	hdr.TTL = buf[8]

	// Byte 9: Protocol
	hdr.Protocol = buf[9]

	// Bytes 10-11: Checksum
	hdr.Checksum = binary.BigEndian.Uint16(buf[10:12])

	// Bytes 12-15: Source IP
	hdr.SrcIP = binary.BigEndian.Uint32(buf[12:16])

	// Bytes 16-19: Destination IP
	hdr.DstIP = binary.BigEndian.Uint32(buf[16:20])
	if hdr.Version != 4 {
		return nil, fmt.Errorf("unsupported IP version: %d", hdr.Version)
	}
	if hdr.IHL < 5 || GetIPHeaderLen(hdr) > len(buf) {
		return nil, fmt.Errorf("invalid IP header length: %d", GetIPHeaderLen(hdr))
	}
	if int(hdr.TotalLen) < GetIPHeaderLen(hdr) || int(hdr.TotalLen) > len(buf) {
		return nil, fmt.Errorf("invalid IP total length: %d", hdr.TotalLen)
	}

	return hdr, nil
}

// IP address conversion utilities

// IPStringToUint32 converts IP string to 32-bit integer
func IPStringToUint32(ipStr string) (uint32, error) {
	ip, valid := parseIPv4(ipStr)
	if !valid {
		return 0, fmt.Errorf("invalid IP address: %s", ipStr)
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3]), nil
}

// IPUint32ToString converts 32-bit integer to IP string
func IPUint32ToString(ipInt uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", byte(ipInt>>24), byte(ipInt>>16), byte(ipInt>>8), byte(ipInt))
}

// ApplyMask applies a subnet mask to an IP address and returns the network address
func ApplyMask(ipStr string, mask uint8) (string, error) {
	ipInt, err := IPStringToUint32(ipStr)
	if err != nil {
		return "", fmt.Errorf("invalid IP address: %s", ipStr)
	}

	if mask > 32 {
		return "", fmt.Errorf("invalid mask: %d (must be 0-32)", mask)
	}

	// Create subnet mask
	var maskInt uint32
	if mask == 0 {
		maskInt = 0
	} else {
		maskInt = ^uint32(0) << (32 - mask)
	}

	// Apply mask
	networkInt := ipInt & maskInt

	return IPUint32ToString(networkInt), nil
}

// RouteSource indicates the protocol that installed the route
type RouteSource uint8

const (
	ROUTE_SOURCE_CONNECTED RouteSource = 0   // Directly connected networks
	ROUTE_SOURCE_STATIC    RouteSource = 1   // Static routes
	ROUTE_SOURCE_RIP       RouteSource = 120 // RIP
	ROUTE_SOURCE_OSPF      RouteSource = 110 // OSPF
	ROUTE_SOURCE_ISIS      RouteSource = 115 // IS-IS
	ROUTE_SOURCE_BGP       RouteSource = 20  // BGP (eBGP)
	ROUTE_SOURCE_IBGP      RouteSource = 200 // iBGP
)

// RouteSourceToString converts route source to human-readable string
func RouteSourceToString(source RouteSource) string {
	switch source {
	case ROUTE_SOURCE_CONNECTED:
		return "C"
	case ROUTE_SOURCE_STATIC:
		return "S"
	case ROUTE_SOURCE_RIP:
		return "R"
	case ROUTE_SOURCE_OSPF:
		return "O"
	case ROUTE_SOURCE_ISIS:
		return "I"
	case ROUTE_SOURCE_BGP:
		return "B"
	case ROUTE_SOURCE_IBGP:
		return "i"
	default:
		return "?"
	}
}

// L3Route represents a routing table entry with industry-standard fields
type L3Route struct {
	Dest      string // Destination network (e.g., "192.168.1.0")
	Mask      uint8  // Subnet mask in CIDR notation (e.g., 24 for /24)
	GatewayIP string // Next hop IP (empty if direct)
	OIF       string // Outgoing interface name

	// Industry standard fields
	AdminDistance uint8       // Administrative Distance (route priority, lower is better)
	Metric        uint32      // Route metric/cost (used when AD is equal)
	Source        RouteSource // Protocol that installed this route

	// Legacy compatibility
	IsDirect bool // True if directly connected network (for backward compatibility)
}

func routingTableReference(route *L3Route, destinationIP, result string) *EventTableReference {
	reference := &EventTableReference{
		Kind:   "routing",
		Result: result,
		Query:  map[string]string{"destinationIp": destinationIP},
	}
	if route == nil {
		return reference
	}
	reference.Entry = map[string]string{
		"destination":   route.Dest,
		"mask":          fmt.Sprintf("%d", route.Mask),
		"gateway":       route.GatewayIP,
		"interface":     route.OIF,
		"source":        RouteSourceToString(route.Source),
		"adminDistance": fmt.Sprintf("%d", route.AdminDistance),
		"metric":        fmt.Sprintf("%d", route.Metric),
		"direct":        fmt.Sprintf("%t", route.IsDirect),
	}
	return reference
}

// RoutingTable represents the L3 routing table (RIB - Routing Information Base)
type RoutingTable struct {
	mutex  sync.RWMutex
	routes []L3Route
}

// InitRoutingTable initializes a new routing table
func InitRoutingTable() *RoutingTable {
	return &RoutingTable{
		routes: make([]L3Route, 0),
	}
}

// AddRoute adds a route to the routing table with default parameters (for backward compatibility)
func (rt *RoutingTable) AddRoute(dest string, mask uint8, gatewayIP string, oif string) error {
	// Default: static route with AD=1, metric=1
	return rt.AddRouteWithParams(dest, mask, gatewayIP, oif, ROUTE_SOURCE_STATIC, uint8(ROUTE_SOURCE_STATIC), 1)
}

// AddRouteWithParams adds a route with full industry-standard parameters
func (rt *RoutingTable) AddRouteWithParams(dest string, mask uint8, gatewayIP string, oif string,
	source RouteSource, adminDistance uint8, metric uint32) error {

	// Apply mask to destination to get network address
	networkAddr, err := ApplyMask(dest, mask)
	if err != nil {
		return fmt.Errorf("failed to apply mask: %w", err)
	}
	if gatewayIP != "" {
		if _, err := IPStringToUint32(gatewayIP); err != nil {
			return fmt.Errorf("invalid gateway: %w", err)
		}
	}

	isDirect := (gatewayIP == "")
	rt.mutex.Lock()
	defer rt.mutex.Unlock()

	// Check if a route from the same source already exists
	for i, route := range rt.routes {
		if route.Dest == networkAddr && route.Mask == mask && route.Source == source {
			// Update existing route from same source
			rt.routes[i].GatewayIP = gatewayIP
			rt.routes[i].OIF = oif
			rt.routes[i].IsDirect = isDirect
			rt.routes[i].AdminDistance = adminDistance
			rt.routes[i].Metric = metric
			LogInfo("Updated route [%s]: %s/%d via %s (%s) AD=%d Metric=%d",
				RouteSourceToString(source), networkAddr, mask, gatewayIP, oif, adminDistance, metric)
			return nil
		}
	}

	// Add new route
	route := L3Route{
		Dest:          networkAddr,
		Mask:          mask,
		GatewayIP:     gatewayIP,
		OIF:           oif,
		IsDirect:      isDirect,
		AdminDistance: adminDistance,
		Metric:        metric,
		Source:        source,
	}

	rt.routes = append(rt.routes, route)
	LogInfo("Added route [%s]: %s/%d via %s (%s) AD=%d Metric=%d",
		RouteSourceToString(source), networkAddr, mask, gatewayIP, oif, adminDistance, metric)
	return nil
}

func (rt *RoutingTable) DeleteRouteFromSource(dest string, mask uint8, source RouteSource) error {
	networkAddr, err := ApplyMask(dest, mask)
	if err != nil {
		return fmt.Errorf("failed to apply mask: %w", err)
	}

	rt.mutex.Lock()
	defer rt.mutex.Unlock()

	for i, route := range rt.routes {
		if route.Dest == networkAddr && route.Mask == mask && route.Source == source {
			rt.routes[i] = rt.routes[len(rt.routes)-1]
			rt.routes = rt.routes[:len(rt.routes)-1]
			LogInfo("Deleted route [%s]: %s/%d", RouteSourceToString(source), networkAddr, mask)
			return nil
		}
	}

	return fmt.Errorf("route not found [%s]: %s/%d", RouteSourceToString(source), networkAddr, mask)
}

// DeleteRouteBySource removes routes installed by a specific protocol
func (rt *RoutingTable) DeleteRouteBySource(source RouteSource) int {
	rt.mutex.Lock()
	defer rt.mutex.Unlock()

	deleted := 0
	newRoutes := make([]L3Route, 0)

	for _, route := range rt.routes {
		if route.Source == source {
			LogInfo("Deleted route [%s]: %s/%d", RouteSourceToString(source), route.Dest, route.Mask)
			deleted++
		} else {
			newRoutes = append(newRoutes, route)
		}
	}

	rt.routes = newRoutes
	return deleted
}

// LookupLPM performs longest prefix match lookup with best route selection
// Selection criteria (in order):
// 1. Longest prefix match (longest mask)
// 2. Lowest Administrative Distance
// 3. Lowest Metric
func (rt *RoutingTable) LookupLPM(destIP uint32) *L3Route {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	bestRouteIndex := -1
	var longestMask uint8 = 0

	destIPStr := IPUint32ToString(destIP)
	LogDebug("LPM Lookup for %s (searching %d routes)", destIPStr, len(rt.routes))

	for i := range rt.routes {
		route := &rt.routes[i]

		// Apply route's mask to destination IP
		networkAddr, err := ApplyMask(destIPStr, route.Mask)
		if err != nil {
			LogDebug("LPM: ApplyMask error for route %s/%d: %v", route.Dest, route.Mask, err)
			continue
		}

		LogDebug("LPM: Comparing %s (masked %s) with route %s/%d [%s] AD=%d Metric=%d",
			destIPStr, networkAddr, route.Dest, route.Mask,
			RouteSourceToString(route.Source), route.AdminDistance, route.Metric)

		// Check if destination matches this route's network
		if networkAddr == route.Dest {
			LogDebug("LPM: MATCH! Route %s/%d", route.Dest, route.Mask)

			// Industry-standard route selection:
			// 1. Prefer longer prefix (more specific route)
			if bestRouteIndex == -1 || route.Mask > longestMask {
				longestMask = route.Mask
				bestRouteIndex = i
				LogDebug("LPM: New best route (longer prefix): %s/%d", route.Dest, route.Mask)
			} else if route.Mask == longestMask {
				bestRoute := &rt.routes[bestRouteIndex]
				// 2. Same prefix length - compare Administrative Distance (lower is better)
				if route.AdminDistance < bestRoute.AdminDistance {
					LogDebug("LPM: New best route (lower AD %d < %d)", route.AdminDistance, bestRoute.AdminDistance)
					bestRouteIndex = i
				} else if route.AdminDistance == bestRoute.AdminDistance {
					// 3. Same AD - compare Metric (lower is better)
					if route.Metric < bestRoute.Metric {
						LogDebug("LPM: New best route (lower metric %d < %d)", route.Metric, bestRoute.Metric)
						bestRouteIndex = i
					}
				}
			}
		}
	}

	if bestRouteIndex != -1 {
		bestRoute := rt.routes[bestRouteIndex]
		LogDebug("LPM: Selected route [%s] %s/%d via %s AD=%d Metric=%d",
			RouteSourceToString(bestRoute.Source), bestRoute.Dest, bestRoute.Mask,
			bestRoute.GatewayIP, bestRoute.AdminDistance, bestRoute.Metric)
		return &bestRoute
	} else {
		LogDebug("LPM: No matching route found!")
	}

	return nil
}

func (rt *RoutingTable) RoutesSnapshot() []L3Route {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	routes := make([]L3Route, len(rt.routes))
	copy(routes, rt.routes)
	return routes
}

// DumpRoutingTable prints the routing table in Cisco-like format
func (rt *RoutingTable) DumpRoutingTable(nodeName string) {
	routes := rt.RoutesSnapshot()

	fmt.Printf("\n=== Routing Table for Node: %s ===\n", nodeName)
	fmt.Printf("Legend: C=Connected, S=Static, R=RIP, O=OSPF, I=IS-IS, B=BGP\n")
	fmt.Printf("%-3s %-20s %-6s %-20s %-16s %-4s %-8s\n",
		"Src", "Destination", "Mask", "Gateway", "Interface", "AD", "Metric")
	fmt.Printf("%-3s %-20s %-6s %-20s %-16s %-4s %-8s\n",
		"---", "---------------", "----", "---------------", "-------------", "---", "------")

	if len(routes) == 0 {
		fmt.Printf("(empty)\n")
		return
	}

	for _, route := range routes {
		gateway := route.GatewayIP
		iface := route.OIF
		source := RouteSourceToString(route.Source)

		if route.IsDirect || route.GatewayIP == "" {
			gateway = "0.0.0.0" // Connected routes show 0.0.0.0 as gateway
		}
		if iface == "" {
			iface = "NA"
		}

		fmt.Printf("%-3s %-20s %-6d %-20s %-16s %-4d %-8d\n",
			source, route.Dest, route.Mask, gateway, iface,
			route.AdminDistance, route.Metric)
	}
	fmt.Printf("\n")
}

// IsLayer3LocalDelivery checks if packet is destined for this node
func IsLayer3LocalDelivery(node *Node, dstIP uint32) bool {
	if node == nil {
		return false
	}

	dstIPStr := IPUint32ToString(dstIP)

	// Check loopback address
	if node.node_nw_prop.is_loopback_addr_config {
		loopbackStr := node.node_nw_prop.loopback_addr.String()
		if loopbackStr == dstIPStr {
			return true
		}
	}

	// Check interface IP addresses
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := node.intf[i]
		if intf == nil {
			continue
		}

		if !intf.IsIPConfigured() {
			continue
		}

		intfIPStr := intf.GetIP().String()
		if intfIPStr == dstIPStr {
			return true
		}
	}

	return false
}

// Layer3IPPacketRecvFromBottom handles IP packets received from L2
func Layer3IPPacketRecvFromBottom(node *Node, intf *Interface, pkt []byte, pktSize int) {
	if node == nil || intf == nil || pkt == nil || pktSize < 20 || pktSize > len(pkt) {
		LogError("L3: Invalid parameters for packet receive")
		return
	}

	// Deserialize IP header
	pkt = pkt[:pktSize]
	ipHdr, err := DeserializeIPHeader(pkt)
	if err != nil {
		LogError("L3: Failed to deserialize IP header: %v", err)
		return
	}

	nodeName := get_node_name(node)
	dstIPStr := IPUint32ToString(ipHdr.DstIP)
	srcIPStr := IPUint32ToString(ipHdr.SrcIP)

	// A header that fails its checksum is corrupt and must be discarded in
	// silence: there is no way to know where a truthful error report would go.
	headerLen := GetIPHeaderLen(ipHdr)
	if !validIPHeaderChecksum(pkt[:headerLen]) {
		LogWarn("L3: Node %s: Discarding a packet from %s with a bad header checksum", nodeName, srcIPStr)
		emitInterfaceEvent(node, intf, ipProtocolName(ipHdr.Protocol), "packet_dropped", map[string]string{
			"destinationIp": dstIPStr,
			"reason":        "bad_header_checksum",
			"sourceIp":      srcIPStr,
		})
		return
	}

	if IsLayer3LocalDelivery(node, ipHdr.DstIP) {
		handleLocalIPPacket(node, intf, ipHdr, pkt[:ipHdr.TotalLen])
		return
	}

	// Look up route for destination
	route := node.node_nw_prop.rt_table.LookupLPM(ipHdr.DstIP)
	if route == nil {
		LogWarn("L3: Node %s: No route to %s", nodeName, dstIPStr)
		emitInterfaceEventWithTable(node, intf, ipProtocolName(ipHdr.Protocol), "packet_dropped", map[string]string{
			"destinationIp": dstIPStr,
			"reason":        "no_route",
			"sourceIp":      srcIPStr,
		}, routingTableReference(nil, dstIPStr, "miss"))
		// Tell the sender rather than letting it retransmit into a black hole.
		//sendICMPErrorRouted(node, intf, pkt, ipHdr, ICMP_TYPE_DEST_UNREACHABLE, ICMP_CODE_NET_UNREACHABLE)
		return
	}
	next_hop := route.GatewayIP
	route_interface := route.OIF
	if route.IsDirect {
		next_hop = dstIPStr
		if route_interface == "" {
			if direct_intf := node_get_matching_subnet_interface(node, dstIPStr); direct_intf != nil {
				route_interface = get_interface_name(direct_intf)
			}
		}
	}
	routeFields := map[string]string{
		"destinationIp":    dstIPStr,
		"egressInterface":  route_interface,
		"ingressInterface": get_interface_name(intf),
		"interface":        route_interface,
		"nextHop":          next_hop,
		"route":            fmt.Sprintf("%s/%d", route.Dest, route.Mask),
		"routeSource":      RouteSourceToString(route.Source),
		"sourceIp":         srcIPStr,
		"ttlBefore":        fmt.Sprintf("%d", ipHdr.TTL),
	}
	if ipHdr.TTL > 0 {
		routeFields["ttlAfter"] = fmt.Sprintf("%d", ipHdr.TTL-1)
	}
	emitInterfaceEventWithTable(node, intf, ipProtocolName(ipHdr.Protocol), "route_selected",
		routeFields, routingTableReference(route, dstIPStr, "hit"))

	// Every transit packet loses one hop, including packets leaving through a
	// directly connected route.
	if ipHdr.TTL <= 1 {
		emitInterfaceEventWithTable(node, intf, ipProtocolName(ipHdr.Protocol), "packet_dropped", map[string]string{
			"destinationIp": dstIPStr,
			"reason":        "ttl_expired",
			"sourceIp":      srcIPStr,
		}, routingTableReference(route, dstIPStr, "hit"))
		// Time Exceeded is what makes each hop visible to traceroute.
		sendICMPErrorRouted(node, intf, pkt, ipHdr, ICMP_TYPE_TIME_EXCEEDED, ICMP_CODE_TTL_EXCEEDED)
		return
	}
	ipHdr.TTL--

	// Update packet with decremented TTL, then repair the checksum the change
	// invalidated.
	pkt[8] = ipHdr.TTL
	setIPHeaderChecksum(pkt[:headerLen])
	emitNodeEventWithTable(node, ipProtocolName(ipHdr.Protocol), "packet_forwarding_started", map[string]string{
		"destinationIp": dstIPStr,
		"interface":     route_interface,
		"nextHop":       next_hop,
		"sourceIp":      srcIPStr,
		"ttl":           fmt.Sprintf("%d", ipHdr.TTL),
	}, routingTableReference(route, dstIPStr, "hit"))

	if route.IsDirect {
		DemotePacketToLayer2(node, ipHdr.DstIP, route_interface, pkt, pktSize, ETHERTYPE_IP)
		return
	}

	// Get next hop IP as uint32
	nextHopIP, err := IPStringToUint32(route.GatewayIP)
	if err != nil {
		LogError("L3: Invalid gateway IP: %s", route.GatewayIP)
		return
	}

	DemotePacketToLayer2(node, nextHopIP, route.OIF, pkt, pktSize, ETHERTYPE_IP)
}

func handleLocalIPPacket(node *Node, intf *Interface, ipHdr *IPHeader, pkt []byte) {
	headerLen := GetIPHeaderLen(ipHdr)
	payload := pkt[headerLen:ipHdr.TotalLen]
	srcIPStr := IPUint32ToString(ipHdr.SrcIP)

	switch ipHdr.Protocol {
	case PROTO_ICMP:
		if len(payload) < 8 {
			LogWarn("ICMP: Packet from %s is too short", srcIPStr)
			return
		}
		switch payload[0] {
		case 8:
			emitInterfaceEvent(node, intf, "ICMP", "icmp_echo_request_received", map[string]string{
				"destinationIp": IPUint32ToString(ipHdr.DstIP),
				"sourceIp":      srcIPStr,
				"ttl":           fmt.Sprintf("%d", ipHdr.TTL),
			})
			reply := append([]byte(nil), payload...)
			reply[0] = 0
			binary.BigEndian.PutUint16(reply[2:4], 0)
			binary.BigEndian.PutUint16(reply[2:4], internetChecksum(reply))
			emitNodeEvent(node, "ICMP", "icmp_echo_reply_created", map[string]string{
				"destinationIp": srcIPStr,
			})
			DemotePacketToLayer3(node, reply, len(reply), PROTO_ICMP, ipHdr.SrcIP)
		case 0:
			node.ping_reply_count.Add(1)
			emitInterfaceEvent(node, intf, "ICMP", "icmp_echo_reply_received", map[string]string{
				"sourceIp": srcIPStr,
				"ttl":      fmt.Sprintf("%d", ipHdr.TTL),
			})
		default:
			if isICMPErrorType(payload[0]) {
				handleICMPError(node, intf, ipHdr, payload)
				return
			}
			LogWarn("ICMP: Unsupported message type %d", payload[0])
		}
	case PROTO_MTCP:
		LogWarn("L4: MTCP is not implemented")
	case PROTO_USERAPP1:
		LogWarn("L5: USERAPP1 is not implemented")
	case IPPROTO_IPIP:
		DecapsulateIPinIP(node, intf, ipHdr, pkt, len(pkt))
	case 200:
		ripPacket, err := DeserializeRIPPacket(payload)
		if err != nil {
			LogError("RIP: Failed to deserialize packet: %v", err)
			return
		}
		node.rip_state.ProcessRIPPacket(ripPacket, srcIPStr, intf)
	default:
		LogWarn("L3: Unknown protocol: %d", ipHdr.Protocol)
	}
}

func internetChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func newICMPEchoRequest() []byte {
	return newICMPEchoRequestWithSequence(1)
}

func newICMPEchoRequestWithSequence(sequence uint16) []byte {
	payload := make([]byte, 8)
	payload[0] = ICMP_TYPE_ECHO_REQUEST
	binary.BigEndian.PutUint16(payload[4:6], 1)
	binary.BigEndian.PutUint16(payload[6:8], sequence)
	binary.BigEndian.PutUint16(payload[2:4], internetChecksum(payload))
	return payload
}

// PromotePacketToLayer3 is the public API for L2 to promote packets to L3
func PromotePacketToLayer3(node *Node, intf *Interface, pkt []byte, pktSize int, protocol uint16) {
	if node == nil || intf == nil || pkt == nil {
		LogError("L3: Invalid parameters for promote")
		return
	}

	LogDebug("L3: Promoting packet to Layer 3, protocol=0x%04x", protocol)

	switch protocol {
	case ETHERTYPE_IP:
		Layer3IPPacketRecvFromBottom(node, intf, pkt, pktSize)
	case PROTO_IP_IN_IP:
		Layer3IPPacketRecvFromBottom(node, intf, pkt, pktSize)
	default:
		LogWarn("L3: Unknown L3 protocol: 0x%04x", protocol)
	}
}

// DemotePacketToLayer3 handles packets from upper layers (L4/L5)
func DemotePacketToLayer3(node *Node, payload []byte, payloadSize int, protocol uint8, dstIP uint32) {
	demotePacketToLayer3WithSource(node, payload, payloadSize, protocol, dstIP, 0)
}

// demotePacketToLayer3WithSource sends a packet whose source address is chosen
// by the caller rather than derived from the egress interface. A source of 0
// means "decide as usual". ICMP error reports need this: they must appear to
// come from the interface that saw the failure, which is not necessarily the
// one the report leaves through.
func demotePacketToLayer3WithSource(node *Node, payload []byte, payloadSize int, protocol uint8, dstIP uint32, preferredSrcIP uint32) {
	demotePacketToLayer3WithOptions(node, payload, payloadSize, protocol, dstIP, preferredSrcIP, 64)
}

// demotePacketToLayer3WithTTL sends a locally originated packet with a
// caller-controlled TTL. Traceroute uses one packet per TTL so each router in
// turn expires a probe and identifies itself with ICMP Time Exceeded.
func demotePacketToLayer3WithTTL(node *Node, payload []byte, payloadSize int, protocol uint8, dstIP uint32, ttl uint8) {
	demotePacketToLayer3WithOptions(node, payload, payloadSize, protocol, dstIP, 0, ttl)
}

func demotePacketToLayer3WithOptions(node *Node, payload []byte, payloadSize int, protocol uint8, dstIP uint32, preferredSrcIP uint32, ttl uint8) {
	if node == nil {
		LogError("L3: Node cannot be nil")
		return
	}
	if payloadSize < 0 || payloadSize > len(payload) || payloadSize > 65515 {
		LogError("L3: Invalid payload size %d", payloadSize)
		return
	}

	nodeName := get_node_name(node)
	dstIPStr := IPUint32ToString(dstIP)

	LogDebug("L3: Node %s: Sending packet to %s, protocol=%d, size=%d",
		nodeName, dstIPStr, protocol, payloadSize)

	// Create IP header
	ipHdr := &IPHeader{}
	InitializeIPHeader(ipHdr)

	ipHdr.Protocol = protocol
	ipHdr.DstIP = dstIP
	ipHdr.TTL = ttl

	// Look up route to determine outgoing interface
	route := node.node_nw_prop.rt_table.LookupLPM(dstIP)
	if route == nil {
		LogWarn("L3: Node %s: No route to %s", nodeName, dstIPStr)
		emitNodeEventWithTable(node, ipProtocolName(protocol), "packet_dropped", map[string]string{
			"destinationIp": dstIPStr,
			"reason":        "no_route",
		}, routingTableReference(nil, dstIPStr, "miss"))
		return
	}
	next_hop := route.GatewayIP
	if route.IsDirect {
		next_hop = dstIPStr
	}

	// Set source IP from outgoing interface (more practical than loopback)
	var oif *Interface
	if route.OIF != "" {
		oif = get_node_if_by_name(node, route.OIF)
	} else {
		// For direct routes, find matching interface
		oif = node_get_matching_subnet_interface(node, dstIPStr)
	}

	if preferredSrcIP != 0 {
		ipHdr.SrcIP = preferredSrcIP
	} else if oif != nil && oif.IsIPConfigured() {
		srcIPStr := oif.GetIP().String()
		srcIP, err := IPStringToUint32(srcIPStr)
		if err != nil {
			LogError("L3: Invalid source IP: %s", srcIPStr)
			return
		}
		ipHdr.SrcIP = srcIP
	} else if node.node_nw_prop.is_loopback_addr_config {
		// Fallback to loopback if no interface IP
		srcIPStr := node.node_nw_prop.loopback_addr.String()
		srcIP, err := IPStringToUint32(srcIPStr)
		if err != nil {
			LogError("L3: Invalid source IP: %s", srcIPStr)
			return
		}
		ipHdr.SrcIP = srcIP
	}
	route_interface := route.OIF
	if route_interface == "" && oif != nil {
		route_interface = get_interface_name(oif)
	}
	emitNodeEventWithTable(node, ipProtocolName(protocol), "route_selected", map[string]string{
		"destinationIp": dstIPStr,
		"interface":     route_interface,
		"nextHop":       next_hop,
		"route":         fmt.Sprintf("%s/%d", route.Dest, route.Mask),
		"routeSource":   RouteSourceToString(route.Source),
	}, routingTableReference(route, dstIPStr, "hit"))

	// Calculate total length (header + payload)
	// TotalLen is in bytes
	ipHdr.TotalLen = uint16(GetIPHeaderLen(ipHdr) + payloadSize)

	// Serialize IP header
	ipHdrBytes := SerializeIPHeader(ipHdr)

	// Create complete packet (IP header + payload)
	totalSize := len(ipHdrBytes) + payloadSize
	pkt := make([]byte, totalSize)
	copy(pkt, ipHdrBytes)
	if payload != nil && payloadSize > 0 {
		copy(pkt[len(ipHdrBytes):], payload[:payloadSize])
	}

	// Determine next hop
	var nextHopIP uint32
	var oifName string

	if route.IsDirect {
		// Direct delivery
		nextHopIP = dstIP
		oifName = ""
	} else {
		// Forward to gateway
		var err error
		nextHopIP, err = IPStringToUint32(route.GatewayIP)
		if err != nil {
			LogError("L3: Invalid gateway IP: %s", route.GatewayIP)
			return
		}
		oifName = route.OIF
	}

	// Send to L2
	LogInfo("L3: Node %s sending IP packet to next hop %s via %s",
		nodeName, IPUint32ToString(nextHopIP), oifName)
	DemotePacketToLayer2(node, nextHopIP, oifName, pkt, totalSize, ETHERTYPE_IP)
}

// Layer5PingFunc sends a ping (ICMP) packet from node to destination
func Layer5PingFunc(node *Node, dstIPAddr string) {
	if node == nil {
		LogError("Ping: Node cannot be nil")
		return
	}

	dstIP, err := IPStringToUint32(dstIPAddr)
	if err != nil {
		LogError("Ping: Invalid destination IP: %s", dstIPAddr)
		fmt.Printf("Error: Invalid destination IP: %s\n", dstIPAddr)
		return
	}

	payload := newICMPEchoRequest()
	DemotePacketToLayer3(node, payload, len(payload), PROTO_ICMP, dstIP)
}

// Layer5TracerouteProbeFunc sends one ICMP echo probe with a specific TTL.
// The sequence number mirrors the TTL, making packet events and quoted ICMP
// errors easy to associate with the hop being discovered.
func Layer5TracerouteProbeFunc(node *Node, dstIPAddr string, ttl uint8) {
	if node == nil {
		LogError("Traceroute: Node cannot be nil")
		return
	}

	dstIP, err := IPStringToUint32(dstIPAddr)
	if err != nil {
		LogError("Traceroute: Invalid destination IP: %s", dstIPAddr)
		return
	}

	payload := newICMPEchoRequestWithSequence(uint16(ttl))
	demotePacketToLayer3WithTTL(node, payload, len(payload), PROTO_ICMP, dstIP, ttl)
}

// =============================
// IP-in-IP Encapsulation (ERO)
// =============================

// Layer3EroPingFunc sends a ping packet via explicit route object (ERO)
// This encapsulates the inner IP packet in an outer IP-in-IP tunnel to force
// the packet through a specific intermediate node (eroIPAddr) before reaching
// the final destination (dstIPAddr)
func Layer3EroPingFunc(node *Node, dstIPAddr string, eroIPAddr string) {
	if node == nil {
		LogError("ERO Ping: Node cannot be nil")
		return
	}

	nodeName := get_node_name(node)
	LogInfo("ERO Ping: Node %s sending to %s via ERO %s", nodeName, dstIPAddr, eroIPAddr)
	emitNodeEvent(node, "ICMP", "ero_ping_started", map[string]string{
		"destinationIp": dstIPAddr,
		"eroIp":         eroIPAddr,
	})

	// Parse destination IP
	dstIP, err := IPStringToUint32(dstIPAddr)
	if err != nil {
		LogError("ERO Ping: Invalid destination IP: %s", dstIPAddr)
		fmt.Printf("Error: Invalid destination IP: %s\n", dstIPAddr)
		return
	}

	// Parse ERO IP
	eroIP, err := IPStringToUint32(eroIPAddr)
	if err != nil {
		LogError("ERO Ping: Invalid ERO IP: %s", eroIPAddr)
		fmt.Printf("Error: Invalid ERO IP: %s\n", eroIPAddr)
		return
	}

	// Get source IP (loopback address of the node)
	srcIPStr := node.node_nw_prop.loopback_addr.String()
	srcIP, err := IPStringToUint32(srcIPStr)
	if err != nil {
		LogError("ERO Ping: Invalid source IP: %s", srcIPStr)
		return
	}

	// Create inner IP header (original packet: src -> dst, ICMP)
	innerHdr := &IPHeader{}
	InitializeIPHeader(innerHdr)
	innerHdr.Protocol = PROTO_ICMP
	innerHdr.SrcIP = srcIP
	innerHdr.DstIP = dstIP
	icmpPayload := newICMPEchoRequest()
	innerHdr.TotalLen = uint16(20 + len(icmpPayload))
	innerHdr.TTL = 64

	// Serialize inner IP header
	innerIPBytes := append(SerializeIPHeader(innerHdr), icmpPayload...)

	// Now encapsulate: send inner IP packet as payload with protocol=IPPROTO_IPIP
	// Outer header will have: src=this node, dst=ERO node, protocol=4 (IP-in-IP)
	LogInfo("ERO Ping: Encapsulating inner packet (proto=ICMP, dst=%s) in IP-in-IP tunnel to ERO %s",
		dstIPAddr, eroIPAddr)

	DemotePacketToLayer3(node, innerIPBytes, len(innerIPBytes), IPPROTO_IPIP, eroIP)
}

// DecapsulateIPinIP handles IP-in-IP decapsulation at ERO node
// Extracts the inner IP packet and re-injects it into the network layer
func DecapsulateIPinIP(node *Node, iif *Interface, outerIPHdr *IPHeader, pkt []byte, pktSize int) {
	nodeName := get_node_name(node)

	// Get interface name
	iifName := string(iif.if_name[:])
	for i, b := range iif.if_name {
		if b == 0 {
			iifName = string(iif.if_name[:i])
			break
		}
	}

	LogInfo("IPIP: Node %s received IP-in-IP packet on %s (outer dst=%s)",
		nodeName, iifName, IPUint32ToString(outerIPHdr.DstIP))

	// Verify this packet is for us (destination matches one of our IPs)
	// If not, just forward the outer packet normally
	isForUs := false

	// Check loopback
	if node.node_nw_prop.is_loopback_addr_config {
		loIPStr := node.node_nw_prop.loopback_addr.String()
		loIP, err := IPStringToUint32(loIPStr)
		if err == nil && loIP == outerIPHdr.DstIP {
			isForUs = true
		}
	}

	// Check all interfaces
	if !isForUs {
		for i := 0; i < MAX_INTF_PER_NODE; i++ {
			intf := node.intf[i]
			if intf == nil {
				continue
			}
			if !IS_INTF_L3_MODE(intf) {
				continue
			}
			intfIPStr := intf.intf_nw_props.ip_addr.String()
			intfIP, err := IPStringToUint32(intfIPStr)
			if err == nil && intfIP == outerIPHdr.DstIP {
				isForUs = true
				break
			}
		}
	}

	if !isForUs {
		// Not for us, forward the outer packet normally
		LogDebug("IPIP: Outer packet not for us, forwarding normally")
		return
	}

	// Extract inner IP packet (starts after outer IP header)
	outerHdrLen := GetIPHeaderLen(outerIPHdr)
	if pktSize < outerHdrLen {
		LogError("IPIP: Packet too short for IP-in-IP decapsulation")
		return
	}

	innerIPPkt := pkt[outerHdrLen:]
	innerPktSize := pktSize - outerHdrLen

	if innerPktSize < 20 {
		LogError("IPIP: Inner packet too short (size=%d)", innerPktSize)
		return
	}

	// Parse inner IP header to log it
	innerHdr, err := DeserializeIPHeader(innerIPPkt)
	if err != nil {
		LogError("IPIP: Failed to parse inner IP header: %v", err)
		return
	}

	LogInfo("IPIP: Decapsulated inner packet (proto=%d, src=%s, dst=%s, TTL=%d)",
		innerHdr.Protocol, IPUint32ToString(innerHdr.SrcIP),
		IPUint32ToString(innerHdr.DstIP), innerHdr.TTL)

	// Re-inject inner packet into network layer for routing to final destination
	// This is like receiving a new IP packet
	LogInfo("IPIP: Re-injecting inner packet into network layer")
	Layer3IPPacketRecvFromBottom(node, iif, innerIPPkt, innerPktSize)
}
