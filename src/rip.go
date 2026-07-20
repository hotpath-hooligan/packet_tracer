package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// RIP Protocol Constants
const (
	RIP_VERSION           = 2
	RIP_UDP_PORT          = 520
	RIP_UPDATE_INTERVAL   = 30 * time.Second  // Send updates every 30 seconds
	RIP_TIMEOUT           = 180 * time.Second // Route timeout
	RIP_GARBAGE_COLLECT   = 120 * time.Second // Garbage collection time
	RIP_TRIGGER_DELAY_MIN = 1 * time.Second   // Minimum triggered-update delay
	RIP_TRIGGER_DELAY_MAX = 5 * time.Second   // Maximum triggered-update delay
	RIP_MAX_METRIC        = 16                // Infinity (unreachable)
	RIP_HEADER_SIZE       = 4
	RIP_ENTRY_SIZE        = 20
	RIP_ADDRESS_FAMILY_IP = 2
)

// RIP Command types
const (
	RIP_COMMAND_REQUEST  = 1
	RIP_COMMAND_RESPONSE = 2
)

// RIPEntry represents a single route entry in RIP packet
type RIPEntry struct {
	AddressFamily uint16 // 2 for IP
	RouteTag      uint16 // Optional tag
	IPAddress     uint32 // Network address
	SubnetMask    uint32 // Subnet mask
	NextHop       uint32 // Next hop IP (0 = sender)
	Metric        uint32 // Hop count (1-16)
}

// RIPPacket represents a RIP version 2 packet
type RIPPacket struct {
	Command uint8      // 1=Request, 2=Response
	Version uint8      // Version 2
	Zero    uint16     // Must be zero
	Entries []RIPEntry // Route entries
}

// RIPRoute represents a learned route with RIP-specific info
type RIPRoute struct {
	Destination string    // Network address
	Mask        uint8     // Subnet mask
	NextHop     string    // Next hop IP
	Interface   string    // Outgoing interface
	Metric      uint32    // Hop count
	LastUpdate  time.Time // Last time this route was updated
	IsExpired   bool      // Route has expired
	ExpiredAt   time.Time // Start of the garbage-collection interval
}

// RIPState represents the RIP daemon state for a node
type RIPState struct {
	node            *Node
	enabled         bool
	routes          map[string]*RIPRoute // Key: "dest/mask"
	mutex           sync.RWMutex
	stopCh          chan struct{}
	waitGroup       sync.WaitGroup
	updateTimer     *time.Ticker
	expirationTimer *time.Ticker
	triggerUpdateCh chan struct{}
}

func ripRoutingTableReference(route *RIPRoute, result string) *EventTableReference {
	if route == nil {
		return nil
	}
	return routingTableReference(&L3Route{
		Dest:          route.Destination,
		Mask:          route.Mask,
		GatewayIP:     route.NextHop,
		OIF:           route.Interface,
		AdminDistance: uint8(ROUTE_SOURCE_RIP),
		Metric:        route.Metric,
		Source:        ROUTE_SOURCE_RIP,
	}, route.Destination, result)
}

// SerializeRIPPacket converts RIP packet to bytes
func SerializeRIPPacket(pkt *RIPPacket) []byte {
	size := RIP_HEADER_SIZE + (len(pkt.Entries) * RIP_ENTRY_SIZE)
	buf := make([]byte, size)

	// Header
	buf[0] = pkt.Command
	buf[1] = pkt.Version
	binary.BigEndian.PutUint16(buf[2:4], pkt.Zero)

	// Entries
	offset := RIP_HEADER_SIZE
	for _, entry := range pkt.Entries {
		binary.BigEndian.PutUint16(buf[offset:offset+2], entry.AddressFamily)
		binary.BigEndian.PutUint16(buf[offset+2:offset+4], entry.RouteTag)
		binary.BigEndian.PutUint32(buf[offset+4:offset+8], entry.IPAddress)
		binary.BigEndian.PutUint32(buf[offset+8:offset+12], entry.SubnetMask)
		binary.BigEndian.PutUint32(buf[offset+12:offset+16], entry.NextHop)
		binary.BigEndian.PutUint32(buf[offset+16:offset+20], entry.Metric)
		offset += RIP_ENTRY_SIZE
	}

	return buf
}

