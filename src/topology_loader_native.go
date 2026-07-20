//go:build !js

package main

import (
	"fmt"
	"os"
)

func load_topology_from_yaml(filename string) (*Graph, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read topology file %s: %v", filename, err)
	}
	return load_topology_from_bytes(data)
}

func load_topology_from_string(data string) (*Graph, error) {
	return load_topology_from_bytes([]byte(data))
}

func load_topology_from_bytes(data []byte) (*Graph, error) {
	return load_topology_from_bytes_with_transport(data, newDefaultTransport())
}
