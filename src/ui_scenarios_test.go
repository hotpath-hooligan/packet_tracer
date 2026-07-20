package main

import (
	"os"
	"testing"
)

func TestUIScenariosHaveExpectedPingOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		source      string
		destination string
		wantReply   bool
		rootBridge  string
		blocked     int
		failFrom    string
		failFromIf  string
		failTo      string
		failToIf    string
		advertisers int
	}{
		{
			name:        "switched LAN succeeds",
			filename:    "../ui/scenarios/switched-lan.yaml",
			source:      "Client-A",
			destination: "Client-B",
			wantReply:   true,
		},
		{
			name:        "multi-switch VLAN trunking succeeds",
			filename:    "../ui/scenarios/multi-switch-vlan-trunking.yaml",
			source:      "Sales-A",
			destination: "Sales-B",
			wantReply:   true,
		},
		{
			name:        "access VLAN mismatch fails",
			filename:    "../ui/scenarios/access-vlan-mismatch.yaml",
			source:      "User-PC",
			destination: "File-SRV",
			wantReply:   false,
		},
		{
			name:        "trunk allowed VLAN mismatch fails",
			filename:    "../ui/scenarios/trunk-allowed-vlan-mismatch.yaml",
			source:      "Users-A",
			destination: "Users-B",
			wantReply:   false,
		},
		{
			name:        "inter-VLAN routing succeeds",
			filename:    "../ui/scenarios/inter-vlan-routing.yaml",
			source:      "Sales-PC",
			destination: "Engineering-PC",
			wantReply:   true,
		},
		{
			name:        "static routing between sites succeeds",
			filename:    "../ui/scenarios/static-routing-between-sites.yaml",
			source:      "Branch-PC",
			destination: "App-SRV",
			wantReply:   true,
		},
		{
			name:        "default gateway misconfiguration fails",
			filename:    "../ui/scenarios/default-gateway-misconfiguration.yaml",
			source:      "User-PC",
			destination: "App-SRV",
			wantReply:   false,
		},
		{
			name:        "missing return route fails",
			filename:    "../ui/scenarios/missing-return-route.yaml",
			source:      "Client-PC",
			destination: "Server-PC",
			wantReply:   false,
		},
		{
			name:        "longest-prefix route selection succeeds",
			filename:    "../ui/scenarios/longest-prefix-route-selection.yaml",
			source:      "User-PC",
			destination: "App-SRV",
			wantReply:   true,
		},
		{
			name:        "STP redundant topology succeeds",
			filename:    "../ui/scenarios/stp-loop.yaml",
			source:      "H1",
			destination: "H2",
			wantReply:   true,
			rootBridge:  "SW1",
			blocked:     1,
		},
		{
			name:        "STP uplink failover succeeds",
			filename:    "../ui/scenarios/stp-uplink-failover.yaml",
			source:      "User-PC",
			destination: "File-SRV",
			wantReply:   true,
			rootBridge:  "Dist-SW1",
			blocked:     1,
			failFrom:    "Access-SW1",
			failFromIf:  "to-d1",
			failTo:      "Dist-SW1",
			failToIf:    "to-a1",
		},
		{
			name:        "LLDP neighbor topology has data-plane connectivity",
			filename:    "../ui/scenarios/lldp-neighbor-discovery.yaml",
			source:      "Admin-PC",
			destination: "File-SRV",
			wantReply:   true,
			advertisers: 4,
		},
		{
			name:        "campus VLAN and routing succeeds",
			filename:    "../ui/scenarios/campus-vlan-routing.yaml",
			source:      "User-A",
			destination: "App-SRV",
			wantReply:   true,
			rootBridge:  "Core-SW",
			blocked:     1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yamlData, err := os.ReadFile(test.filename)
			if err != nil {
				t.Fatalf("failed to read scenario: %v", err)
			}

			simulator := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, nil)
			topology, err := simulator.LoadTopology(string(yamlData))
			if err != nil {
				t.Fatalf("failed to load scenario: %v", err)
			}
			defer cleanup_graph_resources(simulator.graph)
			if topology.Description == "" || topology.Summary == "" {
				t.Fatal("scenario must define description and summary render data")
			}
			if topology.DefaultSource != test.source || topology.DefaultDestination != test.destination {
				t.Fatalf("YAML default endpoints = %q -> %q, want %q -> %q", topology.DefaultSource, topology.DefaultDestination, test.source, test.destination)
			}
			if topology.Canvas == nil {
				t.Fatal("scenario must define a render canvas")
			}
			for _, node := range topology.Nodes {
				if node.Position == nil {
					t.Fatalf("scenario node %s must define a render position", node.Name)
				}
			}
			if test.rootBridge != "" {
				blocked := 0
				rootFound := false
				for _, node := range topology.Nodes {
					if node.Name == test.rootBridge {
						rootFound = node.STP.IsRoot
					}
					for _, port := range node.STP.Ports {
						if node.STP.Enabled && port.State != "forwarding" {
							blocked++
						}
					}
				}
				if !rootFound {
					t.Fatalf("%s must be the STP root bridge", test.rootBridge)
				}
				if blocked != test.blocked {
					t.Fatalf("blocked STP ports = %d, want %d", blocked, test.blocked)
				}
			}

			result, err := simulator.Ping(topology.DefaultSource, topology.DefaultDestination)
			if err != nil {
				t.Fatalf("ping failed to run: %v", err)
			}
			if result.ReplyReceived != test.wantReply {
				t.Fatalf("reply received = %t, want %t", result.ReplyReceived, test.wantReply)
			}
			if test.failFrom != "" {
				if _, err := simulator.SetLinkState(test.failFrom, test.failFromIf, test.failTo, test.failToIf, false); err != nil {
					t.Fatalf("failed to bring down failover link: %v", err)
				}
				result, err = simulator.Ping(topology.DefaultSource, topology.DefaultDestination)
				if err != nil {
					t.Fatalf("post-failover ping failed to run: %v", err)
				}
				if !result.ReplyReceived {
					t.Fatal("post-failover ping did not receive a reply")
				}
			}
			if test.advertisers > 0 {
				trace, err := simulator.TraceLLDP()
				if err != nil {
					t.Fatalf("LLDP discovery failed: %v", err)
				}
				if len(trace.Advertisers) != test.advertisers {
					t.Fatalf("LLDP advertisers = %d, want %d", len(trace.Advertisers), test.advertisers)
				}
				if len(trace.Events) == 0 {
					t.Fatal("LLDP discovery captured no events")
				}
			}
		})
	}
}
