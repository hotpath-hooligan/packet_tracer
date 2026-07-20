package main

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ====== IEEE 802.1D Spanning Tree Protocol ======
//
// BPDUs are carried in 802.3 frames addressed to the Bridge Group Address
// 01:80:C2:00:00:00. The Ethernet type/length field carries the frame length
// (LLC header + BPDU) rather than an EtherType, and an LLC header with
// DSAP/SSAP 0x42 identifies the payload as a BPDU. Frames destined to the
// bridge group address are consumed by the bridge and never forwarded.

const (
	STP_PROTOCOL_ID      uint16 = 0x0000
	STP_VERSION_ID       uint8  = 0x00
	STP_BPDU_TYPE_CONFIG uint8  = 0x00
	STP_BPDU_TYPE_TCN    uint8  = 0x80

	STP_BPDU_SIZE   = 35 // Configuration BPDU, protocol ID through forward delay
	STP_TCN_SIZE    = 4  // Topology change notification BPDU
	STP_LLC_SIZE    = 3  // DSAP + SSAP + control
	STP_LLC_DSAP    = 0x42
	STP_LLC_SSAP    = 0x42
	STP_LLC_CONTROL = 0x03

	STP_FLAG_TOPOLOGY_CHANGE     uint8 = 0x01
	STP_FLAG_TOPOLOGY_CHANGE_ACK uint8 = 0x80
)

const (
	// Bridge priority is configured in steps of 4096; the low 12 bits of the
	// priority field carry the system ID extension in later revisions and are
	// kept zero here.
	STP_DEFAULT_BRIDGE_PRIORITY uint16 = 32768
	STP_BRIDGE_PRIORITY_STEP    uint16 = 4096
	STP_DEFAULT_PORT_PRIORITY   uint8  = 128
	// Default cost of a port whose link declares no cost (802.1D-1998 value
	// for a 100 Mb/s link).
	STP_DEFAULT_PATH_COST uint32 = 19
)

// Default timers (802.1D clause 8.10.2). Times are carried in BPDUs as
// 1/256ths of a second.
const (
	STP_HELLO_TIME    = 2 * time.Second
	STP_MAX_AGE       = 20 * time.Second
	STP_FORWARD_DELAY = 15 * time.Second
)

// stpBridgeGroupMAC is the reserved multicast address BPDUs are sent to.
var stpBridgeGroupMAC = MacAddr{0x01, 0x80, 0xC2, 0x00, 0x00, 0x00}

func isSTPBridgeGroupMAC(mac *MacAddr) bool {
	return mac != nil && *mac == stpBridgeGroupMAC
}

// ====== Bridge and port identifiers ======

// BridgeID is the 8-byte bridge identifier: a 2-byte configurable priority
// followed by the bridge's 6-byte MAC address. Numerically lower is better.
type BridgeID struct {
	Priority uint16
	Address  MacAddr
}

func (id BridgeID) String() string {
	return fmt.Sprintf("%d.%s", id.Priority, id.Address.String())
}

