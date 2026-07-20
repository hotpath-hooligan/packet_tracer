package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ====== ARP (Address Resolution Protocol) ======

// ARP operation codes
const (
	ARP_OP_REQUEST = 1 // ARP request
	ARP_OP_REPLY   = 2 // ARP reply
)

// ARP hardware and protocol types
const (
	ARP_HW_TYPE_ETHERNET = 1      // Ethernet
	ARP_PROTO_TYPE_IP    = 0x0800 // IPv4
	ARP_HW_ADDR_LEN      = 6      // MAC address length
	ARP_PROTO_ADDR_LEN   = 4      // IPv4 address length
	ARP_HDR_SIZE         = 28     // ARP header size (fixed)
)

// arp_hdr_t represents ARP header format
type arp_hdr_t struct {
	hw_type        uint16  // Hardware type (1 for Ethernet)
	proto_type     uint16  // Protocol type (0x0800 for IPv4)
	hw_addr_len    uint8   // Hardware address length (6 for MAC)
	proto_addr_len uint8   // Protocol address length (4 for IPv4)
	op_code        uint16  // Operation code (request=1, reply=2)
	src_mac        MacAddr // Source MAC address
	src_ip         uint32  // Source IP address (network byte order)
	dst_mac        MacAddr // Destination MAC address
	dst_ip         uint32  // Destination IP address (network byte order)
}

func serialize_arp_header(hdr *arp_hdr_t) []byte {
	buffer := make([]byte, ARP_HDR_SIZE)

	// hw_type (2 bytes, big-endian)
	binary.BigEndian.PutUint16(buffer[0:2], hdr.hw_type)

	// proto_type (2 bytes, big-endian)
	binary.BigEndian.PutUint16(buffer[2:4], hdr.proto_type)

	// hw_addr_len (1 byte)
	buffer[4] = hdr.hw_addr_len

	// proto_addr_len (1 byte)
	buffer[5] = hdr.proto_addr_len

	// op_code (2 bytes, big-endian)
	binary.BigEndian.PutUint16(buffer[6:8], hdr.op_code)

	// src_mac (6 bytes)
	copy(buffer[8:14], hdr.src_mac[:])

	// src_ip (4 bytes, already in network byte order)
	binary.BigEndian.PutUint32(buffer[14:18], hdr.src_ip)

	// dst_mac (6 bytes)
	copy(buffer[18:24], hdr.dst_mac[:])

	// dst_ip (4 bytes, already in network byte order)
	binary.BigEndian.PutUint32(buffer[24:28], hdr.dst_ip)

	return buffer
}

// parses bytes into ARP header
func deserialize_arp_header(buffer []byte) (*arp_hdr_t, error) {
	if len(buffer) < ARP_HDR_SIZE {
		return nil, fmt.Errorf("buffer too small for ARP header: need %d bytes, got %d",
			ARP_HDR_SIZE, len(buffer))
	}

	hdr := &arp_hdr_t{}

	// hw_type (2 bytes, big-endian)
	hdr.hw_type = binary.BigEndian.Uint16(buffer[0:2])

	// proto_type (2 bytes, big-endian)
	hdr.proto_type = binary.BigEndian.Uint16(buffer[2:4])

	// hw_addr_len (1 byte)
	hdr.hw_addr_len = buffer[4]

	// proto_addr_len (1 byte)
	hdr.proto_addr_len = buffer[5]

	// op_code (2 bytes, big-endian)
	hdr.op_code = binary.BigEndian.Uint16(buffer[6:8])

	// src_mac (6 bytes)
	copy(hdr.src_mac[:], buffer[8:14])

	// src_ip (4 bytes, network byte order)
	hdr.src_ip = binary.BigEndian.Uint32(buffer[14:18])

	// dst_mac (6 bytes)
	copy(hdr.dst_mac[:], buffer[18:24])

	// dst_ip (4 bytes, network byte order)
	hdr.dst_ip = binary.BigEndian.Uint32(buffer[24:28])

	return hdr, nil
}

// arp_pending_entry represents a packet waiting for ARP resolution
type arp_pending_entry struct {
	pkt              []byte                               // Complete packet (including Ethernet header)
	pkt_size         int                                  // Packet size
	callback         func(*Node, *Interface, []byte, int) // Callback to process packet after ARP resolves
	failure_callback func()                               // Notifies the packet owner when resolution times out
	next             *arp_pending_entry                   // Next pending entry
}

