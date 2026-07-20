package main

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	LLDP_TLV_END                 uint8  = 0
	LLDP_TLV_CHASSIS_ID          uint8  = 1
	LLDP_TLV_PORT_ID             uint8  = 2
	LLDP_TLV_TTL                 uint8  = 3
	LLDP_TLV_SYSTEM_NAME         uint8  = 5
	LLDP_TLV_SYSTEM_DESCRIPTION  uint8  = 6
	LLDP_TLV_SYSTEM_CAPABILITIES uint8  = 7
	LLDP_CHASSIS_ID_LOCAL        uint8  = 7
	LLDP_PORT_ID_INTERFACE_NAME  uint8  = 5
	LLDP_DEFAULT_TTL             uint16 = 120
	LLDP_MAX_TLV_LENGTH                 = 511
)

const (
	LLDP_CAP_OTHER     uint16 = 1 << 0
	LLDP_CAP_REPEATER  uint16 = 1 << 1
	LLDP_CAP_BRIDGE    uint16 = 1 << 2
	LLDP_CAP_WLAN_AP   uint16 = 1 << 3
	LLDP_CAP_ROUTER    uint16 = 1 << 4
	LLDP_CAP_TELEPHONE uint16 = 1 << 5
	LLDP_CAP_DOCSIS    uint16 = 1 << 6
	LLDP_CAP_STATION   uint16 = 1 << 7
)

const (
	LLDP_ADVERTISEMENT_INTERVAL = 30 * time.Second
	LLDP_EXPIRATION_INTERVAL    = time.Second
)

var lldpMulticastMAC = MacAddr{0x01, 0x80, 0xC2, 0x00, 0x00, 0x0E}

type LLDPDU struct {
	ChassisIDSubtype      uint8
	ChassisID             []byte
	PortIDSubtype         uint8
	PortID                []byte
	TTL                   uint16
	SystemName            string
	SystemDescription     string
	SupportedCapabilities uint16
	EnabledCapabilities   uint16
}

type LLDPNeighbor struct {
	ChassisID             string
	PortID                string
	SystemName            string
	SystemDescription     string
	SourceMAC             MacAddr
	LocalInterface        string
	TTL                   uint16
	SupportedCapabilities uint16
	EnabledCapabilities   uint16
	LastUpdate            time.Time
	ExpiresAt             time.Time
}

type LLDPState struct {
	node      *Node
	enabled   bool
	neighbors map[string]*LLDPNeighbor
	mutex     sync.RWMutex
	stopCh    chan struct{}
	waitGroup sync.WaitGroup
}

func InitLLDPState(node *Node) *LLDPState {
	return &LLDPState{
		node:      node,
		neighbors: make(map[string]*LLDPNeighbor),
	}
}

func isLLDPMulticastMAC(mac *MacAddr) bool {
	return mac != nil && *mac == lldpMulticastMAC
}

func serializeLLDPTLV(tlvType uint8, value []byte) ([]byte, error) {
	if tlvType > 127 {
		return nil, fmt.Errorf("invalid LLDP TLV type %d", tlvType)
	}
	if len(value) > LLDP_MAX_TLV_LENGTH {
		return nil, fmt.Errorf("LLDP TLV value is too large: %d bytes", len(value))
	}

	buf := make([]byte, 2+len(value))
	header := uint16(tlvType)<<9 | uint16(len(value))
	binary.BigEndian.PutUint16(buf[:2], header)
	copy(buf[2:], value)
	return buf, nil
}

func appendLLDPTLV(dst []byte, tlvType uint8, value []byte) ([]byte, error) {
	tlv, err := serializeLLDPTLV(tlvType, value)
	if err != nil {
		return nil, err
	}
	return append(dst, tlv...), nil
}