// Compare returns a negative number when id sorts before other (is "better"),
// zero when they are equal, and a positive number otherwise.
func (id BridgeID) Compare(other BridgeID) int {
	if id.Priority != other.Priority {
		if id.Priority < other.Priority {
			return -1
		}
		return 1
	}
	for i := 0; i < MAC_ADDR_SIZE; i++ {
		if id.Address[i] != other.Address[i] {
			if id.Address[i] < other.Address[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func serializeBridgeID(buf []byte, id BridgeID) {
	binary.BigEndian.PutUint16(buf[0:2], id.Priority)
	copy(buf[2:8], id.Address[:])
}

func deserializeBridgeID(buf []byte) BridgeID {
	id := BridgeID{Priority: binary.BigEndian.Uint16(buf[0:2])}
	copy(id.Address[:], buf[2:8])
	return id
}

// makePortID builds the 2-byte port identifier from a priority and a port
// number. Numerically lower is better.
func makePortID(priority uint8, portNumber uint8) uint16 {
	return uint16(priority)<<8 | uint16(portNumber)
}

func formatPortID(portID uint16) string {
	return fmt.Sprintf("%d.%d", portID>>8, portID&0xFF)
}

// ====== Port roles and states ======

type STPPortRole uint8

const (
	STP_ROLE_DISABLED   STPPortRole = iota // Not participating
	STP_ROLE_ROOT                          // Best path towards the root bridge
	STP_ROLE_DESIGNATED                    // Best path towards the root for its segment
	STP_ROLE_ALTERNATE                     // Redundant path, blocked to break the loop
)

func (role STPPortRole) String() string {
	switch role {
	case STP_ROLE_ROOT:
		return "root"
	case STP_ROLE_DESIGNATED:
		return "designated"
	case STP_ROLE_ALTERNATE:
		return "alternate"
	default:
		return "disabled"
	}
}

type STPPortState uint8

const (
	STP_STATE_DISABLED   STPPortState = iota // Administratively down
	STP_STATE_BLOCKING                       // Discards frames, listens to BPDUs
	STP_STATE_LISTENING                      // Converging, no learning yet
	STP_STATE_LEARNING                       // Learning MACs, not yet forwarding
	STP_STATE_FORWARDING                     // Fully operational
)

func (state STPPortState) String() string {
	switch state {
	case STP_STATE_BLOCKING:
		return "blocking"
	case STP_STATE_LISTENING:
		return "listening"
	case STP_STATE_LEARNING:
		return "learning"
	case STP_STATE_FORWARDING:
		return "forwarding"
	default:
		return "disabled"
	}
}

// ====== BPDU ======

// BPDU is a configuration BPDU. Timer fields are held in 1/256ths of a second
// exactly as they appear on the wire.
type BPDU struct {
	ProtocolID   uint16
	Version      uint8
	Type         uint8
	Flags        uint8
	RootID       BridgeID
	RootPathCost uint32
	BridgeID     BridgeID
	PortID       uint16
	MessageAge   uint16
	MaxAge       uint16
	HelloTime    uint16
	ForwardDelay uint16
}

func durationToBPDUTime(value time.Duration) uint16 {
	ticks := value.Seconds() * 256
	if ticks < 0 {
		return 0
	}
	if ticks > float64(^uint16(0)) {
		return ^uint16(0)
	}
	return uint16(ticks)
}

func bpduTimeToDuration(value uint16) time.Duration {
	return time.Duration(float64(value) / 256 * float64(time.Second))
}

func serializeBPDU(bpdu *BPDU) []byte {
	buf := make([]byte, STP_BPDU_SIZE)
	binary.BigEndian.PutUint16(buf[0:2], bpdu.ProtocolID)
	buf[2] = bpdu.Version
	buf[3] = bpdu.Type
	buf[4] = bpdu.Flags
	serializeBridgeID(buf[5:13], bpdu.RootID)
	binary.BigEndian.PutUint32(buf[13:17], bpdu.RootPathCost)
	serializeBridgeID(buf[17:25], bpdu.BridgeID)
	binary.BigEndian.PutUint16(buf[25:27], bpdu.PortID)
	binary.BigEndian.PutUint16(buf[27:29], bpdu.MessageAge)
	binary.BigEndian.PutUint16(buf[29:31], bpdu.MaxAge)
	binary.BigEndian.PutUint16(buf[31:33], bpdu.HelloTime)
	binary.BigEndian.PutUint16(buf[33:35], bpdu.ForwardDelay)
	return buf
}

func deserializeBPDU(buf []byte) (*BPDU, error) {
	if len(buf) < STP_TCN_SIZE {
		return nil, fmt.Errorf("buffer too small for BPDU: got %d bytes", len(buf))
	}

	bpdu := &BPDU{
		ProtocolID: binary.BigEndian.Uint16(buf[0:2]),
		Version:    buf[2],
		Type:       buf[3],
	}
	if bpdu.ProtocolID != STP_PROTOCOL_ID {
		return nil, fmt.Errorf("unsupported BPDU protocol identifier 0x%04x", bpdu.ProtocolID)
	}

	switch bpdu.Type {
	case STP_BPDU_TYPE_TCN:
		return bpdu, nil
	case STP_BPDU_TYPE_CONFIG:
	default:
		return nil, fmt.Errorf("unsupported BPDU type 0x%02x", bpdu.Type)
	}

	if len(buf) < STP_BPDU_SIZE {
		return nil, fmt.Errorf("configuration BPDU is truncated: got %d bytes, need %d",
			len(buf), STP_BPDU_SIZE)
	}

	bpdu.Flags = buf[4]
	bpdu.RootID = deserializeBridgeID(buf[5:13])
	bpdu.RootPathCost = binary.BigEndian.Uint32(buf[13:17])
	bpdu.BridgeID = deserializeBridgeID(buf[17:25])
	bpdu.PortID = binary.BigEndian.Uint16(buf[25:27])
	bpdu.MessageAge = binary.BigEndian.Uint16(buf[27:29])
	bpdu.MaxAge = binary.BigEndian.Uint16(buf[29:31])
	bpdu.HelloTime = binary.BigEndian.Uint16(buf[31:33])
	bpdu.ForwardDelay = binary.BigEndian.Uint16(buf[33:35])

	if bpdu.MessageAge >= bpdu.MaxAge {
		return nil, fmt.Errorf("BPDU message age %d exceeds max age %d", bpdu.MessageAge, bpdu.MaxAge)
	}
	return bpdu, nil
}

// buildSTPFrame wraps a BPDU in the 802.3 + LLC encapsulation used by 802.1D.
func buildSTPFrame(srcMAC MacAddr, payload []byte) []byte {
	llcLength := STP_LLC_SIZE + len(payload)
	frame := make([]byte, ETHERNET_HDR_SIZE+llcLength)

	copy(frame[0:6], stpBridgeGroupMAC[:])
	copy(frame[6:12], srcMAC[:])
	// 802.3 frames carry a length here rather than an EtherType.
	binary.BigEndian.PutUint16(frame[12:14], uint16(llcLength))
	frame[14] = STP_LLC_DSAP
	frame[15] = STP_LLC_SSAP
	frame[16] = STP_LLC_CONTROL
	copy(frame[17:], payload)

	return frame
}

// parseSTPFrame strips the 802.3/LLC encapsulation and returns the BPDU bytes.
func parseSTPFrame(frame []byte) ([]byte, error) {
	if len(frame) < ETHERNET_HDR_SIZE+STP_LLC_SIZE {
		return nil, fmt.Errorf("frame too small to contain an LLC header")
	}
	if frame[14] != STP_LLC_DSAP || frame[15] != STP_LLC_SSAP {
		return nil, fmt.Errorf("frame is not addressed to the bridge spanning tree LLC SAP")
	}
	if frame[16] != STP_LLC_CONTROL {
		return nil, fmt.Errorf("unsupported LLC control field 0x%02x", frame[16])
	}

	llcLength := int(binary.BigEndian.Uint16(frame[12:14]))
	payload := frame[ETHERNET_HDR_SIZE+STP_LLC_SIZE:]
	// The length field covers the LLC header and the BPDU. Trust it only when
	// it fits inside the frame we actually received.
	if llcLength >= STP_LLC_SIZE && llcLength-STP_LLC_SIZE <= len(payload) {
		payload = payload[:llcLength-STP_LLC_SIZE]
	}
	return payload, nil
}

// ====== Priority vectors ======

// stpPriorityVector is the ordered tuple 802.1D uses to rank spanning tree
// information: root bridge, cost to that root, the bridge that sent the
// information, and the port it was sent from.
type stpPriorityVector struct {
	RootID       BridgeID
	RootPathCost uint32
	BridgeID     BridgeID
	PortID       uint16
}

// Compare returns a negative number when the receiver is superior.
func (vector stpPriorityVector) Compare(other stpPriorityVector) int {
	if result := vector.RootID.Compare(other.RootID); result != 0 {
		return result
	}
	if vector.RootPathCost != other.RootPathCost {
		if vector.RootPathCost < other.RootPathCost {
			return -1
		}
		return 1
	}
	if result := vector.BridgeID.Compare(other.BridgeID); result != 0 {
		return result
	}
	if vector.PortID != other.PortID {
		if vector.PortID < other.PortID {
			return -1
		}
		return 1
	}
	return 0
}

// ====== Port and bridge state ======

type STPPort struct {
	intf     *Interface
	portID   uint16
	pathCost uint32
	role     STPPortRole
	state    STPPortState

	// Best BPDU received on this port, when hasBPDU is set.
	hasBPDU  bool
	received stpPriorityVector
	// messageAge carried by the recorded BPDU, used to age it out.
	messageAge time.Duration
	receivedAt time.Time

	// stateDeadline is when a listening or learning port advances, and is zero
	// when no transition is pending.
	stateDeadline time.Time
}

func (port *STPPort) name() string {
	return get_interface_name(port.intf)
}

type STPState struct {
	node    *Node
	mutex   sync.RWMutex
	enabled bool

	priority     uint16
	bridgeID     BridgeID
	rootID       BridgeID
	rootPathCost uint32
	rootPortName string

	ports map[string]*STPPort

	helloTime    time.Duration
	maxAge       time.Duration
	forwardDelay time.Duration

	// topologyChange is set while this bridge is signalling a topology change.
	topologyChange bool

	stopCh    chan struct{}
	waitGroup sync.WaitGroup
}

func InitSTPState(node *Node) *STPState {
	return &STPState{
		node:         node,
		priority:     STP_DEFAULT_BRIDGE_PRIORITY,
		ports:        make(map[string]*STPPort),
		helloTime:    STP_HELLO_TIME,
		maxAge:       STP_MAX_AGE,
		forwardDelay: STP_FORWARD_DELAY,
	}
}

func (stp *STPState) IsEnabled() bool {
	if stp == nil {
		return false
	}
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	return stp.enabled
}

// bridgeAddress derives the bridge's MAC address from the numerically lowest
// interface MAC, so that a bridge identifier is stable for a given node.
func bridgeAddress(node *Node) MacAddr {
	var address MacAddr
	found := false
	for _, intf := range node.intf {
		if intf == nil {
			continue
		}
		mac := intf.GetMac()
		if mac == nil {
			continue
		}
		if !found {
			address = *mac
			found = true
			continue
		}
		candidate := BridgeID{Address: *mac}
		current := BridgeID{Address: address}
		if candidate.Compare(current) < 0 {
			address = *mac
		}
	}
	return address
}

// SetPriority sets the bridge priority. 802.1D requires a multiple of 4096.
func (stp *STPState) SetPriority(priority uint16) bool {
	if stp == nil {
		return false
	}
	if priority%STP_BRIDGE_PRIORITY_STEP != 0 {
		LogError("STP: Bridge priority %d must be a multiple of %d", priority, STP_BRIDGE_PRIORITY_STEP)
		return false
	}

	stp.mutex.Lock()
	stp.priority = priority
	stp.bridgeID.Priority = priority
	enabled := stp.enabled
	if enabled {
		// Re-seed the tree from the new identifier.
		stp.rootID = stp.bridgeID
		stp.rootPathCost = 0
		stp.rootPortName = ""
	}
	stp.mutex.Unlock()

	if enabled {
		stp.Converge()
	}
	return true
}

// stpPortEligible reports whether an interface takes part in the spanning
// tree. Routed interfaces terminate frames rather than bridging them, so they
// are excluded.
func stpPortEligible(intf *Interface) bool {
	return intf != nil && intf.link != nil && intf.link.IsUp() && !IS_INTF_L3_MODE(intf)
}

// syncPorts rebuilds the port table from the node's current interfaces.
// Callers must hold the write lock.
func (stp *STPState) syncPorts() {
	seen := make(map[string]bool, len(stp.ports))

	for index, intf := range stp.node.intf {
		if !stpPortEligible(intf) {
			continue
		}
		name := get_interface_name(intf)
		seen[name] = true
		if port, exists := stp.ports[name]; exists {
			port.intf = intf
			continue
		}

		pathCost := STP_DEFAULT_PATH_COST
		if intf.link != nil && intf.link.cost > 0 {
			pathCost = intf.link.cost
		}
		stp.ports[name] = &STPPort{
			intf:     intf,
			portID:   makePortID(STP_DEFAULT_PORT_PRIORITY, uint8(index+1)),
			pathCost: pathCost,
			role:     STP_ROLE_DESIGNATED,
			state:    STP_STATE_BLOCKING,
		}
	}

	for name := range stp.ports {
		if !seen[name] {
			delete(stp.ports, name)
		}
	}
}

func (stp *STPState) resetForTopologyChange() {
	if stp == nil || stp.node == nil || !stp.IsEnabled() {
		return
	}

	stp.mutex.Lock()
	stp.bridgeID = BridgeID{Priority: stp.priority, Address: bridgeAddress(stp.node)}
	stp.rootID = stp.bridgeID
	stp.rootPathCost = 0
	stp.rootPortName = ""
	stp.syncPorts()
	for _, port := range stp.ports {
		port.role = STP_ROLE_DESIGNATED
		port.state = STP_STATE_BLOCKING
		port.hasBPDU = false
		port.messageAge = 0
		port.receivedAt = time.Time{}
		port.stateDeadline = time.Time{}
	}
	stp.topologyChange = true
	stp.mutex.Unlock()
}

// StartSTP enables the spanning tree on this bridge.
func (stp *STPState) StartSTP() {
	if stp == nil || stp.node == nil {
		return
	}

	stp.mutex.Lock()
	if stp.enabled {
		stp.mutex.Unlock()
		return
	}
	stp.enabled = true
	stp.bridgeID = BridgeID{Priority: stp.priority, Address: bridgeAddress(stp.node)}
	// A bridge starts by believing it is the root of the tree.
	stp.rootID = stp.bridgeID
	stp.rootPathCost = 0
	stp.rootPortName = ""
	stp.syncPorts()
	stp.stopCh = make(chan struct{})
	stopCh := stp.stopCh
	bridgeID := stp.bridgeID
	stp.waitGroup.Add(1)
	stp.mutex.Unlock()

	emitNodeEvent(stp.node, "STP", "stp_enabled", map[string]string{
		"bridgeId": bridgeID.String(),
	})

	stp.Converge()
	go stp.run(stopCh)
}

// StopSTP disables the spanning tree and returns every port to forwarding, so
// that a bridge with the protocol turned off behaves like a plain switch.
func (stp *STPState) StopSTP() {
	if stp == nil {
		return
	}

	stp.mutex.Lock()
	if !stp.enabled {
		stp.mutex.Unlock()
		return
	}
	stp.enabled = false
	stopCh := stp.stopCh
	stp.stopCh = nil
	for _, port := range stp.ports {
		port.role = STP_ROLE_DISABLED
		port.state = STP_STATE_DISABLED
		port.hasBPDU = false
		port.stateDeadline = time.Time{}
	}
	stp.mutex.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	stp.waitGroup.Wait()
	emitNodeEvent(stp.node, "STP", "stp_disabled", nil)
}

func (stp *STPState) run(stopCh <-chan struct{}) {
	defer stp.waitGroup.Done()

	stp.mutex.RLock()
	hello := stp.helloTime
	stp.mutex.RUnlock()
	if hello <= 0 {
		hello = STP_HELLO_TIME
	}

	helloTicker := time.NewTicker(hello)
	timerTicker := time.NewTicker(time.Second)
	defer helloTicker.Stop()
	defer timerTicker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-helloTicker.C:
			stp.node.graph.runBackgroundProtocol(stp.SendHello)
		case <-timerTicker.C:
			stp.node.graph.runBackgroundProtocol(func() {
				stp.ageOutBPDUs()
				stp.advancePortStates()
			})
		}
	}
}

// ====== Transmission ======

// configBPDU builds the BPDU this bridge would advertise from a given port.
// Callers must hold at least the read lock.
func (stp *STPState) configBPDU(port *STPPort, messageAge time.Duration) *BPDU {
	flags := uint8(0)
	if stp.topologyChange {
		flags |= STP_FLAG_TOPOLOGY_CHANGE
	}
	return &BPDU{
		ProtocolID:   STP_PROTOCOL_ID,
		Version:      STP_VERSION_ID,
		Type:         STP_BPDU_TYPE_CONFIG,
		Flags:        flags,
		RootID:       stp.rootID,
		RootPathCost: stp.rootPathCost,
		BridgeID:     stp.bridgeID,
		PortID:       port.portID,
		MessageAge:   durationToBPDUTime(messageAge),
		MaxAge:       durationToBPDUTime(stp.maxAge),
		HelloTime:    durationToBPDUTime(stp.helloTime),
		ForwardDelay: durationToBPDUTime(stp.forwardDelay),
	}
}

// pendingBPDU pairs a BPDU with the port it must be sent from, so frames can
// be transmitted after the state lock is released.
type pendingBPDU struct {
	port *STPPort
	bpdu *BPDU
}

// collectDesignatedBPDUs builds the BPDU for every designated port.
// Callers must hold at least the read lock.
func (stp *STPState) collectDesignatedBPDUs() []pendingBPDU {
	// The age of the information we are relaying: zero at the root, otherwise
	// the age recorded on the root port plus one hello interval.
	messageAge := time.Duration(0)
	if stp.rootPortName != "" {
		if rootPort, exists := stp.ports[stp.rootPortName]; exists && rootPort.hasBPDU {
			messageAge = rootPort.messageAge + stp.helloTime
		}
	}

	pending := make([]pendingBPDU, 0, len(stp.ports))
	for _, port := range stp.portsSorted() {
		if port.role != STP_ROLE_DESIGNATED || port.state == STP_STATE_DISABLED {
			continue
		}
		if messageAge >= stp.maxAge {
			// Information that has aged out must not be propagated further.
			continue
		}
		pending = append(pending, pendingBPDU{port: port, bpdu: stp.configBPDU(port, messageAge)})
	}
	return pending
}

// portsSorted returns ports ordered by port identifier so that behaviour does
// not depend on map iteration order. Callers must hold at least the read lock.
func (stp *STPState) portsSorted() []*STPPort {
	ports := make([]*STPPort, 0, len(stp.ports))
	for _, port := range stp.ports {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].portID < ports[j].portID })
	return ports
}