// DeserializeRIPPacket parses bytes into RIP packet
func DeserializeRIPPacket(buf []byte) (*RIPPacket, error) {
	if len(buf) < RIP_HEADER_SIZE {
		return nil, fmt.Errorf("buffer too small for RIP header")
	}

	pkt := &RIPPacket{
		Command: buf[0],
		Version: buf[1],
		Zero:    binary.BigEndian.Uint16(buf[2:4]),
		Entries: make([]RIPEntry, 0),
	}

	// Parse entries
	offset := RIP_HEADER_SIZE
	for offset+RIP_ENTRY_SIZE <= len(buf) {
		entry := RIPEntry{
			AddressFamily: binary.BigEndian.Uint16(buf[offset : offset+2]),
			RouteTag:      binary.BigEndian.Uint16(buf[offset+2 : offset+4]),
			IPAddress:     binary.BigEndian.Uint32(buf[offset+4 : offset+8]),
			SubnetMask:    binary.BigEndian.Uint32(buf[offset+8 : offset+12]),
			NextHop:       binary.BigEndian.Uint32(buf[offset+12 : offset+16]),
			Metric:        binary.BigEndian.Uint32(buf[offset+16 : offset+20]),
		}
		pkt.Entries = append(pkt.Entries, entry)
		offset += RIP_ENTRY_SIZE
	}

	return pkt, nil
}

// InitRIPState initializes RIP state for a node
func InitRIPState(node *Node) *RIPState {
	return &RIPState{
		node:    node,
		enabled: false,
		routes:  make(map[string]*RIPRoute),
	}
}

// StartRIP enables RIP protocol on the node
func (rip *RIPState) StartRIP() {
	rip.mutex.Lock()
	if rip.enabled {
		rip.mutex.Unlock()
		LogWarn("RIP: Already enabled on node %s", get_node_name(rip.node))
		return
	}
	stopCh := make(chan struct{})
	triggerUpdateCh := make(chan struct{}, 1)
	updateTimer := time.NewTicker(RIP_UPDATE_INTERVAL)
	expirationTimer := time.NewTicker(30 * time.Second)
	rip.stopCh = stopCh
	rip.triggerUpdateCh = triggerUpdateCh
	rip.updateTimer = updateTimer
	rip.expirationTimer = expirationTimer
	rip.enabled = true
	rip.waitGroup.Add(1)
	rip.mutex.Unlock()

	nodeName := get_node_name(rip.node)
	LogInfo("RIP: Starting RIP daemon on node %s", nodeName)
	emitNodeEvent(rip.node, "RIP", "rip_enabled", nil)

	go rip.runRIPDaemon(stopCh, triggerUpdateCh, updateTimer, expirationTimer)
}

// StopRIP disables RIP protocol on the node
func (rip *RIPState) StopRIP() {
	rip.mutex.Lock()
	if !rip.enabled {
		rip.mutex.Unlock()
		return
	}
	rip.enabled = false
	stopCh := rip.stopCh
	updateTimer := rip.updateTimer
	expirationTimer := rip.expirationTimer
	rip.stopCh = nil
	rip.triggerUpdateCh = nil
	rip.updateTimer = nil
	rip.expirationTimer = nil
	close(stopCh)
	updateTimer.Stop()
	expirationTimer.Stop()
	rip.mutex.Unlock()

	LogInfo("RIP: Stopping RIP daemon on node %s", get_node_name(rip.node))
	rip.waitGroup.Wait()
	rip.node.node_nw_prop.rt_table.DeleteRouteBySource(ROUTE_SOURCE_RIP)

	rip.mutex.Lock()
	rip.routes = make(map[string]*RIPRoute)
	rip.mutex.Unlock()
	emitNodeEvent(rip.node, "RIP", "rip_disabled", nil)
}

