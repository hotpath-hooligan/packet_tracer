//go:build !js

package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/peterh/liner"
	"github.com/spf13/cobra"
)

// Global topology var
var currentTopology *Graph

var rootCmd = &cobra.Command{
	Use:   "tcp-ip-stack",
	Short: "A TCP/IP simulator in Go",
	Run: func(cmd *cobra.Command, args []string) {
		startInteractiveShell()
	},
}

var showCmd = &cobra.Command{
	Use:   "show",
	Short: "Show commands",
}

var loadCmd = &cobra.Command{
	Use:   "load",
	Short: "Load topology from YAML file",
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run commands on nodes",
}

var runNodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Run commands on a specific node",
}

var showNodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Show node information",
}

func findCurrentTopologyNode(nodeName string) *Node {
	if currentTopology == nil {
		return nil
	}
	for _, node := range currentTopology.node_list {
		if get_node_name(node) == nodeName {
			return node
		}
	}
	return nil
}

var showNodeMacCmd = &cobra.Command{
	Use:   "mac [node-name]",
	Short: "Show MAC address table for a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			LogError("Node '%s' not found in topology", nodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", nodeName)
			return
		}

		// Dump the MAC table
		mac_table_dump(&targetNode.node_nw_prop.mac_table, nodeName)
	},
}

var resolveArpCmd = &cobra.Command{
	Use:   "resolve-arp [node-name] [ip-address]",
	Short: "Resolve ARP for IP address on specified node",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]
		ipAddress := args[1]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			LogError("Node '%s' not found in topology", nodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", nodeName)
			return
		}

		// Send ARP broadcast request
		result := send_arp_broadcast_request(targetNode, nil, ipAddress)
		if result != 0 {
			LogError("Failed to send ARP request for IP %s", ipAddress)
			fmt.Printf("Error: Failed to send ARP request for IP %s\n", ipAddress)
		}
	},
}

var showNodeRouteCmd = &cobra.Command{
	Use:   "route [node-name]",
	Short: "Show routing table for a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			LogError("Node '%s' not found in topology", nodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", nodeName)
			return
		}

		// Dump the routing table
		targetNode.node_nw_prop.rt_table.DumpRoutingTable(nodeName)
	},
}

var showNodeArpCmd = &cobra.Command{
	Use:   "arp [node-name]",
	Short: "Show ARP table for a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			LogError("Node '%s' not found in topology", nodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", nodeName)
			return
		}

		// Dump the ARP table
		arp_table_dump(&targetNode.node_nw_prop.arp_table, nodeName)
	},
}

var addRouteCmd = &cobra.Command{
	Use:   "add-route [node-name] [dest-ip/mask] [gateway-ip] [oif]",
	Short: "Add a route to a node's routing table",
	Args:  cobra.ExactArgs(4),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]
		destCIDR := args[1]
		gatewayIP := args[2]
		oif := args[3]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			LogError("Node '%s' not found in topology", nodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", nodeName)
			return
		}
		if get_node_if_by_name(targetNode, oif) == nil {
			fmt.Printf("Error: Interface '%s' not found on node %s\n", oif, nodeName)
			return
		}

		// Parse CIDR (e.g., "192.168.1.0/24")
		parts := strings.Split(destCIDR, "/")
		if len(parts) != 2 {
			fmt.Printf("Error: Invalid CIDR format. Use: dest-ip/mask (e.g., 192.168.1.0/24)\n")
			return
		}

		destIP := parts[0]
		maskStr := parts[1]

		// Parse mask
		var mask uint8
		_, err := fmt.Sscanf(maskStr, "%d", &mask)
		if err != nil || mask > 32 {
			fmt.Printf("Error: Invalid mask: %s (must be 0-32)\n", maskStr)
			return
		}

		// Add route
		err = targetNode.node_nw_prop.rt_table.AddRoute(destIP, mask, gatewayIP, oif)
		if err != nil {
			LogError("Failed to add route: %v", err)
			fmt.Printf("Error: Failed to add route: %v\n", err)
			return
		}

		fmt.Printf("Successfully added route on node %s: %s via %s (%s)\n",
			nodeName, destCIDR, gatewayIP, oif)
	},
}

