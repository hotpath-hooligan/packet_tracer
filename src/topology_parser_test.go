package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTopologyValidationEnforcesDeviceLimit(t *testing.T) {
	config := TopologyConfig{Topology: TopologyInfo{Name: "Device Limit"}}
	for index := 0; index < MAX_NODES_PER_TOPOLOGY; index++ {
		config.Nodes = append(config.Nodes, NodeConfig{Name: fmt.Sprintf("N%d", index)})
	}

	if err := validate_topology_config(&config); err != nil {
		t.Fatalf("expected %d devices to pass validation, got %v", MAX_NODES_PER_TOPOLOGY, err)
	}

	config.Nodes = append(config.Nodes, NodeConfig{Name: "TooMany"})
	if err := validate_topology_config(&config); err == nil {
		t.Fatalf("expected %d devices to fail validation", MAX_NODES_PER_TOPOLOGY+1)
	} else if want := "topology supports at most 100 devices"; !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error to contain %q, got %q", want, err)
	}
}

func TestTopologyValidationRejectsConflictingInterfaceFields(t *testing.T) {
	tests := []struct {
		name          string
		interfaceYAML string
		wantError     string
	}{
		{
			name: "IP with access configuration",
			interfaceYAML: `
        ip: "10.0.0.1"
        mask: 24
        mode: "access"
        vlan: 10`,
			wantError: "interface eth0 on node SW1 cannot be both a routed interface (ip/mask) and a Layer 2 switchport (access/trunk configuration)",
		},
		{
			name: "IP with trunk configuration",
			interfaceYAML: `
        ip: "10.0.0.1"
        mask: 24
        mode: "trunk"
        native_vlan: 1
        allowed_vlans: [10, 20]`,
			wantError: "interface eth0 on node SW1 cannot be both a routed interface (ip/mask) and a Layer 2 switchport (access/trunk configuration)",
		},
		{
			name: "mask without IP",
			interfaceYAML: `
        mask: 24
        mode: "access"`,
			wantError: "interface eth0 on node SW1 configures mask without ip",
		},
		{
			name: "access mode with trunk fields",
			interfaceYAML: `
        mode: "access"
        vlan: 10
        allowed_vlans: [10, 20]`,
			wantError: "interface eth0 on node SW1 cannot use native_vlan or allowed_vlans in access mode",
		},
		{
			name: "trunk mode with access VLAN",
			interfaceYAML: `
        mode: "trunk"
        vlan: 10
        allowed_vlans: [10, 20]`,
			wantError: "interface eth0 on node SW1 cannot use vlan in trunk mode",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yamlData := `
topology:
  name: "Validation Test"
nodes:
  - name: "SW1"
    interfaces:
      - name: "eth0"` + test.interfaceYAML + "\n"

			_, err := load_topology_from_string(yamlData)
			if err == nil {
				t.Fatal("expected topology load to fail")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected error to contain %q, got %q", test.wantError, err)
			}
		})
	}
}

func TestTopologyParserRejectsUnknownFields(t *testing.T) {
	const yamlData = `
topology:
  name: "Validation Test"
nodes:
  - name: "SW1"
    interfaces:
      - name: "eth0"
        mode: "trunk"
        allowd_vlans: [10]
`

	_, err := load_topology_from_string(yamlData)
	if err == nil {
		t.Fatal("expected topology load to fail")
	}
	if !strings.Contains(err.Error(), "field allowd_vlans not found") {
		t.Fatalf("expected unknown field error, got %q", err)
	}
}

func TestTopologyParserRejectsUnknownDeviceType(t *testing.T) {
	const yamlData = `
topology:
  name: "Validation Test"
nodes:
  - name: "R1"
    type: firewall
`

	_, err := load_topology_from_string(yamlData)
	if err == nil {
		t.Fatal("expected topology load to fail")
	}
	if !strings.Contains(err.Error(), `invalid device type "firewall" on node R1`) {
		t.Fatalf("expected invalid device type error, got %q", err)
	}
}

func TestTopologyDeviceTypeResolution(t *testing.T) {
	const yamlData = `
topology:
  name: "Device Types"
nodes:
  - name: "edge"
    type: router
    interfaces:
      - name: "eth0"
        ip: "10.0.1.1"
        mask: 24
      - name: "eth1"
        ip: "10.0.2.1"
        mask: 24
  - name: "R2"
    interfaces:
      - name: "eth0"
        ip: "10.0.2.2"
        mask: 24
  - name: "H1"
    interfaces:
      - name: "eth0"
        ip: "10.0.1.2"
        mask: 24
  - name: "SW1"
    interfaces:
      - name: "access1"
        mode: access
        vlan: 10
`

	graph, err := load_topology_from_bytes_with_transport([]byte(yamlData), NewMemoryTransport())
	if err != nil {
		t.Fatalf("failed to load topology: %v", err)
	}
	defer cleanup_graph_resources(graph)

	want := map[string]DeviceType{
		"edge": DEVICE_TYPE_ROUTER,
		"R2":   DEVICE_TYPE_ROUTER,
		"H1":   DEVICE_TYPE_HOST,
		"SW1":  DEVICE_TYPE_SWITCH,
	}
	for name, deviceType := range want {
		node := findGraphNode(graph, name)
		if node == nil {
			t.Fatalf("node %s was not created", name)
		}
		if node.device_type != deviceType {
			t.Errorf("node %s type = %q, want %q", name, node.device_type, deviceType)
		}
	}
}