// arp_entry represents a single ARP table entry
type arp_entry struct {
	ip_addr      IpAddr             // Key: IP address (4 bytes)
	mac_addr     MacAddr            // Resolved MAC address (6 bytes)
	oif_name     [IF_NAME_SIZE]byte // Outgoing interface name
	is_sane      bool               // Entry validity flag (false = complete, true = pending ARP resolution)
	created_at   time.Time          // When the entry was created
	updated_at   time.Time          // Last time entry was updated
	pending_list *arp_pending_entry // List of packets waiting for ARP resolution
	retry        func()             // Re-sends the request without holding the table lock
	requests     int                // Number of requests sent, including the initial request
	next         *arp_entry         // Next entry in linked list (simulating glthread)
}

// arp_table represents the ARP table for a node
type arp_table struct {
	head  *arp_entry   // Head of linked list
	mutex sync.RWMutex // Mutex for thread-safe access
}

func arpTableReference(result, ip, mac, interfaceName string, pending bool) *EventTableReference {
	return &EventTableReference{
		Kind:   "arp",
		Result: result,
		Query:  map[string]string{"ip": ip},
		Entry: map[string]string{
			"ip":        ip,
			"mac":       mac,
			"interface": interfaceName,
			"pending":   fmt.Sprintf("%t", pending),
		},
	}
}

func arpTableMissReference(ip string) *EventTableReference {
	return &EventTableReference{
		Kind:   "arp",
		Result: "miss",
		Query:  map[string]string{"ip": ip},
	}
}

func arpTableReferenceFromTable(table *arp_table, ip, result string) *EventTableReference {
	var address IpAddr
	if table == nil || !set_ip_addr(&address, ip) {
		return arpTableMissReference(ip)
	}
	table.mutex.RLock()
	defer table.mutex.RUnlock()
	for current := table.head; current != nil; current = current.next {
		if current.ip_addr == address {
			return arpTableReference(
				result,
				ip,
				current.mac_addr.String(),
				strings.TrimRight(string(current.oif_name[:]), "\x00"),
				current.is_sane,
			)
		}
	}
	return arpTableMissReference(ip)
}

// ARP entry timeout duration (60 seconds)
const ARP_ENTRY_TIMEOUT = 60 * time.Second

// ARP cleanup interval (30 seconds)
const ARP_CLEANUP_INTERVAL = 30 * time.Second

// Incomplete entries have a much shorter lifetime than resolved cache entries.
// Send one request immediately, retry twice, and then fail the queued packets.
const (
	ARP_REQUEST_RETRY_INTERVAL = time.Second
	ARP_REQUEST_MAX_ATTEMPTS   = 3
	ARP_PENDING_TIMEOUT        = time.Duration(ARP_REQUEST_MAX_ATTEMPTS) * ARP_REQUEST_RETRY_INTERVAL
	ARP_MAINTENANCE_INTERVAL   = 100 * time.Millisecond
)

// initializes the ARP table
func init_arp_table(table *arp_table) {
	if table == nil {
		return
	}
	table.head = nil
	// mutex is automatically initialized
}