var addVlanInterfaceCmd = &cobra.Command{
	Use:   "add-vlan-interface [node-name] [vlan-id] [ip-address] [mask]",
	Short: "Add a VLAN interface (SVI) to a node for inter-VLAN routing",
	Args:  cobra.ExactArgs(4),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]
		vlanIDStr := args[1]
		ipAddr := args[2]
		maskStr := args[3]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			LogError("Node '%s' not found in topology", nodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", nodeName)
			return
		}

		// Parse VLAN ID
		var vlanID uint16
		_, err := fmt.Sscanf(vlanIDStr, "%d", &vlanID)
		if err != nil || vlanID < VLAN_MIN || vlanID > VLAN_MAX {
			fmt.Printf("Error: Invalid VLAN ID: %s (must be %d-%d)\n", vlanIDStr, VLAN_MIN, VLAN_MAX)
			return
		}

		// Parse mask
		var mask uint8
		_, err = fmt.Sscanf(maskStr, "%d", &mask)
		if err != nil || mask > 32 {
			fmt.Printf("Error: Invalid mask: %s (must be 0-32)\n", maskStr)
			return
		}

		// Add VLAN interface
		success := targetNode.AddVlanInterface(vlanID, ipAddr, mask)
		if !success {
			fmt.Printf("Error: Failed to add VLAN %d interface to node %s\n", vlanID, nodeName)
			return
		}

		fmt.Printf("✓ Successfully configured VLAN %d interface on node %s with IP %s/%d\n",
			vlanID, nodeName, ipAddr, mask)
		fmt.Printf("  Node %s can now route between VLANs\n", nodeName)
	},
}

var pingCmd = &cobra.Command{
	Use:   "ping [src-node] [dest-ip]",
	Short: "Send a ping from source node to destination IP",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		srcNodeName := args[0]
		destIP := args[1]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the source node
		var srcNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == srcNodeName {
				srcNode = node
				break
			}
		}

		if srcNode == nil {
			LogError("Node '%s' not found in topology", srcNodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", srcNodeName)
			return
		}

		// Send ping
		Layer5PingFunc(srcNode, destIP)
	},
}

var eroPingCmd = &cobra.Command{
	Use:   "ero-ping [src-node] [dest-ip] [ero-ip]",
	Short: "Send a ping via Explicit Route Object (IP-in-IP tunnel through ERO)",
	Long: `Send a ping packet encapsulated in IP-in-IP tunnel, forcing the packet
to go through a specific intermediate node (ERO) before reaching the destination.
Example: ero-ping R1 192.168.3.1 10.0.2.1
This will send a ping from R1 to 192.168.3.1, tunneled through 10.0.2.1`,
	Args: cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		srcNodeName := args[0]
		destIP := args[1]
		eroIP := args[2]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded. Use 'load topology [filename]' first.")
			return
		}

		// Find the source node
		var srcNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == srcNodeName {
				srcNode = node
				break
			}
		}

		if srcNode == nil {
			LogError("Node '%s' not found in topology", srcNodeName)
			fmt.Printf("Error: Node '%s' not found in topology\n", srcNodeName)
			return
		}

		// Send ERO ping
		Layer3EroPingFunc(srcNode, destIP, eroIP)
	},
}

var enableRIPCmd = &cobra.Command{
	Use:   "enable-rip [node-name]",
	Short: "Enable RIP protocol on a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			fmt.Printf("Error: Node '%s' not found\n", nodeName)
			return
		}

		// Enable RIP
		targetNode.rip_state.StartRIP()
		fmt.Printf("✓ RIP enabled on %s\n", nodeName)
	},
}

var disableRIPCmd = &cobra.Command{
	Use:   "disable-rip [node-name]",
	Short: "Disable RIP protocol on a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			fmt.Printf("Error: Node '%s' not found\n", nodeName)
			return
		}

		// Disable RIP
		targetNode.rip_state.StopRIP()
		fmt.Printf("RIP disabled on node %s\n", nodeName)
	},
}