// runRIPDaemon is the main RIP daemon loop
func (rip *RIPState) runRIPDaemon(stopCh, triggerUpdateCh <-chan struct{}, updateTimer, expirationTimer *time.Ticker) {
	defer rip.waitGroup.Done()

	nodeName := get_node_name(rip.node)
	LogInfo("RIP: Daemon started on node %s", nodeName)
	rip.node.graph.runBackgroundProtocol(rip.SendRoutingUpdates)

	var triggerTimer *time.Timer
	var triggerTimerCh <-chan time.Time

	for {
		select {
		case <-stopCh:
			if triggerTimer != nil {
				triggerTimer.Stop()
			}
			LogInfo("RIP: Daemon stopped on node %s", nodeName)
			return

		case <-updateTimer.C:
			// A periodic update supersedes a pending triggered update.
			if triggerTimer != nil {
				triggerTimer.Stop()
				triggerTimer = nil
				triggerTimerCh = nil
			}
			rip.node.graph.runBackgroundProtocol(rip.SendRoutingUpdates)

		case <-triggerUpdateCh:
			if triggerTimer == nil {
				triggerTimer = time.NewTimer(ripTriggeredUpdateDelay())
				triggerTimerCh = triggerTimer.C
			}

		case <-triggerTimerCh:
			rip.node.graph.runBackgroundProtocol(rip.SendRoutingUpdates)
			triggerTimer = nil
			triggerTimerCh = nil

		case <-expirationTimer.C:
			// Check for expired routes
			rip.node.graph.runBackgroundProtocol(rip.CheckExpiredRoutes)
		}
	}
}

func ripTriggeredUpdateDelay() time.Duration {
	window := RIP_TRIGGER_DELAY_MAX - RIP_TRIGGER_DELAY_MIN
	if window <= 0 {
		return RIP_TRIGGER_DELAY_MIN
	}
	return RIP_TRIGGER_DELAY_MIN + time.Duration(rand.Int63n(int64(window)))
}

// requestTriggeredUpdate asks the RIP daemon to advertise a routing change
// without waiting for the next periodic update. The buffered channel coalesces
// a burst of changes into one update.
func (rip *RIPState) requestTriggeredUpdate() {
	rip.mutex.RLock()
	enabled := rip.enabled
	triggerUpdateCh := rip.triggerUpdateCh
	rip.mutex.RUnlock()
	if !enabled || triggerUpdateCh == nil {
		return
	}

	select {
	case triggerUpdateCh <- struct{}{}:
	default:
	}
}

// routesForAdvertisement returns forwarding routes plus poisoned RIP routes
// retained solely for garbage collection. Expired routes must not remain in
// the FIB, but RFC 2453 requires them to be advertised with metric 16 until
// their garbage-collection timer expires.
func (rip *RIPState) routesForAdvertisement() []L3Route {
	rip.mutex.RLock()
	poisoned := make([]L3Route, 0)
	for _, route := range rip.routes {
		if !route.IsExpired {
			continue
		}
		poisoned = append(poisoned, L3Route{
			Dest:          route.Destination,
			Mask:          route.Mask,
			GatewayIP:     route.NextHop,
			OIF:           route.Interface,
			AdminDistance: uint8(ROUTE_SOURCE_RIP),
			Metric:        RIP_MAX_METRIC,
			Source:        ROUTE_SOURCE_RIP,
		})
	}
	rip.mutex.RUnlock()

	routes := rip.node.node_nw_prop.rt_table.RoutesSnapshot()
	advertised := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		advertised[fmt.Sprintf("%s/%d", route.Dest, route.Mask)] = struct{}{}
	}
	for _, route := range poisoned {
		key := fmt.Sprintf("%s/%d", route.Dest, route.Mask)
		if _, exists := advertised[key]; exists {
			continue
		}
		routes = append(routes, route)
	}
	return routes
}