func serializeLLDPDU(du *LLDPDU) ([]byte, error) {
	if du == nil || len(du.ChassisID) == 0 || len(du.PortID) == 0 {
		return nil, fmt.Errorf("LLDP chassis ID and port ID are required")
	}

	payload := make([]byte, 0, 96)
	chassisValue := append([]byte{du.ChassisIDSubtype}, du.ChassisID...)
	portValue := append([]byte{du.PortIDSubtype}, du.PortID...)
	ttlValue := make([]byte, 2)
	binary.BigEndian.PutUint16(ttlValue, du.TTL)

	var err error
	payload, err = appendLLDPTLV(payload, LLDP_TLV_CHASSIS_ID, chassisValue)
	if err != nil {
		return nil, err
	}
	payload, err = appendLLDPTLV(payload, LLDP_TLV_PORT_ID, portValue)
	if err != nil {
		return nil, err
	}
	payload, err = appendLLDPTLV(payload, LLDP_TLV_TTL, ttlValue)
	if err != nil {
		return nil, err
	}
	if du.SystemName != "" {
		payload, err = appendLLDPTLV(payload, LLDP_TLV_SYSTEM_NAME, []byte(du.SystemName))
		if err != nil {
			return nil, err
		}
	}
	if du.SystemDescription != "" {
		payload, err = appendLLDPTLV(payload, LLDP_TLV_SYSTEM_DESCRIPTION, []byte(du.SystemDescription))
		if err != nil {
			return nil, err
		}
	}

	capabilities := make([]byte, 4)
	binary.BigEndian.PutUint16(capabilities[:2], du.SupportedCapabilities)
	binary.BigEndian.PutUint16(capabilities[2:], du.EnabledCapabilities)
	payload, err = appendLLDPTLV(payload, LLDP_TLV_SYSTEM_CAPABILITIES, capabilities)
	if err != nil {
		return nil, err
	}
	return appendLLDPTLV(payload, LLDP_TLV_END, nil)
}

func deserializeLLDPDU(payload []byte) (*LLDPDU, error) {
	du := &LLDPDU{}
	offset := 0
	mandatoryStage := 0

	for offset+2 <= len(payload) {
		header := binary.BigEndian.Uint16(payload[offset : offset+2])
		offset += 2
		tlvType := uint8(header >> 9)
		length := int(header & LLDP_MAX_TLV_LENGTH)
		if offset+length > len(payload) {
			return nil, fmt.Errorf("truncated LLDP TLV type %d", tlvType)
		}
		value := payload[offset : offset+length]
		offset += length

		switch tlvType {
		case LLDP_TLV_END:
			if length != 0 {
				return nil, fmt.Errorf("invalid LLDP End TLV length %d", length)
			}
			if mandatoryStage != 3 {
				return nil, fmt.Errorf("LLDPDU is missing a mandatory TLV")
			}
			return du, nil
		case LLDP_TLV_CHASSIS_ID:
			if mandatoryStage != 0 || length < 2 {
				return nil, fmt.Errorf("invalid LLDP Chassis ID TLV")
			}
			du.ChassisIDSubtype = value[0]
			du.ChassisID = append([]byte(nil), value[1:]...)
			mandatoryStage = 1
		case LLDP_TLV_PORT_ID:
			if mandatoryStage != 1 || length < 2 {
				return nil, fmt.Errorf("invalid LLDP Port ID TLV")
			}
			du.PortIDSubtype = value[0]
			du.PortID = append([]byte(nil), value[1:]...)
			mandatoryStage = 2
		case LLDP_TLV_TTL:
			if mandatoryStage != 2 || length != 2 {
				return nil, fmt.Errorf("invalid LLDP TTL TLV")
			}
			du.TTL = binary.BigEndian.Uint16(value)
			mandatoryStage = 3
		case LLDP_TLV_SYSTEM_NAME:
			if mandatoryStage != 3 {
				return nil, fmt.Errorf("LLDP optional TLV precedes mandatory TLVs")
			}
			du.SystemName = string(value)
		case LLDP_TLV_SYSTEM_DESCRIPTION:
			if mandatoryStage != 3 {
				return nil, fmt.Errorf("LLDP optional TLV precedes mandatory TLVs")
			}
			du.SystemDescription = string(value)
		case LLDP_TLV_SYSTEM_CAPABILITIES:
			if mandatoryStage != 3 || length != 4 {
				return nil, fmt.Errorf("invalid LLDP System Capabilities TLV")
			}
			du.SupportedCapabilities = binary.BigEndian.Uint16(value[:2])
			du.EnabledCapabilities = binary.BigEndian.Uint16(value[2:])
		default:
			if mandatoryStage != 3 {
				return nil, fmt.Errorf("LLDP optional TLV precedes mandatory TLVs")
			}
		}
	}

	return nil, fmt.Errorf("LLDPDU has no End TLV")
}

func nodeLLDPCapabilities(node *Node) uint16 {
	if node == nil {
		return LLDP_CAP_OTHER
	}

	var capabilities uint16
	for _, intf := range node.intf {
		if intf == nil {
			continue
		}
		if IS_INTF_L3_MODE(intf) {
			capabilities |= LLDP_CAP_ROUTER
		} else {
			capabilities |= LLDP_CAP_BRIDGE
		}
	}
	if node.GetVlanInterfaceCount() > 0 {
		capabilities |= LLDP_CAP_BRIDGE | LLDP_CAP_ROUTER
	}
	if capabilities == 0 {
		capabilities = LLDP_CAP_OTHER
	}
	return capabilities
}