var showNodeRIPCmd = &cobra.Command{
	Use:   "rip [node-name]",
	Short: "Show RIP state for a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		nodeName := args[0]

		if currentTopology == nil {
			fmt.Println("Error: No topology loaded.")
			return
		}

		// Find the node
		var targetNode *Node
		for _, node := range currentTopology.node_list {
			if get_node_name(node) == nodeName {
				targetNode = node
				break
			}
		}

		if targetNode == nil {
			fmt.Printf("Error: Node '%s' not found\n", nodeName)
			return
		}

		// Show RIP state
		targetNode.rip_state.DumpRIPState()
	},
}

var showNodeLLDPCmd = &cobra.Command{
	Use:   "lldp [node-name]",
	Short: "Show LLDP neighbors for a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if currentTopology == nil {
			fmt.Println("Error: No topology loaded.")
			return
		}
		targetNode := findCurrentTopologyNode(args[0])
		if targetNode == nil {
			fmt.Printf("Error: Node '%s' not found\n", args[0])
			return
		}
		targetNode.lldp_state.DumpNeighbors()
	},
}

var enableLLDPCmd = &cobra.Command{
	Use:   "enable-lldp [node-name]",
	Short: "Enable LLDP on a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if currentTopology == nil {
			fmt.Println("Error: No topology loaded.")
			return
		}
		targetNode := findCurrentTopologyNode(args[0])
		if targetNode == nil {
			fmt.Printf("Error: Node '%s' not found\n", args[0])
			return
		}
		targetNode.lldp_state.StartLLDP()
		fmt.Printf("✓ LLDP enabled on %s\n", args[0])
	},
}

var disableLLDPCmd = &cobra.Command{
	Use:   "disable-lldp [node-name]",
	Short: "Disable LLDP on a node",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if currentTopology == nil {
			fmt.Println("Error: No topology loaded.")
			return
		}
		targetNode := findCurrentTopologyNode(args[0])
		if targetNode == nil {
			fmt.Printf("Error: Node '%s' not found\n", args[0])
			return
		}
		targetNode.lldp_state.StopLLDP()
		fmt.Printf("LLDP disabled on node %s\n", args[0])
	},
}

var loadTopologyCmd = &cobra.Command{
	Use:   "topology [filename]",
	Short: "Load topology from YAML file",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		filename := "topologies/triangle.yaml"
		if len(args) > 0 {
			filename = args[0]
		}

		fmt.Printf("Loading topology: %s...\n", filename)
		topology, err := load_topology_from_yaml(filename)
		if err != nil {
			LogError("Error loading topology: %v", err)
			fmt.Printf("✗ Error loading topology: %v\n", err)
			return
		}

		if currentTopology != nil {
			cleanup_graph_resources(currentTopology)
			currentTopology = nil
		}

		currentTopology = topology
		if err := topology.Start(); err != nil {
			cleanup_graph_resources(topology)
			currentTopology = nil
			fmt.Printf("✗ Error starting topology transport: %v\n", err)
			return
		}
		fmt.Printf("Successfully loaded topology: %s\n", get_topology_name(topology))
		fmt.Printf("%s transport started for all nodes\n", topology.transport.Name())
	},
}

var showTopologyCmd = &cobra.Command{
	Use:   "topology",
	Short: "Show network topology",
	Run: func(cmd *cobra.Command, args []string) {
		if currentTopology == nil {
			fmt.Println("No topology loaded. Use 'load topology [filename]' to load a topology first.")
			return
		}

		fmt.Printf("Displaying topology: %s\n", get_topology_name(currentTopology))
		LogDebug("Displaying topology: %s", get_topology_name(currentTopology))
		dump_graph_info(currentTopology)
	},
}

func startInteractiveShell() {
	username := os.Getenv("USER")
	if username == "" {
		username = "user"
	}

	// Liner is used for command history and other interactive CLI features
	line := liner.NewLiner()
	defer line.Close()

	// Enable history
	line.SetCtrlCAborts(true)

	// Load history from file
	historyFile := os.Getenv("HOME") + "/.tcp-ip-stack_history"
	if f, err := os.Open(historyFile); err == nil {
		line.ReadHistory(f)
		f.Close()
	}

	fmt.Printf("Welcome to Network Simulator CLI\n")
	fmt.Printf("Type 'help' for available commands or 'exit' to quit.\n\n")

	for {
		prompt := fmt.Sprintf("%s@nw-simulator> ", username)
		input, err := line.Prompt(prompt)

		if err != nil {
			// Handle Ctrl+C or EOF
			if err == liner.ErrPromptAborted {
				fmt.Println("\nUse 'exit' to quit")
				continue
			}
			break
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		// Add to history
		line.AppendHistory(input)

		// Handle exit
		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			break
		}

		// Parse and execute
		executeCommand(input)
	}

	// Save command history to file
	if f, err := os.Create(historyFile); err == nil {
		line.WriteHistory(f)
		f.Close()
	}
}