func TestTopologyLLDPConfiguration(t *testing.T) {
	const yamlData = `
topology:
  name: "LLDP configuration"
nodes:
  - name: "R1"
    type: router
    lldp: true
    interfaces:
      - name: "eth0"
        ip: "10.0.0.1"
        mask: 24
  - name: "R2"
    type: router
    lldp: true
    interfaces:
      - name: "eth0"
        ip: "10.0.0.2"
        mask: 24
  - name: "disabled"
    lldp: false
  - name: "default"
links:
  - from_node: "R1"
    from_interface: "eth0"
    to_node: "R2"
    to_interface: "eth0"
`

	graph, err := load_topology_from_bytes_with_transport([]byte(yamlData), NewMemoryTransport())
	if err != nil {
		t.Fatalf("failed to load topology: %v", err)
	}
	defer cleanup_graph_resources(graph)

	for _, name := range []string{"R1", "R2"} {
		node := findGraphNode(graph, name)
		if node == nil || node.lldp_state == nil || !node.lldp_state.IsEnabled() {
			t.Fatalf("LLDP was not enabled from YAML on %s", name)
		}
		if neighbors := node.lldp_state.NeighborsSnapshot(); len(neighbors) != 1 {
			t.Fatalf("%s learned %d LLDP neighbors, want 1: %#v", name, len(neighbors), neighbors)
		}
	}

	for _, name := range []string{"disabled", "default"} {
		node := findGraphNode(graph, name)
		if node == nil || node.lldp_state == nil {
			t.Fatalf("node %s has no LLDP state", name)
		}
		if node.lldp_state.IsEnabled() {
			t.Fatalf("LLDP unexpectedly enabled on %s", name)
		}
	}
}

func TestTopologyRenderDataComesFromYAML(t *testing.T) {
	const yamlData = `
topology:
  name: "Rendered topology"
  description: "Description from YAML"
  summary: "Summary from YAML"
  default_source: "H1"
  default_destination: "H2"
  canvas:
    width: 1200
    height: 640
nodes:
  - name: "H1"
    type: host
    position: {x: 160, y: 320}
    interfaces:
      - name: "eth0"
        ip: "10.0.0.1"
        mask: 24
  - name: "H2"
    type: host
    position: {x: 1040, y: 320}
    interfaces:
      - name: "eth0"
        ip: "10.0.0.2"
        mask: 24
links:
  - from_node: "H1"
    from_interface: "eth0"
    to_node: "H2"
    to_interface: "eth0"
`

	simulator := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, nil)
	state, err := simulator.LoadTopology(yamlData)
	if err != nil {
		t.Fatalf("failed to load topology: %v", err)
	}
	defer cleanup_graph_resources(simulator.graph)

	if state.Description != "Description from YAML" || state.Summary != "Summary from YAML" {
		t.Fatalf("render text was not preserved: %#v", state)
	}
	if state.DefaultSource != "H1" || state.DefaultDestination != "H2" {
		t.Fatalf("default endpoints were not preserved: %q -> %q", state.DefaultSource, state.DefaultDestination)
	}
	if state.Canvas == nil || state.Canvas.Width != 1200 || state.Canvas.Height != 640 {
		t.Fatalf("canvas was not preserved: %#v", state.Canvas)
	}
	if len(state.Nodes) != 2 || state.Nodes[0].Position == nil || state.Nodes[0].Position.X != 160 || state.Nodes[0].Position.Y != 320 {
		t.Fatalf("node positions were not preserved: %#v", state.Nodes)
	}
}

func TestTopologyValidationRejectsInvalidRenderData(t *testing.T) {
	tests := []struct {
		name      string
		yamlData  string
		wantError string
	}{
		{
			name: "position without canvas",
			yamlData: `
topology:
  name: "Invalid render data"
nodes:
  - name: "H1"
    position: {x: 10, y: 10}
`,
			wantError: "configures position without topology canvas",
		},
		{
			name: "position outside canvas",
			yamlData: `
topology:
  name: "Invalid render data"
  canvas: {width: 100, height: 100}
nodes:
  - name: "H1"
    position: {x: 101, y: 10}
`,
			wantError: "must be inside the topology canvas",
		},
		{
			name: "unknown default source",
			yamlData: `
topology:
  name: "Invalid render data"
  default_source: "missing"
nodes:
  - name: "H1"
`,
			wantError: "default_source missing not found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := load_topology_from_string(test.yamlData)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want an error containing %q", err, test.wantError)
			}
		})
	}
}

func TestBundledTopologiesPassStrictValidation(t *testing.T) {
	files, err := filepath.Glob("../topologies/*.yaml")
	if err != nil {
		t.Fatalf("failed to list bundled topologies: %v", err)
	}
	files = append(files, "../ui/default-topology.yaml")

	for _, filename := range files {
		t.Run(filepath.Base(filename), func(t *testing.T) {
			data, err := os.ReadFile(filename)
			if err != nil {
				t.Fatalf("failed to read topology: %v", err)
			}
			graph, err := load_topology_from_bytes_with_transport(data, NewMemoryTransport())
			if err != nil {
				t.Fatalf("bundled topology failed validation: %v", err)
			}
			cleanup_graph_resources(graph)
		})
	}
}