func buildLLDPDU(node *Node, intf *Interface, ttl uint16) *LLDPDU {
	capabilities := nodeLLDPCapabilities(node)
	return &LLDPDU{
		ChassisIDSubtype:      LLDP_CHASSIS_ID_LOCAL,
		ChassisID:             []byte(get_node_name(node)),
		PortIDSubtype:         LLDP_PORT_ID_INTERFACE_NAME,
		PortID:                []byte(get_interface_name(intf)),
		TTL:                   ttl,
		SystemName:            get_node_name(node),
		SystemDescription:     "Go Network Simulator",
		SupportedCapabilities: capabilities,
		EnabledCapabilities:   capabilities,
	}
}

func sendLLDPAdvertisement(node *Node, intf *Interface, ttl uint16) error {
	if node == nil || intf == nil || intf.link == nil {
		return fmt.Errorf("LLDP requires a connected interface")
	}

	payload, err := serializeLLDPDU(buildLLDPDU(node, intf, ttl))
	if err != nil {
		return err
	}
	frame := tag_packet_with_ethernet_hdr(payload, len(payload))
	if frame == nil {
		return fmt.Errorf("failed to create LLDP Ethernet frame")
	}
	frame.header.dst_mac = lldpMulticastMAC
	frame.header.src_mac = *intf.GetMac()
	frame.header.ethertype = ETHERTYPE_LLDP
	frameBytes := serialize_ethernet_frame(frame)
	action := "lldp_advertisement_created"
	if ttl == 0 {
		action = "lldp_shutdown_advertisement_created"
	}
	emitInterfaceEvent(node, intf, "LLDP", action, map[string]string{
		"ttl": fmt.Sprintf("%d", ttl),
	})
	return send_frame(frameBytes, len(frameBytes), intf)
}

func (lldp *LLDPState) SendAdvertisements() {
	if !lldp.IsEnabled() {
		return
	}
	lldp.sendAdvertisementsWithTTL(LLDP_DEFAULT_TTL)
}

func (lldp *LLDPState) sendAdvertisementsWithTTL(ttl uint16) {
	if lldp == nil || lldp.node == nil {
		return
	}
	for _, intf := range lldp.node.intf {
		if intf == nil || intf.link == nil {
			continue
		}
		if err := sendLLDPAdvertisement(lldp.node, intf, ttl); err != nil {
			LogWarn("LLDP: Node %s failed to advertise on %s: %v",
				get_node_name(lldp.node), get_interface_name(intf), err)
		}
	}
}

func (lldp *LLDPState) StartLLDP() {
	if lldp == nil || lldp.node == nil {
		return
	}

	lldp.mutex.Lock()
	if lldp.enabled {
		lldp.mutex.Unlock()
		return
	}
	lldp.enabled = true
	lldp.stopCh = make(chan struct{})
	stopCh := lldp.stopCh
	lldp.waitGroup.Add(1)
	lldp.mutex.Unlock()

	emitNodeEvent(lldp.node, "LLDP", "lldp_enabled", nil)
	lldp.SendAdvertisements()
	go lldp.run(stopCh)
}

func (lldp *LLDPState) run(stopCh <-chan struct{}) {
	defer lldp.waitGroup.Done()
	advertisementTicker := time.NewTicker(LLDP_ADVERTISEMENT_INTERVAL)
	expirationTicker := time.NewTicker(LLDP_EXPIRATION_INTERVAL)
	defer advertisementTicker.Stop()
	defer expirationTicker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-advertisementTicker.C:
			lldp.node.graph.runBackgroundProtocol(lldp.SendAdvertisements)
		case <-expirationTicker.C:
			lldp.node.graph.runBackgroundProtocol(func() { lldp.RemoveExpiredNeighbors() })
		}
	}
}

func (lldp *LLDPState) StopLLDP() {
	if lldp == nil {
		return
	}

	lldp.mutex.Lock()
	if !lldp.enabled {
		lldp.mutex.Unlock()
		return
	}
	lldp.enabled = false
	stopCh := lldp.stopCh
	lldp.stopCh = nil
	close(stopCh)
	lldp.mutex.Unlock()

	lldp.sendAdvertisementsWithTTL(0)
	lldp.waitGroup.Wait()
	lldp.mutex.Lock()
	lldp.neighbors = make(map[string]*LLDPNeighbor)
	lldp.mutex.Unlock()
	emitNodeEvent(lldp.node, "LLDP", "lldp_disabled", nil)
}