// SendRoutingUpdates sends RIP updates to all neighbors
func (rip *RIPState) SendRoutingUpdates() {
	nodeName := get_node_name(rip.node)
	LogDebug("RIP: Node %s sending routing updates", nodeName)
	routes := rip.routesForAdvertisement()
	if len(routes) == 0 {
		LogDebug("RIP: No routes to advertise from node %s", nodeName)
		return
	}

	// Send to all interfaces
	for i := 0; i < MAX_INTF_PER_NODE; i++ {
		intf := rip.node.intf[i]
		if intf == nil || !intf.IsIPConfigured() {
			continue
		}

		// Get neighbor node
		nbr := get_nbr_node(intf)
		if nbr == nil {
			continue
		}

		intfName := get_interface_name(intf)
		packet := buildRIPResponse(routes, intfName)
		ripData := SerializeRIPPacket(packet)
		LogDebug("RIP: Sending update from %s via %s to %s (%d entries)",
			nodeName, intfName, get_node_name(nbr), len(packet.Entries))

		// Encapsulate in UDP/IP/Ethernet and send
		// For simplicity, we'll send RIP directly (in real world, it goes over UDP)
		rip.SendRIPPacket(ripData, intf)
	}
}

func buildRIPResponse(routes []L3Route, outgoingInterface string) *RIPPacket {
	packet := &RIPPacket{
		Command: RIP_COMMAND_RESPONSE,
		Version: RIP_VERSION,
		Entries: make([]RIPEntry, 0, len(routes)),
	}

	for _, route := range routes {
		destIP, err := IPStringToUint32(route.Dest)
		if err != nil {
			continue
		}

		var maskInt uint32
		if route.Mask > 0 {
			maskInt = ^uint32(0) << (32 - route.Mask)
		}

		// Connected routes are stored at cost 0. Advertise that cost unchanged;
		// ProcessRIPPacket adds the incoming link cost when it receives the route.
		metric := route.Metric
		if route.Source != ROUTE_SOURCE_CONNECTED && metric == 0 {
			metric = 1
		}
		if route.Source == ROUTE_SOURCE_RIP && route.OIF == outgoingInterface {
			metric = RIP_MAX_METRIC
		}
		if metric > RIP_MAX_METRIC {
			metric = RIP_MAX_METRIC
		}

		packet.Entries = append(packet.Entries, RIPEntry{
			AddressFamily: RIP_ADDRESS_FAMILY_IP,
			IPAddress:     destIP,
			SubnetMask:    maskInt,
			Metric:        metric,
		})
	}

	return packet
}

// SendRIPPacket sends a RIP packet through an interface
func (rip *RIPState) SendRIPPacket(ripData []byte, intf *Interface) {
	// The simulator sends RIP directly over its custom IP protocol to the neighbor.
	nbr := get_nbr_node(intf)
	if nbr == nil {
		return
	}

	// Get neighbor's interface IP
	remoteIntf := get_remote_interface(intf)
	if remoteIntf == nil || !remoteIntf.IsIPConfigured() {
		return
	}

	nbrIP := remoteIntf.GetIP().String()

	nbrIPInt, err := IPStringToUint32(nbrIP)
	if err != nil {
		return
	}

	// Send through the normal IP/L2 path. On an ARP miss it queues the packet,
	// starts resolution, and transmits the queued RIP update after ARP succeeds.
	DemotePacketToLayer3(rip.node, ripData, len(ripData), 200, nbrIPInt)
}