func (stp *STPState) transmit(pending []pendingBPDU) {
	for _, item := range pending {
		if !stp.node.graph.consumeSTPBudget() {
			LogWarn("STP: Node %s exhausted its BPDU budget; suppressing further transmissions",
				get_node_name(stp.node))
			return
		}
		mac := item.port.intf.GetMac()
		if mac == nil {
			continue
		}
		frame := buildSTPFrame(*mac, serializeBPDU(item.bpdu))
		if err := send_frame(frame, len(frame), item.port.intf); err != nil {
			LogDebug("STP: Node %s failed to send a BPDU on %s: %v",
				get_node_name(stp.node), item.port.name(), err)
		}
	}
}

// SendHello advertises this bridge's spanning tree information on every
// designated port.
func (stp *STPState) SendHello() {
	if !stp.IsEnabled() {
		return
	}
	defer stp.node.graph.beginSTPCascade()()

	stp.mutex.RLock()
	pending := stp.collectDesignatedBPDUs()
	stp.mutex.RUnlock()
	stp.transmit(pending)
}

// ====== Reception ======

func (stp *STPState) ProcessBPDU(intf *Interface, ethHdr *EthernetHeader, payload []byte) error {
	if stp == nil || intf == nil || ethHdr == nil {
		return fmt.Errorf("invalid BPDU receive context")
	}
	if !isSTPBridgeGroupMAC(&ethHdr.dst_mac) {
		return fmt.Errorf("BPDU is not addressed to the bridge group address")
	}
	if !stp.IsEnabled() {
		return nil
	}

	bpdu, err := deserializeBPDU(payload)
	if err != nil {
		return err
	}

	portName := get_interface_name(intf)

	if bpdu.Type == STP_BPDU_TYPE_TCN {
		return stp.processTopologyChangeNotification(intf, portName)
	}

	stp.mutex.Lock()
	port, exists := stp.ports[portName]
	if !exists {
		stp.mutex.Unlock()
		return fmt.Errorf("interface %s does not take part in the spanning tree", portName)
	}

	received := stpPriorityVector{
		RootID:       bpdu.RootID,
		RootPathCost: bpdu.RootPathCost,
		BridgeID:     bpdu.BridgeID,
		PortID:       bpdu.PortID,
	}

	// A BPDU sent by this bridge means the segment loops back on itself.
	if received.BridgeID.Compare(stp.bridgeID) == 0 {
		stp.mutex.Unlock()
		LogDebug("STP: Node %s received its own BPDU on %s (self-looped segment)",
			get_node_name(stp.node), portName)
		return nil
	}

	ownVector := stpPriorityVector{
		RootID:       stp.rootID,
		RootPathCost: stp.rootPathCost,
		BridgeID:     stp.bridgeID,
		PortID:       port.portID,
	}
	superior := !port.hasBPDU || received.Compare(port.received) < 0

	if !superior {
		// The neighbour's information is no better than what we already hold.
		// If we are the designated bridge for this segment, answer immediately
		// so the neighbour learns the better path.
		inferiorToUs := ownVector.Compare(received) < 0
		var reply []pendingBPDU
		if port.role == STP_ROLE_DESIGNATED && inferiorToUs {
			messageAge := time.Duration(0)
			if stp.rootPortName != "" {
				if rootPort, ok := stp.ports[stp.rootPortName]; ok && rootPort.hasBPDU {
					messageAge = rootPort.messageAge + stp.helloTime
				}
			}
			reply = []pendingBPDU{{port: port, bpdu: stp.configBPDU(port, messageAge)}}
		} else if received.Compare(port.received) == 0 {
			// An identical refresh: restart the ageing timer.
			port.receivedAt = time.Now()
			port.messageAge = bpduTimeToDuration(bpdu.MessageAge)
		}
		stp.mutex.Unlock()
		stp.transmit(reply)
		return nil
	}

	port.hasBPDU = true
	port.received = received
	port.messageAge = bpduTimeToDuration(bpdu.MessageAge)
	port.receivedAt = time.Now()
	// Adopt the root bridge's timers, as 802.1D requires all bridges in a tree
	// to run on the root's values.
	if received.RootID.Compare(stp.bridgeID) < 0 {
		stp.maxAge = bpduTimeToDuration(bpdu.MaxAge)
		stp.helloTime = bpduTimeToDuration(bpdu.HelloTime)
		stp.forwardDelay = bpduTimeToDuration(bpdu.ForwardDelay)
	}

	changes := stp.recomputeLocked()
	pending := stp.collectDesignatedBPDUs()
	stp.mutex.Unlock()

	stp.emitChanges(changes)
	// Only relay onward when our own view of the tree actually moved,
	// which bounds the exchange: priority vectors strictly improve.
	if len(changes) > 0 {
		stp.transmit(pending)
	}
	return nil
}