func (lldp *LLDPState) IsEnabled() bool {
	if lldp == nil {
		return false
	}
	lldp.mutex.RLock()
	defer lldp.mutex.RUnlock()
	return lldp.enabled
}

func formatLLDPID(subtype uint8, value []byte) string {
	if (subtype == 3 || subtype == 4) && len(value) == MAC_ADDR_SIZE {
		mac := MacAddr{}
		copy(mac[:], value)
		return mac.String()
	}
	if len(value) == 0 {
		return "-"
	}
	printable := true
	for _, b := range value {
		if b < 32 || b > 126 {
			printable = false
			break
		}
	}
	if printable {
		return string(value)
	}
	return fmt.Sprintf("%x", value)
}

func lldpNeighborKey(localInterface, chassisID, portID string) string {
	return localInterface + "\x00" + chassisID + "\x00" + portID
}

func (lldp *LLDPState) ProcessFrame(intf *Interface, ethHdr *EthernetHeader, payload []byte) error {
	if lldp == nil || intf == nil || ethHdr == nil {
		return fmt.Errorf("invalid LLDP receive context")
	}
	if !isLLDPMulticastMAC(&ethHdr.dst_mac) {
		return fmt.Errorf("invalid LLDP destination MAC %s", ethHdr.dst_mac.String())
	}
	if ethHdr.ethertype != ETHERTYPE_LLDP {
		return fmt.Errorf("invalid LLDP EtherType 0x%04x", ethHdr.ethertype)
	}
	if !lldp.IsEnabled() {
		return nil
	}

	du, err := deserializeLLDPDU(payload)
	if err != nil {
		return err
	}
	localInterface := get_interface_name(intf)
	chassisID := formatLLDPID(du.ChassisIDSubtype, du.ChassisID)
	portID := formatLLDPID(du.PortIDSubtype, du.PortID)
	key := lldpNeighborKey(localInterface, chassisID, portID)

	lldp.mutex.Lock()
	if !lldp.enabled {
		lldp.mutex.Unlock()
		return nil
	}
	if du.TTL == 0 {
		_, existed := lldp.neighbors[key]
		delete(lldp.neighbors, key)
		lldp.mutex.Unlock()
		if existed {
			emitInterfaceEvent(lldp.node, intf, "LLDP", "lldp_neighbor_removed", map[string]string{
				"chassisId": chassisID,
				"portId":    portID,
			})
		}
		return nil
	}

	now := time.Now()
	_, alreadyKnown := lldp.neighbors[key]
	lldp.neighbors[key] = &LLDPNeighbor{
		ChassisID:             chassisID,
		PortID:                portID,
		SystemName:            du.SystemName,
		SystemDescription:     du.SystemDescription,
		SourceMAC:             ethHdr.src_mac,
		LocalInterface:        localInterface,
		TTL:                   du.TTL,
		SupportedCapabilities: du.SupportedCapabilities,
		EnabledCapabilities:   du.EnabledCapabilities,
		LastUpdate:            now,
		ExpiresAt:             now.Add(time.Duration(du.TTL) * time.Second),
	}
	lldp.mutex.Unlock()
	action := "lldp_neighbor_refreshed"
	if !alreadyKnown {
		action = "lldp_neighbor_discovered"
	}
	emitInterfaceEvent(lldp.node, intf, "LLDP", action, map[string]string{
		"capabilities": formatLLDPCapabilities(du.EnabledCapabilities),
		"chassisId":    chassisID,
		"portId":       portID,
		"sourceMac":    ethHdr.src_mac.String(),
		"systemName":   du.SystemName,
		"ttl":          fmt.Sprintf("%d", du.TTL),
	})
	if !alreadyKnown && (lldp.node.graph == nil || !lldp.node.graph.lldpTraceRound.Load()) {
		if err := sendLLDPAdvertisement(lldp.node, intf, LLDP_DEFAULT_TTL); err != nil {
			LogWarn("LLDP: Node %s failed to respond on %s: %v",
				get_node_name(lldp.node), get_interface_name(intf), err)
		}
	}
	return nil
}