func executeCommand(input string) {
	args := strings.Fields(input)
	if len(args) == 0 {
		return
	}

	// Create a temporary root command for parsing this specific input
	cmd := &cobra.Command{}
	cmd.AddCommand(showCmd)
	cmd.AddCommand(loadCmd)
	cmd.AddCommand(runCmd)

	helpCmd := &cobra.Command{
		Use:   "help",
		Short: "Help about any command",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Available commands:")
			fmt.Println("  load topology [file]                       - Load topology from YAML file (default: topologies/triangle.yaml)")
			fmt.Println("  show topology                              - Display loaded network topology")
			fmt.Println("  show node mac <node-name>                  - Show MAC address table for a node")
			fmt.Println("  show node route <node-name>                - Show routing table for a node")
			fmt.Println("  show node arp <node-name>                  - Show ARP table for a node")
			fmt.Println("  show node rip <node-name>                  - Show RIP state for a node")
			fmt.Println("  show node lldp <node-name>                 - Show LLDP neighbors for a node")
			fmt.Println("  run node resolve-arp <node-name> <ip-addr> - Resolve ARP for IP address on specified node")
			fmt.Println("  run node add-route <node> <dest/mask> <gw> <oif> - Add a route to node's routing table")
			fmt.Println("  run node ping <src-node> <dest-ip>         - Send ping from source node to destination IP")
			fmt.Println("  run node enable-rip <node-name>            - Enable RIP protocol on a node")
			fmt.Println("  run node disable-rip <node-name>           - Disable RIP protocol on a node")
			fmt.Println("  run node enable-lldp <node-name>           - Enable LLDP on a node")
			fmt.Println("  run node disable-lldp <node-name>          - Disable LLDP on a node")
			fmt.Println("  help                                       - Show this help message")
			fmt.Println("  exit                                       - Exit the shell")
		},
	}
	cmd.AddCommand(helpCmd)

	// Execute the command
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}

func init() {
	showCmd.AddCommand(showTopologyCmd)
	showCmd.AddCommand(showNodeCmd)
	showNodeCmd.AddCommand(showNodeMacCmd)
	showNodeCmd.AddCommand(showNodeRouteCmd)
	showNodeCmd.AddCommand(showNodeArpCmd)
	showNodeCmd.AddCommand(showNodeRIPCmd)
	showNodeCmd.AddCommand(showNodeLLDPCmd)
	loadCmd.AddCommand(loadTopologyCmd)
	runCmd.AddCommand(runNodeCmd)
	runNodeCmd.AddCommand(resolveArpCmd)
	runNodeCmd.AddCommand(addRouteCmd)
	runNodeCmd.AddCommand(addVlanInterfaceCmd)
	runNodeCmd.AddCommand(pingCmd)
	runNodeCmd.AddCommand(eroPingCmd)
	runNodeCmd.AddCommand(enableRIPCmd)
	runNodeCmd.AddCommand(disableRIPCmd)
	runNodeCmd.AddCommand(enableLLDPCmd)
	runNodeCmd.AddCommand(disableLLDPCmd)
}

func main() {
	// signal handling for cleanup
	setupSignalHandler()

	if err := rootCmd.Execute(); err != nil {
		cleanup()
		os.Exit(1)
	}

	cleanup()
}

// graceful shutdown on SIGINT/SIGTERM
func setupSignalHandler() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt signal. Cleaning up...")
		cleanup()
		os.Exit(0)
	}()
}

// cleanup operations before exit
func cleanup() {
	if currentTopology != nil {
		cleanup_graph_resources(currentTopology)
		currentTopology = nil
	}
}