func (stp *STPState) processTopologyChangeNotification(intf *Interface, portName string) error {
	emitInterfaceEvent(stp.node, intf, "STP", "stp_topology_change_notified", map[string]string{
		"interface": portName,
	})
	stp.mutex.Lock()
	stp.topologyChange = true
	stp.mutex.Unlock()
	// A topology change shortens MAC ageing so stale entries do not blackhole
	// traffic across the new tree.
	flush_mac_table(&stp.node.node_nw_prop.mac_table)
	return nil
}

// ====== Spanning tree computation ======

// stpChange records a port whose role or state moved, for event reporting.
type stpChange struct {
	port     string
	role     STPPortRole
	state    STPPortState
	oldRole  STPPortRole
	oldState STPPortState
}

// recomputeLocked runs the spanning tree computation over the recorded BPDUs
// and reassigns port roles. Callers must hold the write lock.
func (stp *STPState) recomputeLocked() []stpChange {
	previousRoot := stp.rootID
	previousRootPort := stp.rootPortName

	ports := stp.portsSorted()
	before := make(map[string]stpChange, len(ports))
	for _, port := range ports {
		before[port.name()] = stpChange{oldRole: port.role, oldState: port.state}
	}

	// Step 1: elect the root bridge. It is the best bridge identifier we know
	// of, whether our own or one learned from a neighbour.
	rootID := stp.bridgeID
	for _, port := range ports {
		if port.hasBPDU && port.received.RootID.Compare(rootID) < 0 {
			rootID = port.received.RootID
		}
	}
	stp.rootID = rootID

	isRoot := rootID.Compare(stp.bridgeID) == 0

	// Step 2: choose the root port, the port with the cheapest path to the
	// root. Ties break on the sending bridge, then its port, then ours.
	var rootPort *STPPort
	var rootCost uint32
	if !isRoot {
		var bestVector stpPriorityVector
		for _, port := range ports {
			if !port.hasBPDU || port.received.RootID.Compare(rootID) != 0 {
				continue
			}
			candidate := stpPriorityVector{
				RootID:       rootID,
				RootPathCost: port.received.RootPathCost + port.pathCost,
				BridgeID:     port.received.BridgeID,
				PortID:       port.received.PortID,
			}
			if rootPort == nil || candidate.Compare(bestVector) < 0 {
				rootPort = port
				bestVector = candidate
			}
		}
		if rootPort != nil {
			rootCost = bestVector.RootPathCost
		}
	}

	stp.rootPathCost = rootCost
	if rootPort != nil {
		stp.rootPortName = rootPort.name()
	} else {
		stp.rootPortName = ""
	}

	// Step 3: assign a role to every port.
	for _, port := range ports {
		switch {
		case port == rootPort:
			stp.setPortRole(port, STP_ROLE_ROOT)
		default:
			// We are the designated bridge for this segment when the BPDU we
			// would send is better than anything we have received on it.
			ourVector := stpPriorityVector{
				RootID:       rootID,
				RootPathCost: rootCost,
				BridgeID:     stp.bridgeID,
				PortID:       port.portID,
			}
			if !port.hasBPDU || ourVector.Compare(port.received) < 0 {
				stp.setPortRole(port, STP_ROLE_DESIGNATED)
			} else {
				// A better bridge owns this segment: block the port to break
				// the loop, but keep listening to BPDUs on it.
				stp.setPortRole(port, STP_ROLE_ALTERNATE)
			}
		}
	}

	changes := make([]stpChange, 0)
	for _, port := range ports {
		previous := before[port.name()]
		if previous.oldRole == port.role && previous.oldState == port.state {
			continue
		}
		changes = append(changes, stpChange{
			port:     port.name(),
			role:     port.role,
			state:    port.state,
			oldRole:  previous.oldRole,
			oldState: previous.oldState,
		})
	}

	if previousRoot.Compare(stp.rootID) != 0 || previousRootPort != stp.rootPortName {
		stp.topologyChange = true
	}
	return changes
}