// ProcessRIPPacket handles incoming RIP packets
func (rip *RIPState) ProcessRIPPacket(packet *RIPPacket, srcIP string, recvIntf *Interface) {
	rip.mutex.RLock()
	enabled := rip.enabled
	rip.mutex.RUnlock()
	if !enabled {
		return
	}

	nodeName := get_node_name(rip.node)

	if packet.Command == RIP_COMMAND_REQUEST {
		LogDebug("RIP: Node %s received RIP request from %s", nodeName, srcIP)
		// Respond with routing table
		rip.SendRoutingUpdates()
		return
	}

	if packet.Command == RIP_COMMAND_RESPONSE {
		LogInfo("RIP: Node %s received RIP response from %s (%d entries)",
			nodeName, srcIP, len(packet.Entries))
		emitInterfaceEvent(rip.node, recvIntf, "RIP", "rip_update_received", map[string]string{
			"entries":  fmt.Sprintf("%d", len(packet.Entries)),
			"sourceIp": srcIP,
		})

		// Process each route entry
		rip.mutex.Lock()
		client_events := make([]SimulationEvent, 0)
		routesChanged := false

		for _, entry := range packet.Entries {
			if entry.AddressFamily != RIP_ADDRESS_FAMILY_IP {
				continue
			}

			// Extract route info
			destIPStr := IPUint32ToString(entry.IPAddress)
			maskBits := subnetMaskToMaskBits(entry.SubnetMask)
			metric := entry.Metric

			// Apply mask to get network address
			networkAddr, err := ApplyMask(destIPStr, maskBits)
			if err != nil {
				continue
			}

			// Increment metric (distance from this router)
			newMetric := metric + 1
			if newMetric > RIP_MAX_METRIC {
				newMetric = RIP_MAX_METRIC
			}

			// Check if this is a better route
			routeKey := fmt.Sprintf("%s/%d", networkAddr, maskBits)
			existingRoute, exists := rip.routes[routeKey]

			intfName := get_interface_name(recvIntf)

			if newMetric < RIP_MAX_METRIC && (!exists || newMetric < existingRoute.Metric) {
				// New or better route - install it
				LogInfo("RIP: Installing route %s/%d via %s (metric %d)",
					networkAddr, maskBits, srcIP, newMetric)

				newRoute := &RIPRoute{
					Destination: networkAddr,
					Mask:        maskBits,
					NextHop:     srcIP,
					Interface:   intfName,
					Metric:      newMetric,
					LastUpdate:  time.Now(),
					IsExpired:   false,
				}
				rip.routes[routeKey] = newRoute
				routesChanged = true
				client_events = append(client_events, SimulationEvent{
					Protocol: "RIP",
					Node:     nodeName,
					Action:   "rip_route_learned",
					Table:    ripRoutingTableReference(newRoute, "learned"),
					Fields: map[string]string{
						"interface": intfName,
						"metric":    fmt.Sprintf("%d", newMetric),
						"nextHop":   srcIP,
						"route":     routeKey,
					},
				})

				// Add to main routing table
				rt := rip.node.node_nw_prop.rt_table
				rt.AddRouteWithParams(
					networkAddr,
					maskBits,
					srcIP,
					intfName,
					ROUTE_SOURCE_RIP,
					uint8(ROUTE_SOURCE_RIP),
					newMetric,
				)

			} else if exists && srcIP == existingRoute.NextHop {
				// Update from same next hop - refresh
				oldMetric := existingRoute.Metric
				wasExpired := existingRoute.IsExpired
				now := time.Now()
				existingRoute.Metric = newMetric
				existingRoute.LastUpdate = now
				existingRoute.IsExpired = newMetric >= RIP_MAX_METRIC
				existingRoute.Interface = intfName
				if existingRoute.IsExpired && !wasExpired {
					existingRoute.ExpiredAt = now
				} else if !existingRoute.IsExpired {
					existingRoute.ExpiredAt = time.Time{}
				}
				if oldMetric != newMetric || wasExpired != existingRoute.IsExpired {
					routesChanged = true
				}
				if existingRoute.IsExpired {
					rip.node.node_nw_prop.rt_table.DeleteRouteFromSource(
						networkAddr,
						maskBits,
						ROUTE_SOURCE_RIP,
					)
				} else {
					rip.node.node_nw_prop.rt_table.AddRouteWithParams(
						networkAddr,
						maskBits,
						srcIP,
						intfName,
						ROUTE_SOURCE_RIP,
						uint8(ROUTE_SOURCE_RIP),
						newMetric,
					)
				}
				LogDebug("RIP: Refreshed route %s/%d", networkAddr, maskBits)
				action := "rip_route_refreshed"
				tableResult := "updated"
				if existingRoute.IsExpired {
					action = "rip_route_expired"
					tableResult = "expired"
				}
				client_events = append(client_events, SimulationEvent{
					Protocol: "RIP",
					Node:     nodeName,
					Action:   action,
					Table:    ripRoutingTableReference(existingRoute, tableResult),
					Fields: map[string]string{
						"interface": intfName,
						"metric":    fmt.Sprintf("%d", newMetric),
						"nextHop":   srcIP,
						"route":     routeKey,
					},
				})
			}
		}
		rip.mutex.Unlock()
		if routesChanged {
			rip.requestTriggeredUpdate()
		}
		for _, event := range client_events {
			emitNodeEventWithTable(rip.node, event.Protocol, event.Action, event.Fields, event.Table)
		}
	}
}

