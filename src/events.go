package main

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

type SimulationEvent struct {
	Sequence      uint64
	Time          string
	Protocol      string
	Node          string
	From          string
	To            string
	Action        string
	Interface     string
	PeerInterface string
	// SourceInterface and DestinationInterface make link events unambiguous.
	// Interface remains the interface local to Node; PeerInterface is its peer.
	SourceInterface      string
	DestinationInterface string
	Size                 int
	Fields               map[string]string
	Table                *EventTableReference
}

type EventTableReference struct {
	Kind   string
	Result string
	Query  map[string]string
	Entry  map[string]string
}

type EventSink func(SimulationEvent)

type EventBus struct {
	mu          sync.Mutex
	sequence    uint64
	sink        EventSink
	pending     []pendingEvent
	dispatching bool
}

type pendingEvent struct {
	event SimulationEvent
	sink  EventSink
}

func NewEventBus() *EventBus {
	return &EventBus{}
}

func (bus *EventBus) SetSink(sink EventSink) {
	bus.mu.Lock()
	bus.sink = sink
	bus.mu.Unlock()
}

func (bus *EventBus) Emit(event SimulationEvent) {
	if bus == nil {
		return
	}
	bus.mu.Lock()
	bus.sequence++
	event.Sequence = bus.sequence
	if event.Time == "" {
		event.Time = time.Now().Format("15:04:05.000")
	}
	bus.pending = append(bus.pending, pendingEvent{event: event, sink: bus.sink})
	if bus.dispatching {
		bus.mu.Unlock()
		return
	}
	bus.dispatching = true
	bus.mu.Unlock()

	for {
		bus.mu.Lock()
		if len(bus.pending) == 0 {
			bus.pending = nil
			bus.dispatching = false
			bus.mu.Unlock()
			return
		}
		pending := bus.pending[0]
		bus.pending = bus.pending[1:]
		bus.mu.Unlock()
		if pending.sink != nil {
			pending.sink(pending.event)
		}
	}
}

func (graph *Graph) emitFrameEvent(action string, fromIntf, toIntf *Interface, frame []byte) {
	graph.emitFrameEventWithFields(action, fromIntf, toIntf, frame, nil)
}

func (graph *Graph) emitFrameEventWithFields(action string, fromIntf, toIntf *Interface, frame []byte, fields map[string]string) {
	if graph == nil || fromIntf == nil || toIntf == nil {
		return
	}
	from := get_node_name(fromIntf.att_node)
	to := get_node_name(toIntf.att_node)
	node := from
	localIntf := fromIntf
	peerIntf := toIntf
	if action == "frame_received" {
		node = to
		localIntf = toIntf
		peerIntf = fromIntf
	}
	graph.events.Emit(SimulationEvent{
		Protocol:             frameProtocol(frame),
		Node:                 node,
		From:                 from,
		To:                   to,
		Action:               action,
		Interface:            get_interface_name(localIntf),
		PeerInterface:        get_interface_name(peerIntf),
		SourceInterface:      get_interface_name(fromIntf),
		DestinationInterface: get_interface_name(toIntf),
		Size:                 len(frame),
		Fields:               frameEventFields(frame, fields),
	})
}

// frameEventFields records the packet facts customers need to follow one
// packet across links and verify the Layer 2 rewrite performed by a router.
func frameEventFields(frame []byte, extra map[string]string) map[string]string {
	fields := make(map[string]string, len(extra)+10)
	for key, value := range extra {
		fields[key] = value
	}
	if len(frame) < ETHERNET_HDR_SIZE {
		return fields
	}

	var destinationMAC, sourceMAC MacAddr
	copy(destinationMAC[:], frame[:6])
	copy(sourceMAC[:], frame[6:12])
	fields["sourceMac"] = sourceMAC.String()
	fields["destinationMac"] = destinationMAC.String()

	etherType := binary.BigEndian.Uint16(frame[12:14])
	payloadOffset := ETHERNET_HDR_SIZE
	if etherType == ETHERTYPE_VLAN && len(frame) >= VLAN_HEADER_SIZE {
		fields["vlan"] = fmt.Sprintf("%d", binary.BigEndian.Uint16(frame[14:16])&0x0fff)
		etherType = binary.BigEndian.Uint16(frame[16:18])
		payloadOffset = VLAN_HEADER_SIZE
	}
	if etherType != ETHERTYPE_IP || len(frame) < payloadOffset+20 {
		return fields
	}

	ipHeader, err := DeserializeIPHeader(frame[payloadOffset:])
	if err != nil {
		return fields
	}
	fields["sourceIp"] = IPUint32ToString(ipHeader.SrcIP)
	fields["destinationIp"] = IPUint32ToString(ipHeader.DstIP)
	fields["ttl"] = fmt.Sprintf("%d", ipHeader.TTL)

	headerLength := GetIPHeaderLen(ipHeader)
	icmpOffset := payloadOffset + headerLength
	if ipHeader.Protocol != PROTO_ICMP || len(frame) < icmpOffset+8 {
		return fields
	}
	icmpType := frame[icmpOffset]
	icmpCode := frame[icmpOffset+1]
	fields["icmpType"] = icmpMessageName(icmpType, icmpCode)
	if icmpType != ICMP_TYPE_ECHO_REQUEST && icmpType != ICMP_TYPE_ECHO_REPLY {
		return fields
	}
	identifier := binary.BigEndian.Uint16(frame[icmpOffset+4 : icmpOffset+6])
	sequence := binary.BigEndian.Uint16(frame[icmpOffset+6 : icmpOffset+8])
	fields["icmpIdentifier"] = fmt.Sprintf("%d", identifier)
	fields["icmpSequence"] = fmt.Sprintf("%d", sequence)
	fields["flowId"] = fmt.Sprintf("icmp-%d-%d", identifier, sequence)
	return fields
}

func (graph *Graph) emitEvent(event SimulationEvent) {
	if graph != nil && graph.events != nil {
		graph.events.Emit(event)
	}
}

func emitNodeEvent(node *Node, protocol, action string, fields map[string]string) {
	emitNodeEventWithTable(node, protocol, action, fields, nil)
}

func emitNodeEventWithTable(node *Node, protocol, action string, fields map[string]string, table *EventTableReference) {
	if node == nil || node.graph == nil {
		return
	}
	node.graph.emitEvent(SimulationEvent{
		Protocol: protocol,
		Node:     get_node_name(node),
		Action:   action,
		Fields:   fields,
		Table:    table,
	})
}

func emitInterfaceEvent(node *Node, intf *Interface, protocol, action string, fields map[string]string) {
	emitInterfaceEventWithTable(node, intf, protocol, action, fields, nil)
}

func emitInterfaceEventWithTable(node *Node, intf *Interface, protocol, action string, fields map[string]string, table *EventTableReference) {
	if node == nil || node.graph == nil {
		return
	}
	event := SimulationEvent{
		Protocol: protocol,
		Node:     get_node_name(node),
		Action:   action,
		Fields:   fields,
		Table:    table,
	}
	if intf != nil {
		event.Interface = get_interface_name(intf)
	}
	node.graph.emitEvent(event)
}

func ipProtocolName(protocol uint8) string {
	switch protocol {
	case PROTO_ICMP:
		return "ICMP"
	case IPPROTO_IPIP:
		return "IPIP"
	case 200:
		return "RIP"
	default:
		return "IP"
	}
}