// setPortRole applies a role and starts the matching state transition.
// Callers must hold the write lock.
func (stp *STPState) setPortRole(port *STPPort, role STPPortRole) {
	previousRole := port.role
	port.role = role

	switch role {
	case STP_ROLE_ROOT, STP_ROLE_DESIGNATED:
		if port.state == STP_STATE_FORWARDING || port.state == STP_STATE_LEARNING {
			// Already converging towards forwarding; leave the timer alone.
			return
		}
		if stp.forwardDelay <= 0 {
			port.state = STP_STATE_FORWARDING
			port.stateDeadline = time.Time{}
			return
		}
		port.state = STP_STATE_LISTENING
		port.stateDeadline = time.Now().Add(stp.forwardDelay)
	default:
		// A port that loses its forwarding role blocks immediately: 802.1D
		// only delays the transition towards forwarding, never away from it.
		if previousRole != role && port.state == STP_STATE_FORWARDING {
			flush_mac_table_for_interface(&stp.node.node_nw_prop.mac_table, port.name())
		}
		port.state = STP_STATE_BLOCKING
		port.stateDeadline = time.Time{}
	}
}

// advancePortStates moves listening and learning ports forward once their
// forward delay expires.
func (stp *STPState) advancePortStates() {
	if !stp.IsEnabled() {
		return
	}

	stp.mutex.Lock()
	now := time.Now()
	changes := make([]stpChange, 0)
	for _, port := range stp.portsSorted() {
		if port.stateDeadline.IsZero() || now.Before(port.stateDeadline) {
			continue
		}
		oldState := port.state
		switch port.state {
		case STP_STATE_LISTENING:
			port.state = STP_STATE_LEARNING
			port.stateDeadline = now.Add(stp.forwardDelay)
		case STP_STATE_LEARNING:
			port.state = STP_STATE_FORWARDING
			port.stateDeadline = time.Time{}
		default:
			port.stateDeadline = time.Time{}
			continue
		}
		changes = append(changes, stpChange{
			port: port.name(), role: port.role, state: port.state,
			oldRole: port.role, oldState: oldState,
		})
	}
	stp.mutex.Unlock()

	stp.emitChanges(changes)
}