// adds a new entry to the ARP table
// Returns true on success, false if entry already exists or on error
func arp_table_add_entry(table *arp_table, ip_addr *IpAddr, mac_addr *MacAddr, oif_name string) bool {
	if table == nil || ip_addr == nil || mac_addr == nil {
		return false
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	// Check if entry already exists
	for current := table.head; current != nil; current = current.next {
		if current.ip_addr == *ip_addr {
			// Entry exists, don't add duplicate
			return false
		}
	}

	// Create new entry
	now := time.Now()
	entry := &arp_entry{
		ip_addr:    *ip_addr,
		mac_addr:   *mac_addr,
		is_sane:    false,
		created_at: now,
		updated_at: now,
		next:       table.head, // Insert at head
	}

	// Copy interface name
	copy(entry.oif_name[:], []byte(oif_name))

	// Insert at head of list
	table.head = entry

	return true
}

// looks up an IP address in the ARP table
// Returns pointer to MAC address if found, nil otherwise
func arp_table_lookup(table *arp_table, ip_addr *IpAddr) *MacAddr {
	if table == nil || ip_addr == nil {
		return nil
	}

	table.mutex.RLock()
	defer table.mutex.RUnlock()

	// Search through linked list
	// Only return MAC if entry is complete (is_sane = false means complete)
	for current := table.head; current != nil; current = current.next {
		if current.ip_addr == *ip_addr && !current.is_sane {
			mac_addr := current.mac_addr
			return &mac_addr
		}
	}

	return nil
}

// pdates an existing ARP table entry
// Returns true on success, false if entry not found
func arp_table_update_entry(table *arp_table, ip_addr *IpAddr, mac_addr *MacAddr, oif_name string) bool {
	if table == nil || ip_addr == nil || mac_addr == nil {
		return false
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	// Find the entry
	for current := table.head; current != nil; current = current.next {
		if current.ip_addr == *ip_addr {
			// Update entry
			current.mac_addr = *mac_addr
			copy(current.oif_name[:], []byte(oif_name))
			current.is_sane = false
			current.updated_at = time.Now()
			return true
		}
	}

	return false
}

// deletes an entry from the ARP table
// Returns true on success, false if entry not found
func arp_table_delete_entry(table *arp_table, ip_addr *IpAddr) bool {
	if table == nil || ip_addr == nil {
		return false
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	// Handle deletion from head
	if table.head != nil && table.head.ip_addr == *ip_addr {
		table.head = table.head.next
		return true
	}

	// Search and delete from middle/end
	for current := table.head; current != nil && current.next != nil; current = current.next {
		if current.next.ip_addr == *ip_addr {
			current.next = current.next.next
			return true
		}
	}

	return false
}

// clears all entries from the ARP table
func arp_table_clear(table *arp_table) {
	if table == nil {
		return
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	table.head = nil
}

// removes expired complete entries from the ARP table. Incomplete entries are
// owned by maintain_arp_pending_entries, which retries them and notifies every
// queued packet before removing them.
// Returns the number of entries removed
func arp_table_cleanup_expired(table *arp_table) int {
	if table == nil {
		return 0
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	now := time.Now()
	removed := 0

	for link := &table.head; *link != nil; {
		entry := *link
		if !entry.is_sane && now.Sub(entry.updated_at) > ARP_ENTRY_TIMEOUT {
			LogDebug("ARP: Removing expired entry for %s (age: %v)",
				entry.ip_addr.String(), now.Sub(entry.updated_at))
			*link = entry.next
			entry.next = nil
			removed++
			continue
		}
		link = &entry.next
	}

	if removed > 0 {
		LogInfo("ARP: Cleaned up %d expired entries", removed)
	}

	return removed
}

// arp_table_resolve_or_queue atomically looks up a complete binding or queues a
// packet on an incomplete one. Keeping all three operations under the table
// lock prevents both lookup/create and resolve/queue races.
//
// requestNeeded is true only for the caller that created the incomplete entry;
// that caller sends the initial request after the lock has been released.
func arp_table_resolve_or_queue(
	table *arp_table,
	ip_addr *IpAddr,
	oif_name string,
	pkt []byte,
	pkt_size int,
	callback func(*Node, *Interface, []byte, int),
	failure_callback func(),
	retry func(),
) (mac *MacAddr, requestNeeded bool, queued bool) {
	if table == nil || ip_addr == nil || pkt_size <= 0 || pkt_size > len(pkt) {
		return nil, false, false
	}

	pkt_copy := append([]byte(nil), pkt[:pkt_size]...)
	now := time.Now()
	pending := &arp_pending_entry{
		pkt:              pkt_copy,
		pkt_size:         pkt_size,
		callback:         callback,
		failure_callback: failure_callback,
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	for entry := table.head; entry != nil; entry = entry.next {
		if entry.ip_addr != *ip_addr {
			continue
		}
		if !entry.is_sane {
			resolved := entry.mac_addr
			return &resolved, false, false
		}

		pending.next = entry.pending_list
		entry.pending_list = pending
		if entry.retry == nil {
			entry.retry = retry
		}
		LogDebug("ARP: Added pending packet to queue for IP %s (%d bytes)",
			entry.ip_addr.String(), pkt_size)
		return nil, false, true
	}

	entry := &arp_entry{
		ip_addr:      *ip_addr,
		is_sane:      true,
		created_at:   now,
		updated_at:   now,
		pending_list: pending,
		retry:        retry,
		requests:     1, // The caller sends the initial request on return.
		next:         table.head,
	}
	copy(entry.oif_name[:], []byte(oif_name))
	table.head = entry

	LogDebug("ARP: Created pending entry and queued packet for %s (%d bytes)",
		ip_addr.String(), pkt_size)
	return nil, true, true
}

// process_arp_pending_entry processes a single pending packet after ARP is resolved
func process_arp_pending_entry(node *Node, oif *Interface, arp_entry *arp_entry, pending *arp_pending_entry) {
	if node == nil || oif == nil || arp_entry == nil || pending == nil {
		return
	}

	LogInfo("ARP: Processing pending packet for %s (MAC: %s) - %d bytes",
		arp_entry.ip_addr.String(), arp_entry.mac_addr.String(), pending.pkt_size)

	// Call the callback to send the packet
	if pending.callback != nil {
		pending.callback(node, oif, pending.pkt, pending.pkt_size)
	}
}

type arp_retry_action struct {
	ip       string
	oif_name string
	attempt  int
	retry    func()
}

type arp_failure_action struct {
	ip            string
	oif_name      string
	attempts      int
	pending_count int
	callbacks     []func()
}

// collect_arp_pending_actions advances the state machine while holding the
// table lock, but returns all callbacks for execution after unlocking. ARP
// delivery is synchronous for the in-memory transport, so calling a retry
// while locked would otherwise deadlock when its reply updates this table.
func collect_arp_pending_actions(table *arp_table, now time.Time) ([]arp_retry_action, []arp_failure_action) {
	if table == nil {
		return nil, nil
	}

	table.mutex.Lock()
	defer table.mutex.Unlock()

	var retries []arp_retry_action
	var failures []arp_failure_action
	for link := &table.head; *link != nil; {
		entry := *link
		if !entry.is_sane {
			link = &entry.next
			continue
		}

		age := now.Sub(entry.created_at)
		if age >= ARP_PENDING_TIMEOUT {
			failure := arp_failure_action{
				ip:       entry.ip_addr.String(),
				oif_name: strings.TrimRight(string(entry.oif_name[:]), "\x00"),
				attempts: entry.requests,
			}
			for pending := entry.pending_list; pending != nil; pending = pending.next {
				failure.pending_count++
				if pending.failure_callback != nil {
					failure.callbacks = append(failure.callbacks, pending.failure_callback)
				}
			}
			failures = append(failures, failure)
			*link = entry.next
			entry.next = nil
			entry.pending_list = nil
			entry.retry = nil
			continue
		}

		if entry.requests < ARP_REQUEST_MAX_ATTEMPTS && now.Sub(entry.updated_at) >= ARP_REQUEST_RETRY_INTERVAL {
			entry.requests++
			entry.updated_at = now
			retries = append(retries, arp_retry_action{
				ip:       entry.ip_addr.String(),
				oif_name: strings.TrimRight(string(entry.oif_name[:]), "\x00"),
				attempt:  entry.requests,
				retry:    entry.retry,
			})
		}
		link = &entry.next
	}

	return retries, failures
}

func maintain_arp_pending_entries(node *Node, now time.Time) int {
	if node == nil {
		return 0
	}

	retries, failures := collect_arp_pending_actions(&node.node_nw_prop.arp_table, now)
	for _, action := range retries {
		LogInfo("ARP: Retrying request for %s (attempt %d/%d)",
			action.ip, action.attempt, ARP_REQUEST_MAX_ATTEMPTS)
		emitInterfaceEventWithTable(node, get_node_if_by_name(node, action.oif_name), "ARP", "arp_request_retried", map[string]string{
			"attempt":     fmt.Sprintf("%d", action.attempt),
			"interface":   action.oif_name,
			"maxAttempts": fmt.Sprintf("%d", ARP_REQUEST_MAX_ATTEMPTS),
			"targetIp":    action.ip,
		}, arpTableReferenceFromTable(&node.node_nw_prop.arp_table, action.ip, "pending"))
		if action.retry != nil {
			action.retry()
		}
	}

	for _, action := range failures {
		LogWarn("ARP: Resolution for %s timed out after %d request(s); dropping %d queued packet(s)",
			action.ip, action.attempts, action.pending_count)
		emitInterfaceEventWithTable(node, get_node_if_by_name(node, action.oif_name), "ARP", "arp_resolution_failed", map[string]string{
			"attempts":       fmt.Sprintf("%d", action.attempts),
			"interface":      action.oif_name,
			"pendingPackets": fmt.Sprintf("%d", action.pending_count),
			"reason":         "resolution_timeout",
			"targetIp":       action.ip,
		}, arpTableMissReference(action.ip))
		for _, callback := range action.callbacks {
			callback()
		}
	}

	return len(failures)
}

// starts ARP table cleanup goroutine for a node
func start_arp_table_cleanup(node *Node) {
	if node == nil {
		return
	}

	stop_ch := make(chan bool)
	node.arp_cleanup_stop_ch = stop_ch
	node.background_wait_group.Add(1)

	node_name := get_node_name(node)
	LogInfo("ARP: Starting cleanup goroutine for node %s (interval: %v, timeout: %v)",
		node_name, ARP_CLEANUP_INTERVAL, ARP_ENTRY_TIMEOUT)

	// Start cleanup goroutine
	go func(stop_ch <-chan bool) {
		defer node.background_wait_group.Done()
		maintenance_ticker := time.NewTicker(ARP_MAINTENANCE_INTERVAL)
		cleanup_ticker := time.NewTicker(ARP_CLEANUP_INTERVAL)
		defer maintenance_ticker.Stop()
		defer cleanup_ticker.Stop()

		for {
			select {
			case <-stop_ch:
				LogInfo("ARP: Stopping cleanup goroutine for node %s", node_name)
				return
			case <-maintenance_ticker.C:
				maintain_arp_pending_entries(node, time.Now())
			case <-cleanup_ticker.C:
				removed := arp_table_cleanup_expired(&node.node_nw_prop.arp_table)
				if removed > 0 {
					LogDebug("ARP: Cleanup removed %d entries for node %s", removed, node_name)
				}
			}
		}
	}(stop_ch)
}

// stops ARP table cleanup goroutine for a node
func stop_arp_table_cleanup(node *Node) {
	if node == nil || node.arp_cleanup_stop_ch == nil {
		return
	}

	stop_ch := node.arp_cleanup_stop_ch
	node.arp_cleanup_stop_ch = nil
	close(stop_ch)
}

// displays all entries in the ARP table
func arp_table_dump(table *arp_table, node_name string) {
	if table == nil {
		return
	}

	table.mutex.RLock()
	defer table.mutex.RUnlock()

	fmt.Printf("\n=== ARP Table for Node %s ===\n", node_name)
	fmt.Printf("%-15s %-17s %-16s %s\n", "IP Address", "MAC Address", "Interface", "Status")
	fmt.Printf("%-15s %-17s %-16s %s\n", "----------", "-----------", "---------", "------")

	count := 0
	for current := table.head; current != nil; current = current.next {
		ip_str := current.ip_addr.String()
		mac_str := current.mac_addr.String()

		// Extract interface name
		oif_name := string(current.oif_name[:])
		for i, b := range oif_name {
			if b == 0 {
				oif_name = oif_name[:i]
				break
			}
		}

		status := "Valid"
		if current.is_sane {
			status = "Pending"
		}

		fmt.Printf("%-15s %-17s %-16s %s\n", ip_str, mac_str, oif_name, status)
		count++
	}

	if count == 0 {
		fmt.Printf("(empty)\n")
	}
	fmt.Printf("Total entries: %d\n\n", count)
}

// sends an ARP broadcast request to resolve IP address
// Args:
//   - node: the node sending the ARP request
//   - oif: output interface (optional, can be nil - will find matching subnet interface)
//   - ip_addr: the IP address to resolve
//
// Returns: 0 on success, -1 on failure
func send_arp_broadcast_request(node *Node, oif *Interface, ip_addr string) int {
	if node == nil {
		LogError("ARP: node is nil")
		return -1
	}

	// Find output interface if not provided
	var out_intf *Interface
	if oif == nil {
		out_intf = node_get_matching_subnet_interface(node, ip_addr)
		if out_intf == nil {
			LogError("ARP: could not find matching subnet interface for IP %s", ip_addr)
			return -1
		}
	} else {
		out_intf = oif
	}

	// Check if we're trying to resolve our own IP
	if out_intf.IsIPConfigured() {
		intf_ip := out_intf.GetIP()
		var target_ip IpAddr
		if set_ip_addr(&target_ip, ip_addr) {
			if ip_addr_equal(intf_ip, &target_ip) {
				LogError("ARP: cannot resolve own IP address %s", ip_addr)
				return -1
			}
		}
	}

	// Build ARP payload
	arp_hdr := &arp_hdr_t{
		hw_type:        ARP_HW_TYPE_ETHERNET,
		proto_type:     ARP_PROTO_TYPE_IP,
		hw_addr_len:    ARP_HW_ADDR_LEN,
		proto_addr_len: ARP_PROTO_ADDR_LEN,
		op_code:        ARP_OP_REQUEST,
	}

	// Source MAC (our MAC)
	src_mac := out_intf.GetMac()
	arp_hdr.src_mac = *src_mac

	// Source IP (our IP) - store in host byte order (serializer will convert)
	if out_intf.IsIPConfigured() {
		src_ip := out_intf.GetIP()
		var src_ip_uint32 uint32
		if ip_addr_str_to_int32(src_ip.String(), &src_ip_uint32) {
			arp_hdr.src_ip = src_ip_uint32
		}
	}

	// Destination MAC (00:00:00:00:00:00 for ARP request)
	arp_hdr.dst_mac = MacAddr{0, 0, 0, 0, 0, 0}

	// Destination IP (target IP we want to resolve) - store in host byte order
	var dst_ip_uint32 uint32
	if !ip_addr_str_to_int32(ip_addr, &dst_ip_uint32) {
		LogError("ARP: invalid IP address %s", ip_addr)
		return -1
	}
	arp_hdr.dst_ip = dst_ip_uint32

	// Serialize ARP header to bytes
	arp_payload := serialize_arp_header(arp_hdr)

	// Tag ARP payload with Ethernet header
	frame := tag_packet_with_ethernet_hdr(arp_payload, ARP_HDR_SIZE)
	if frame == nil {
		LogError("ARP: failed to tag packet with Ethernet header")
		return -1
	}

	// Set Ethernet header fields
	var broadcast_mac MacAddr
	layer2_fill_with_broadcast_mac(broadcast_mac[:])
	frame.header.dst_mac = broadcast_mac
	frame.header.src_mac = *src_mac
	frame.header.ethertype = ETHERTYPE_ARP

	// Serialize frame to bytes
	frame_bytes := serialize_ethernet_frame(frame)

	emitInterfaceEventWithTable(node, out_intf, "ARP", "arp_request_started", map[string]string{
		"senderIp":  IPUint32ToString(arp_hdr.src_ip),
		"senderMac": src_mac.String(),
		"targetIp":  ip_addr,
	}, arpTableReferenceFromTable(&node.node_nw_prop.arp_table, ip_addr, "pending"))
	err := send_frame(frame_bytes, len(frame_bytes), out_intf)
	if err != nil {
		LogError("ARP: failed to send broadcast request: %v", err)
		return -1
	}

	return 0
}

// sends an ARP reply in response to an ARP request packet
// Args:
//   - pkt_buffer: the incoming packet buffer containing Ethernet + ARP headers
//   - oif: output interface to send the reply on
//   - reply_ip: the IP address to use in the ARP reply (e.g., VLAN interface IP or physical interface IP)
func send_arp_reply_msg(pkt_buffer []byte, oif *Interface, reply_ip *IpAddr) {
	if pkt_buffer == nil || oif == nil || reply_ip == nil {
		LogError("ARP: nil parameter in send_arp_reply_msg")
		return
	}

	// Parse incoming ARP header (after Ethernet header) using deserializer
	arp_hdr_in, err := deserialize_arp_header(pkt_buffer[ETHERNET_HDR_SIZE:])
	if err != nil {
		LogError("ARP: error parsing incoming ARP header: %v", err)
		return
	}

	arp_hdr_reply := &arp_hdr_t{
		hw_type:        ARP_HW_TYPE_ETHERNET,
		proto_type:     ARP_PROTO_TYPE_IP,
		hw_addr_len:    ARP_HW_ADDR_LEN,
		proto_addr_len: ARP_PROTO_ADDR_LEN,
		op_code:        ARP_OP_REPLY,
	}

	// Source MAC: our interface MAC
	src_mac := oif.GetMac()
	arp_hdr_reply.src_mac = *src_mac

	// Source IP: use the provided reply IP (could be physical interface IP or VLAN interface IP)
	var src_ip_uint32 uint32
	if ip_addr_str_to_int32(reply_ip.String(), &src_ip_uint32) {
		arp_hdr_reply.src_ip = src_ip_uint32
	}

	// Destination MAC: source MAC from incoming request
	arp_hdr_reply.dst_mac = arp_hdr_in.src_mac

	// Destination IP: source IP from incoming request (already deserialized to host byte order)
	arp_hdr_reply.dst_ip = arp_hdr_in.src_ip

	// Serialize ARP reply to bytes
	arp_reply_payload := serialize_arp_header(arp_hdr_reply)

	// Tag ARP reply payload with Ethernet header
	frame := tag_packet_with_ethernet_hdr(arp_reply_payload, ARP_HDR_SIZE)
	if frame == nil {
		LogError("ARP: failed to tag packet with Ethernet header")
		return
	}

	// Set Ethernet header fields
	frame.header.dst_mac = arp_hdr_in.src_mac
	frame.header.src_mac = *src_mac
	frame.header.ethertype = ETHERTYPE_ARP

	// Serialize frame to bytes
	frame_bytes := serialize_ethernet_frame(frame)
	for vlanID, vlanIntf := range oif.att_node.GetVlanInterfacesSnapshot() {
		if !ip_addr_equal(&vlanIntf.ip_addr, reply_ip) {
			continue
		}
		frame_bytes, err = prepare_frame_for_interface(oif, frame_bytes, vlanID)
		if err != nil {
			LogError("ARP: error preparing VLAN %d reply: %v", vlanID, err)
			return
		}
		break
	}

	// Send the reply
	// Convert target IP to string for display
	var reply_to_ip [16]byte
	ip_addr_int32_to_str(arp_hdr_in.src_ip, reply_to_ip[:])
	reply_ip_str := string(reply_to_ip[:])
	if idx := strings.IndexByte(reply_ip_str, 0); idx != -1 {
		reply_ip_str = reply_ip_str[:idx]
	}
	emitInterfaceEvent(oif.att_node, oif, "ARP", "arp_reply_created", map[string]string{
		"senderIp":   reply_ip.String(),
		"senderMac":  src_mac.String(),
		"receiverIp": reply_ip_str,
	})
	send_err := send_frame(frame_bytes, len(frame_bytes), oif)
	if send_err != nil {
		LogError("ARP: error sending reply: %v", send_err)
	}
}

// arp_learn_binding records a sender's IP to MAC binding and releases any
// packets that were waiting on it.
//
// RFC 826 calls this the merge step: whenever an ARP packet arrives, a node
// updates an existing entry for the sender, and installs a new one when the
// packet was addressed to it. Both requests and replies carry the sender's
// binding, so learning from a request saves the round trip that would
// otherwise be needed before this node can answer.
//
// cacheState labels the event emitted for the UI, and existingOnly restricts
// the update to bindings already in the table, which is the behaviour RFC 826
// requires for a packet that is not addressed to us.
func arp_learn_binding(node *Node, iif *Interface, ip_addr *IpAddr, mac_addr *MacAddr, existingOnly bool) {
	if node == nil || iif == nil || ip_addr == nil || mac_addr == nil {
		return
	}

	iif_name := get_interface_name(iif)
	table := &node.node_nw_prop.arp_table
	table.mutex.Lock()

	var entry *arp_entry
	for current := table.head; current != nil; current = current.next {
		if current.ip_addr == *ip_addr {
			entry = current
			break
		}
	}

	if entry == nil {
		if existingOnly {
			// The packet was not addressed to us and we hold no entry for the
			// sender, so there is nothing to merge.
			table.mutex.Unlock()
			return
		}
		now := time.Now()
		entry = &arp_entry{
			ip_addr:    *ip_addr,
			mac_addr:   *mac_addr,
			is_sane:    false,
			created_at: now,
			updated_at: now,
			next:       table.head,
		}
		copy(entry.oif_name[:], []byte(iif_name))
		table.head = entry
		table.mutex.Unlock()

		emitInterfaceEventWithTable(node, iif, "ARP", "arp_resolved", map[string]string{
			"ip":             ip_addr.String(),
			"mac":            mac_addr.String(),
			"cacheState":     "learned",
			"pendingPackets": "0",
		}, arpTableReference("learned", ip_addr.String(), mac_addr.String(), iif_name, false))
		return
	}

	entry.mac_addr = *mac_addr
	entry.is_sane = false // The binding is now complete
	entry.updated_at = time.Now()
	copy(entry.oif_name[:], []byte(iif_name))

	// Take the queued packets before unlocking so they are sent exactly once.
	pending_list := entry.pending_list
	entry.pending_list = nil
	entry.retry = nil
	table.mutex.Unlock()

	pending_count := 0
	for pending := pending_list; pending != nil; pending = pending.next {
		pending_count++
	}
	emitInterfaceEventWithTable(node, iif, "ARP", "arp_resolved", map[string]string{
		"ip":             ip_addr.String(),
		"mac":            mac_addr.String(),
		"cacheState":     "updated",
		"pendingPackets": fmt.Sprintf("%d", pending_count),
	}, arpTableReference("updated", ip_addr.String(), mac_addr.String(), iif_name, false))

	for pending := pending_list; pending != nil; pending = pending.next {
		process_arp_pending_entry(node, iif, entry, pending)
	}
	if pending_count > 0 {
		emitInterfaceEventWithTable(node, iif, "ARP", "pending_packets_released", map[string]string{
			"targetIp": ip_addr.String(),
			"count":    fmt.Sprintf("%d", pending_count),
		}, arpTableReference("hit", ip_addr.String(), mac_addr.String(), iif_name, false))
	}
}

// processes an incoming ARP reply message
// Args:
//   - node: the node receiving the ARP reply
//   - iif: incoming interface
//   - pkt_buffer: the packet buffer containing Ethernet + ARP headers
func process_arp_reply_msg(node *Node, iif *Interface, pkt_buffer []byte) {
	if node == nil || iif == nil || pkt_buffer == nil {
		LogError("ARP: nil parameter in process_arp_reply_msg")
		return
	}

	LogDebug("ARP: Reply received on interface %s of node %s", get_interface_name(iif), get_node_name(node))

	// Parse ARP header (after Ethernet header) using deserializer
	arp_hdr, err := deserialize_arp_header(pkt_buffer[ETHERNET_HDR_SIZE:])
	if err != nil {
		LogError("ARP: error parsing ARP header: %v", err)
		return
	}

	// Source IP is already in host byte order from deserializer
	src_ip_uint32 := arp_hdr.src_ip

	// Convert IP to string
	var ip_str [16]byte
	ip_addr_int32_to_str(src_ip_uint32, ip_str[:])

	// Update ARP table with the resolved MAC address
	// Create IP address from the ARP reply
	var ip_addr IpAddr
	// Trim null bytes from the IP string
	ip_str_trimmed := string(ip_str[:])
	if idx := strings.IndexByte(ip_str_trimmed, 0); idx != -1 {
		ip_str_trimmed = ip_str_trimmed[:idx]
	}
	LogInfo("ARP: Processing reply - IP string: '%s', MAC: %s", ip_str_trimmed, arp_hdr.src_mac.String())
	if !set_ip_addr(&ip_addr, ip_str_trimmed) {
		LogError("ARP: Failed to parse IP address from reply: '%s'", string(ip_str[:]))
		return
	}

	LogInfo("ARP: IP address parsed successfully: %s", ip_addr.String())
	arp_learn_binding(node, iif, &ip_addr, &arp_hdr.src_mac, false)
}

//	processes an incoming ARP broadcast request
//
// Args:
//   - node: the node receiving the ARP request
//   - iif: incoming interface
//   - pkt_buffer: the packet buffer containing Ethernet + ARP headers
func process_arp_broadcast_request(node *Node, iif *Interface, pkt_buffer []byte) {
	if node == nil || iif == nil || pkt_buffer == nil {
		LogError("ARP: nil parameter in process_arp_broadcast_request")
		return
	}

	LogDebug("ARP: Broadcast request received on interface %s of node %s", get_interface_name(iif), get_node_name(node))

	// Parse ARP header (after Ethernet header) using deserializer
	arp_hdr, err := deserialize_arp_header(pkt_buffer[ETHERNET_HDR_SIZE:])
	if err != nil {
		LogError("ARP: error parsing ARP header: %v", err)
		return
	}

	// Destination IP is already in host byte order from deserializer
	arp_dst_ip := arp_hdr.dst_ip

	// Convert IP to string
	var ip_addr_str [16]byte
	if !ip_addr_int32_to_str(arp_dst_ip, ip_addr_str[:]) {
		LogError("ARP: could not convert IP address")
		return
	}

	// Convert target IP string for comparison
	ip_str_clean := string(ip_addr_str[:])
	// Find the null terminator
	for i, b := range ip_addr_str {
		if b == 0 {
			ip_str_clean = string(ip_addr_str[:i])
			break
		}
	}

	var target_ip IpAddr
	if !set_ip_addr(&target_ip, ip_str_clean) {
		LogError("ARP: could not parse target IP address")
		return
	}

	// Check if the destination IP matches our physical interface IP
	shouldReply := false
	if iif.IsIPConfigured() {
		iif_ip := iif.GetIP()
		if ip_addr_equal(iif_ip, &target_ip) {
			shouldReply = true
			LogDebug("ARP: Target IP %s matches interface IP", target_ip.String())
		}
	}

	// Also check if target IP matches any VLAN interface (SVI) on this node
	if !shouldReply {
		for _, vlan_intf := range node.GetVlanInterfacesSnapshot() {
			if ip_addr_equal(&vlan_intf.ip_addr, &target_ip) {
				shouldReply = true
				LogDebug("ARP: Target IP %s matches VLAN interface", target_ip.String())
				break
			}
		}
	}

	// RFC 826: merge the sender's binding before deciding whether to answer.
	// The requester's address pair is right there in the packet, so learning it
	// now means this node can reply to the sender later without ARPing for it.
	// A request that is not for us may only refresh an entry we already hold.
	var sender_ip IpAddr
	sender_ip_str := IPUint32ToString(arp_hdr.src_ip)
	if arp_hdr.src_ip != 0 && set_ip_addr(&sender_ip, sender_ip_str) {
		arp_learn_binding(node, iif, &sender_ip, &arp_hdr.src_mac, !shouldReply)
	}

	if !shouldReply {
		LogDebug("ARP: Broadcast request dropped, Dst IP %s did not match any interface",
			target_ip.String())
		return
	}

	emitInterfaceEvent(node, iif, "ARP", "arp_request_received", map[string]string{
		"senderIp":  IPUint32ToString(arp_hdr.src_ip),
		"senderMac": arp_hdr.src_mac.String(),
		"targetIp":  target_ip.String(),
	})
	send_arp_reply_msg(pkt_buffer, iif, &target_ip)
}
