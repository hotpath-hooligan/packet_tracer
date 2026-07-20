//go:build !js

package main

type udpMonitorSet struct {
	graph *Graph
}

func start_udp_monitoring(graph *Graph) *udpMonitorSet {
	if graph == nil {
		return nil
	}
	if err := graph.Start(); err != nil {
		LogError("Failed to start %s transport: %v", graph.transport.Name(), err)
		return nil
	}
	return &udpMonitorSet{graph: graph}
}

func stop_udp_monitoring(monitors *udpMonitorSet) {
	if monitors != nil && monitors.graph != nil {
		monitors.graph.Stop()
	}
}