// ageOutBPDUs discards spanning tree information that has not been refreshed
// within max age, then recomputes the tree.
func (stp *STPState) ageOutBPDUs() {
	if !stp.IsEnabled() {
		return
	}
	defer stp.node.graph.beginSTPCascade()()

	stp.mutex.Lock()
	now := time.Now()
	aged := false
	for _, port := range stp.portsSorted() {
		if !port.hasBPDU {
			continue
		}
		// Information ages from the moment it left the root bridge.
		if now.Sub(port.receivedAt)+port.messageAge <= stp.maxAge {
			continue
		}
		LogInfo("STP: Node %s aged out the BPDU recorded on %s",
			get_node_name(stp.node), port.name())
		port.hasBPDU = false
		port.received = stpPriorityVector{}
		port.messageAge = 0
		aged = true
	}
	if !aged {
		stp.mutex.Unlock()
		return
	}

	changes := stp.recomputeLocked()
	pending := stp.collectDesignatedBPDUs()
	stp.mutex.Unlock()

	stp.emitChanges(changes)
	stp.transmit(pending)
}

func (stp *STPState) emitChanges(changes []stpChange) {
	for _, change := range changes {
		intf := get_node_if_by_name(stp.node, change.port)
		emitInterfaceEvent(stp.node, intf, "STP", "stp_port_role_changed", map[string]string{
			"interface":     change.port,
			"role":          change.role.String(),
			"state":         change.state.String(),
			"previousRole":  change.oldRole.String(),
			"previousState": change.oldState.String(),
			"rootBridge":    stp.RootBridgeID().String(),
		})
	}
}