// CheckExpiredRoutes removes expired routes
func (rip *RIPState) CheckExpiredRoutes() {
	rip.mutex.Lock()

	now := time.Now()
	expiredRoutes := make([]string, 0)
	client_events := make([]SimulationEvent, 0)
	routesChanged := false
	rt := rip.node.node_nw_prop.rt_table

	for key, route := range rip.routes {
		age := now.Sub(route.LastUpdate)

		if age > RIP_TIMEOUT && !route.IsExpired {
			// Stop forwarding immediately, but retain a poisoned RIP copy until
			// garbage collection so neighbors learn that it is unreachable.
			route.IsExpired = true
			route.Metric = RIP_MAX_METRIC
			route.ExpiredAt = now
			rt.DeleteRouteFromSource(route.Destination, route.Mask, ROUTE_SOURCE_RIP)
			routesChanged = true
			LogInfo("RIP: Route %s expired", key)
			client_events = append(client_events, SimulationEvent{
				Protocol: "RIP",
				Node:     get_node_name(rip.node),
				Action:   "rip_route_expired",
				Fields:   map[string]string{"route": key},
				Table:    ripRoutingTableReference(route, "expired"),
			})
		}

		if route.IsExpired && !route.ExpiredAt.IsZero() && now.Sub(route.ExpiredAt) > RIP_GARBAGE_COLLECT {
			// Remove from RIP state once the poison-retention period ends.
			expiredRoutes = append(expiredRoutes, key)
		}
	}

	// Remove expired routes from RIP state after their poison has been retained
	// for the garbage-collection interval. They already left the FIB at timeout.
	for _, key := range expiredRoutes {
		route := rip.routes[key]
		LogInfo("RIP: Removing expired route %s", key)
		delete(rip.routes, key)
		client_events = append(client_events, SimulationEvent{
			Protocol: "RIP",
			Node:     get_node_name(rip.node),
			Action:   "rip_route_removed",
			Fields:   map[string]string{"route": key},
			Table:    ripRoutingTableReference(route, "removed"),
		})
	}
	rip.mutex.Unlock()
	if routesChanged {
		rip.requestTriggeredUpdate()
	}
	for _, event := range client_events {
		emitNodeEventWithTable(rip.node, event.Protocol, event.Action, event.Fields, event.Table)
	}
}

// subnetMaskToMaskBits converts subnet mask uint32 to CIDR bits
func subnetMaskToMaskBits(mask uint32) uint8 {
	bits := uint8(0)
	for mask != 0 {
		if mask&0x80000000 != 0 {
			bits++
		}
		mask <<= 1
	}
	return bits
}

// DumpRIPState prints RIP routing information
func (rip *RIPState) DumpRIPState() {
	nodeName := get_node_name(rip.node)

	rip.mutex.RLock()
	defer rip.mutex.RUnlock()

	fmt.Printf("\n=== RIP State for Node: %s ===\n", nodeName)
	fmt.Printf("Status: ")
	if rip.enabled {
		fmt.Printf("Enabled\n")
	} else {
		fmt.Printf("Disabled\n")
		return
	}

	fmt.Printf("\nLearned Routes:\n")
	fmt.Printf("%-20s %-6s %-20s %-16s %-8s %-10s\n",
		"Network", "Mask", "Next Hop", "Interface", "Metric", "Age")
	fmt.Printf("%-20s %-6s %-20s %-16s %-8s %-10s\n",
		"-------", "----", "--------", "---------", "------", "---")

	if len(rip.routes) == 0 {
		fmt.Printf("(none)\n")
	} else {
		now := time.Now()
		for _, route := range rip.routes {
			age := now.Sub(route.LastUpdate)
			status := ""
			if route.IsExpired {
				status = " (expired)"
			}
			fmt.Printf("%-20s %-6d %-20s %-16s %-8d %-10s%s\n",
				route.Destination, route.Mask, route.NextHop,
				route.Interface, route.Metric,
				formatDuration(age), status)
		}
	}
	fmt.Printf("\n")
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