func (lldp *LLDPState) RemoveExpiredNeighbors() int {
	if lldp == nil {
		return 0
	}

	lldp.mutex.Lock()
	now := time.Now()
	removed := 0
	expired := make([]LLDPNeighbor, 0)
	for key, neighbor := range lldp.neighbors {
		if !now.Before(neighbor.ExpiresAt) {
			expired = append(expired, *neighbor)
			delete(lldp.neighbors, key)
			removed++
		}
	}
	lldp.mutex.Unlock()
	for _, neighbor := range expired {
		emitNodeEvent(lldp.node, "LLDP", "lldp_neighbor_expired", map[string]string{
			"chassisId":  neighbor.ChassisID,
			"interface":  neighbor.LocalInterface,
			"portId":     neighbor.PortID,
			"systemName": neighbor.SystemName,
		})
	}
	return removed
}

func (lldp *LLDPState) NeighborsSnapshot() []LLDPNeighbor {
	if lldp == nil {
		return nil
	}

	lldp.mutex.RLock()
	neighbors := make([]LLDPNeighbor, 0, len(lldp.neighbors))
	for _, neighbor := range lldp.neighbors {
		neighbors = append(neighbors, *neighbor)
	}
	lldp.mutex.RUnlock()
	sort.Slice(neighbors, func(i, j int) bool {
		if neighbors[i].LocalInterface != neighbors[j].LocalInterface {
			return neighbors[i].LocalInterface < neighbors[j].LocalInterface
		}
		if neighbors[i].ChassisID != neighbors[j].ChassisID {
			return neighbors[i].ChassisID < neighbors[j].ChassisID
		}
		return neighbors[i].PortID < neighbors[j].PortID
	})
	return neighbors
}

func formatLLDPCapabilities(capabilities uint16) string {
	names := make([]string, 0, 8)
	values := []struct {
		capability uint16
		name       string
	}{
		{LLDP_CAP_OTHER, "Other"},
		{LLDP_CAP_REPEATER, "Repeater"},
		{LLDP_CAP_BRIDGE, "Bridge"},
		{LLDP_CAP_WLAN_AP, "WLAN"},
		{LLDP_CAP_ROUTER, "Router"},
		{LLDP_CAP_TELEPHONE, "Telephone"},
		{LLDP_CAP_DOCSIS, "DOCSIS"},
		{LLDP_CAP_STATION, "Station"},
	}
	for _, value := range values {
		if capabilities&value.capability != 0 {
			names = append(names, value.name)
		}
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

func (lldp *LLDPState) DumpNeighbors() {
	if lldp == nil || lldp.node == nil {
		return
	}

	status := "Disabled"
	if lldp.IsEnabled() {
		status = "Enabled"
	}
	fmt.Printf("\n=== LLDP Neighbors for Node: %s ===\n", get_node_name(lldp.node))
	fmt.Printf("Status: %s\n", status)
	fmt.Printf("%-16s %-16s %-16s %-16s %-20s %s\n",
		"Local Interface", "System Name", "Chassis ID", "Port ID", "Capabilities", "TTL")
	fmt.Printf("%-16s %-16s %-16s %-16s %-20s %s\n",
		"---------------", "-----------", "----------", "-------", "------------", "---")

	neighbors := lldp.NeighborsSnapshot()
	if len(neighbors) == 0 {
		fmt.Println("(none)")
		fmt.Println()
		return
	}
	now := time.Now()
	for _, neighbor := range neighbors {
		remaining := int(neighbor.ExpiresAt.Sub(now).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		fmt.Printf("%-16s %-16s %-16s %-16s %-20s %ds\n",
			neighbor.LocalInterface,
			neighbor.SystemName,
			neighbor.ChassisID,
			neighbor.PortID,
			formatLLDPCapabilities(neighbor.EnabledCapabilities),
			remaining)
	}
	fmt.Println()
}

func layer2FrameRecvLLDP(node *Node, intf *Interface, ethHdr *EthernetHeader, pkt []byte, pktSize int) int {
	if node == nil || intf == nil || ethHdr == nil || pktSize < ETHERNET_HDR_SIZE || pktSize > len(pkt) {
		return -1
	}
	if node.lldp_state == nil {
		return 0
	}
	if !node.lldp_state.IsEnabled() {
		emitInterfaceEvent(node, intf, "LLDP", "lldp_frame_ignored", map[string]string{
			"reason": "lldp_disabled",
		})
		return 0
	}
	if err := node.lldp_state.ProcessFrame(intf, ethHdr, pkt[ETHERNET_HDR_SIZE:pktSize]); err != nil {
		LogWarn("LLDP: Node %s rejected frame on %s: %v",
			get_node_name(node), get_interface_name(intf), err)
		emitInterfaceEvent(node, intf, "LLDP", "frame_dropped", map[string]string{
			"reason": "invalid_lldp_frame",
		})
		return -1
	}
	return 0
}