// ====== Convergence ======

// Converge advertises this bridge's view of the tree. Because frames are
// delivered synchronously, neighbours process each BPDU and relay their own
// before this returns, so the tree settles without waiting for hello timers.
func (stp *STPState) Converge() {
	if !stp.IsEnabled() {
		return
	}
	defer stp.node.graph.beginSTPCascade()()

	stp.mutex.Lock()
	changes := stp.recomputeLocked()
	pending := stp.collectDesignatedBPDUs()
	stp.mutex.Unlock()

	stp.emitChanges(changes)
	stp.transmit(pending)
}

// ConvergeSpanningTree settles the spanning tree across every bridge in the
// graph. Bridges are seeded in a deterministic order and the exchange is
// repeated until no bridge changes its mind.
func ConvergeSpanningTree(graph *Graph) {
	if graph == nil {
		return
	}
	graph.resetSTPBudget()

	// Each round lets every bridge advertise; a bridge that learns something
	// better relays it immediately. Rounds are bounded by the number of
	// bridges, which is the diameter bound for a distance vector of this kind.
	rounds := len(graph.node_list) + 1
	for round := 0; round < rounds; round++ {
		converged := true
		for _, node := range graph.node_list {
			if node == nil || !node.stp_state.IsEnabled() {
				continue
			}
			before := node.stp_state.convergenceMark()
			node.stp_state.Converge()
			if before != node.stp_state.convergenceMark() {
				converged = false
			}
		}
		if converged {
			break
		}
	}

	// Ports converge through listening and learning under the hello timers.
	// A topology load has no traffic to protect yet, so ports that hold a
	// forwarding role are promoted directly.
	for _, node := range graph.node_list {
		if node != nil {
			node.stp_state.promoteForwardingPorts()
		}
	}
}

// stpConvergenceMark is a comparable summary of a bridge's position in the
// tree, used to detect that the computation has settled.
type stpConvergenceMark struct {
	rootID       BridgeID
	rootPathCost uint32
	rootPort     string
	roles        string
}

func (stp *STPState) convergenceMark() stpConvergenceMark {
	if stp == nil {
		return stpConvergenceMark{}
	}
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()

	roles := make([]byte, 0, len(stp.ports)*2)
	for _, port := range stp.portsSorted() {
		roles = append(roles, byte(port.role), byte(port.state))
	}
	return stpConvergenceMark{
		rootID:       stp.rootID,
		rootPathCost: stp.rootPathCost,
		rootPort:     stp.rootPortName,
		roles:        string(roles),
	}
}

// promoteForwardingPorts skips the listening and learning delays for ports
// that already hold a forwarding role.
func (stp *STPState) promoteForwardingPorts() {
	if !stp.IsEnabled() {
		return
	}

	stp.mutex.Lock()
	changes := make([]stpChange, 0)
	for _, port := range stp.portsSorted() {
		if port.role != STP_ROLE_ROOT && port.role != STP_ROLE_DESIGNATED {
			continue
		}
		if port.state == STP_STATE_FORWARDING {
			continue
		}
		oldState := port.state
		port.state = STP_STATE_FORWARDING
		port.stateDeadline = time.Time{}
		changes = append(changes, stpChange{
			port: port.name(), role: port.role, state: port.state,
			oldRole: port.role, oldState: oldState,
		})
	}
	stp.mutex.Unlock()

	stp.emitChanges(changes)
}

// ====== Queries used by the forwarding path ======

// stp_port_can_forward reports whether the switching path may send or receive
// data frames on an interface. Interfaces on a bridge that is not running the
// spanning tree always forward.
func stp_port_can_forward(node *Node, intf *Interface) bool {
	if node == nil || intf == nil || intf.link == nil || !intf.link.IsUp() {
		return false
	}
	if !node.stp_state.IsEnabled() {
		return true
	}
	stp := node.stp_state
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	port, exists := stp.ports[get_interface_name(intf)]
	if !exists {
		return true
	}
	return port.state == STP_STATE_FORWARDING
}

// stp_port_can_learn reports whether MAC learning is permitted on an
// interface. Learning starts one state before forwarding.
func stp_port_can_learn(node *Node, intf *Interface) bool {
	if node == nil || intf == nil || intf.link == nil || !intf.link.IsUp() {
		return false
	}
	if !node.stp_state.IsEnabled() {
		return true
	}
	stp := node.stp_state
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	port, exists := stp.ports[get_interface_name(intf)]
	if !exists {
		return true
	}
	return port.state == STP_STATE_LEARNING || port.state == STP_STATE_FORWARDING
}

// stp_port_state_name reports the spanning tree state of an interface for
// logging and events, or an empty string when the bridge is not running it.
func stp_port_state_name(node *Node, intf *Interface) string {
	if node == nil || intf == nil || !node.stp_state.IsEnabled() {
		return ""
	}
	stp := node.stp_state
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	port, exists := stp.ports[get_interface_name(intf)]
	if !exists {
		return ""
	}
	return port.state.String()
}

func (stp *STPState) RootBridgeID() BridgeID {
	if stp == nil {
		return BridgeID{}
	}
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	return stp.rootID
}

func (stp *STPState) BridgeIdentifier() BridgeID {
	if stp == nil {
		return BridgeID{}
	}
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	return stp.bridgeID
}

func (stp *STPState) IsRootBridge() bool {
	if stp == nil {
		return false
	}
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	return stp.enabled && stp.rootID.Compare(stp.bridgeID) == 0
}

// STPPortStatus is a read-only view of a port for snapshots and dumps.
type STPPortStatus struct {
	Interface string
	Role      string
	State     string
	PathCost  uint32
	PortID    string
}

func (stp *STPState) PortStatusSnapshot() []STPPortStatus {
	if stp == nil {
		return nil
	}
	stp.mutex.RLock()
	defer stp.mutex.RUnlock()

	status := make([]STPPortStatus, 0, len(stp.ports))
	for _, port := range stp.portsSorted() {
		status = append(status, STPPortStatus{
			Interface: port.name(),
			Role:      port.role.String(),
			State:     port.state.String(),
			PathCost:  port.pathCost,
			PortID:    formatPortID(port.portID),
		})
	}
	return status
}

// BridgeSummary reports this bridge's position in the spanning tree.
func (stp *STPState) BridgeSummary() STPBridgeState {
	if stp == nil {
		return STPBridgeState{Ports: make([]STPPortStatus, 0)}
	}

	ports := stp.PortStatusSnapshot()
	if ports == nil {
		ports = make([]STPPortStatus, 0)
	}

	stp.mutex.RLock()
	defer stp.mutex.RUnlock()
	return STPBridgeState{
		Enabled:      stp.enabled,
		Priority:     stp.priority,
		BridgeID:     stp.bridgeID.String(),
		RootID:       stp.rootID.String(),
		IsRoot:       stp.enabled && stp.rootID.Compare(stp.bridgeID) == 0,
		RootPathCost: stp.rootPathCost,
		RootPort:     stp.rootPortName,
		Ports:        ports,
	}
}

func (stp *STPState) DumpSTPState() {
	if stp == nil || stp.node == nil {
		return
	}

	fmt.Printf("\n=== Spanning Tree for Node: %s ===\n", get_node_name(stp.node))
	if !stp.IsEnabled() {
		fmt.Printf("Status: Disabled\n\n")
		return
	}

	stp.mutex.RLock()
	bridgeID := stp.bridgeID
	rootID := stp.rootID
	rootCost := stp.rootPathCost
	rootPort := stp.rootPortName
	stp.mutex.RUnlock()

	fmt.Printf("Status: Enabled\n")
	fmt.Printf("Bridge ID: %s\n", bridgeID.String())
	fmt.Printf("Root ID:   %s", rootID.String())
	if rootID.Compare(bridgeID) == 0 {
		fmt.Printf("  (this bridge is the root)\n")
	} else {
		fmt.Printf("  cost %d via %s\n", rootCost, rootPort)
	}

	fmt.Printf("%-16s %-12s %-12s %-10s %s\n", "Interface", "Role", "State", "Cost", "Port ID")
	fmt.Printf("%-16s %-12s %-12s %-10s %s\n", "---------", "----", "-----", "----", "-------")
	for _, port := range stp.PortStatusSnapshot() {
		fmt.Printf("%-16s %-12s %-12s %-10d %s\n",
			port.Interface, port.Role, port.State, port.PathCost, port.PortID)
	}
	fmt.Println()
}

// layer2FrameRecvSTP is the entry point for frames addressed to the bridge
// group address.
func layer2FrameRecvSTP(node *Node, intf *Interface, ethHdr *EthernetHeader, pkt []byte, pktSize int) int {
	if node == nil || intf == nil || ethHdr == nil || pktSize < ETHERNET_HDR_SIZE || pktSize > len(pkt) {
		return -1
	}
	if node.stp_state == nil || !node.stp_state.IsEnabled() {
		// BPDUs are link-local: a bridge that is not running the spanning tree
		// still must not forward them.
		emitInterfaceEvent(node, intf, "STP", "stp_frame_ignored", map[string]string{
			"reason": "stp_disabled",
		})
		return 0
	}

	payload, err := parseSTPFrame(pkt[:pktSize])
	if err == nil {
		err = node.stp_state.ProcessBPDU(intf, ethHdr, payload)
	}
	if err != nil {
		LogWarn("STP: Node %s rejected a BPDU on %s: %v",
			get_node_name(node), get_interface_name(intf), err)
		emitInterfaceEvent(node, intf, "STP", "frame_dropped", map[string]string{
			"reason": "invalid_bpdu",
		})
		return -1
	}
	return 0
}
