//go:build js && wasm

package main

import (
	"fmt"
	"syscall/js"
)

var wasmFunctions []js.Func

func main() {
	// Trace commands return finite event snapshots. Background protocol events are
	// intentionally not posted to the UI, so periodic daemons cannot extend playback.
	simulator := NewSimulator(func() FrameTransport { return NewMemoryTransport() }, nil)
	exposeWasmFunction("loadTopology", func(args []js.Value) any {
		if len(args) != 1 {
			return rejectedPromise(fmt.Errorf("loadTopology expects YAML text"))
		}
		return asyncResult(func() (js.Value, error) {
			state, err := simulator.LoadTopology(args[0].String())
			if err != nil {
				return js.Undefined(), err
			}
			return topologyStateToJS(state), nil
		})
	})
	exposeWasmFunction("ping", func(args []js.Value) any {
		if len(args) != 2 {
			return rejectedPromise(fmt.Errorf("ping expects source and destination"))
		}
		return asyncResult(func() (js.Value, error) {
			result, err := simulator.Ping(args[0].String(), args[1].String())
			if err != nil {
				return js.Undefined(), err
			}
			return pingResultToJS(result), nil
		})
	})
	exposeWasmFunction("tracePing", func(args []js.Value) any {
		if len(args) != 2 {
			return rejectedPromise(fmt.Errorf("tracePing expects source and destination"))
		}
		return asyncResult(func() (js.Value, error) {
			result, err := simulator.TracePing(args[0].String(), args[1].String())
			if err != nil {
				return js.Undefined(), err
			}
			return pingTraceResultToJS(result), nil
		})
	})
	exposeWasmFunction("traceTraceroute", func(args []js.Value) any {
		if len(args) != 2 {
			return rejectedPromise(fmt.Errorf("traceTraceroute expects source and destination"))
		}
		return asyncResult(func() (js.Value, error) {
			result, err := simulator.TraceTraceroute(args[0].String(), args[1].String(), DefaultTracerouteMaxHops)
			if err != nil {
				return js.Undefined(), err
			}
			return tracerouteTraceResultToJS(result), nil
		})
	})
	exposeWasmFunction("traceLLDP", func(args []js.Value) any {
		if len(args) != 0 {
			return rejectedPromise(fmt.Errorf("traceLLDP does not accept arguments"))
		}
		return asyncResult(func() (js.Value, error) {
			result, err := simulator.TraceLLDP()
			if err != nil {
				return js.Undefined(), err
			}
			return lldpTraceResultToJS(result), nil
		})
	})
	exposeWasmFunction("getNodeState", func(args []js.Value) any {
		if len(args) != 1 {
			return rejectedPromise(fmt.Errorf("getNodeState expects a node name"))
		}
		return asyncResult(func() (js.Value, error) {
			state, err := simulator.GetNodeState(args[0].String())
			if err != nil {
				return js.Undefined(), err
			}
			return nodeStateToJS(state), nil
		})
	})
	exposeWasmFunction("setLLDP", func(args []js.Value) any {
		if len(args) != 2 {
			return rejectedPromise(fmt.Errorf("setLLDP expects a node name and enabled state"))
		}
		return asyncResult(func() (js.Value, error) {
			state, err := simulator.SetLLDP(args[0].String(), args[1].Bool())
			if err != nil {
				return js.Undefined(), err
			}
			return nodeStateToJS(state), nil
		})
	})
	exposeWasmFunction("setSTP", func(args []js.Value) any {
		if len(args) != 2 {
			return rejectedPromise(fmt.Errorf("setSTP expects a node name and enabled state"))
		}
		return asyncResult(func() (js.Value, error) {
			state, err := simulator.SetSTP(args[0].String(), args[1].Bool())
			if err != nil {
				return js.Undefined(), err
			}
			return nodeStateToJS(state), nil
		})
	})
	exposeWasmFunction("setSTPPriority", func(args []js.Value) any {
		if len(args) != 2 {
			return rejectedPromise(fmt.Errorf("setSTPPriority expects a node name and bridge priority"))
		}
		priority := args[1].Int()
		if priority < 0 || priority > int(^uint16(0)) {
			return rejectedPromise(fmt.Errorf("bridge priority %d is out of range", priority))
		}
		return asyncResult(func() (js.Value, error) {
			state, err := simulator.SetSTPPriority(args[0].String(), uint16(priority))
			if err != nil {
				return js.Undefined(), err
			}
			return nodeStateToJS(state), nil
		})
	})
	exposeWasmFunction("setLinkState", func(args []js.Value) any {
		if len(args) != 5 {
			return rejectedPromise(fmt.Errorf("setLinkState expects two link endpoints and an up state"))
		}
		return asyncResult(func() (js.Value, error) {
			state, err := simulator.SetLinkState(
				args[0].String(), args[1].String(), args[2].String(), args[3].String(), args[4].Bool(),
			)
			if err != nil {
				return js.Undefined(), err
			}
			return topologyStateToJS(state), nil
		})
	})
	exposeWasmFunction("reset", func(args []js.Value) any {
		return asyncResult(func() (js.Value, error) {
			state, err := simulator.Reset()
			if err != nil {
				return js.Undefined(), err
			}
			return topologyStateToJS(state), nil
		})
	})
	select {}
}

func exposeWasmFunction(name string, handler func([]js.Value) any) {
	function := js.FuncOf(func(this js.Value, args []js.Value) any {
		return handler(args)
	})
	wasmFunctions = append(wasmFunctions, function)
	js.Global().Set(name, function)
}

func asyncResult(work func() (js.Value, error)) js.Value {
	return newPromise(func(resolve, reject js.Value) {
		result, err := work()
		if err != nil {
			reject.Invoke(err.Error())
			return
		}
		resolve.Invoke(result)
	})
}

func rejectedPromise(err error) js.Value {
	return newPromise(func(resolve, reject js.Value) {
		reject.Invoke(err.Error())
	})
}

func newPromise(work func(resolve, reject js.Value)) js.Value {
	executor := js.FuncOf(func(this js.Value, args []js.Value) any {
		go work(args[0], args[1])
		return nil
	})
	promise := js.Global().Get("Promise").New(executor)
	executor.Release()
	return promise
}

func topologyStateToJS(state *TopologyState) js.Value {
	if state == nil {
		return js.Null()
	}
	nodes := make([]any, len(state.Nodes))
	for index := range state.Nodes {
		nodes[index] = nodeStateObject(&state.Nodes[index])
	}
	links := make([]any, len(state.Links))
	for index, link := range state.Links {
		links[index] = map[string]any{
			"from":          link.From,
			"fromInterface": link.FromInterface,
			"to":            link.To,
			"toInterface":   link.ToInterface,
			"cost":          link.Cost,
			"up":            link.Up,
		}
	}
	var canvas any
	if state.Canvas != nil {
		canvas = map[string]any{"width": state.Canvas.Width, "height": state.Canvas.Height}
	}
	return js.ValueOf(map[string]any{
		"name":               state.Name,
		"description":        state.Description,
		"summary":            state.Summary,
		"defaultSource":      state.DefaultSource,
		"defaultDestination": state.DefaultDestination,
		"canvas":             canvas,
		"transport":          state.Transport,
		"nodes":              nodes,
		"links":              links,
	})
}

func nodeStateToJS(state *NodeState) js.Value {
	if state == nil {
		return js.Null()
	}
	return js.ValueOf(nodeStateObject(state))
}

func nodeStateObject(state *NodeState) map[string]any {
	interfaces := make([]any, len(state.Interfaces))
	for index, intf := range state.Interfaces {
		allowedVLANs := make([]any, len(intf.AllowedVLANs))
		for vlanIndex, vlan := range intf.AllowedVLANs {
			allowedVLANs[vlanIndex] = vlan
		}
		interfaces[index] = map[string]any{
			"name":         intf.Name,
			"ip":           intf.IP,
			"mask":         intf.Mask,
			"mode":         intf.Mode,
			"accessVlan":   intf.AccessVLAN,
			"nativeVlan":   intf.NativeVLAN,
			"allowedVlans": allowedVLANs,
			"up":           intf.Up,
		}
	}
	vlans := make([]any, len(state.VLANInterfaces))
	for index, intf := range state.VLANInterfaces {
		vlans[index] = map[string]any{"vlan": intf.VLAN, "ip": intf.IP, "mask": intf.Mask}
	}
	routes := make([]any, len(state.Routes))
	for index, route := range state.Routes {
		routes[index] = map[string]any{
			"destination":   route.Destination,
			"mask":          route.Mask,
			"gateway":       route.Gateway,
			"interface":     route.Interface,
			"source":        route.Source,
			"adminDistance": route.AdminDistance,
			"metric":        route.Metric,
			"direct":        route.Direct,
		}
	}
	arp := make([]any, len(state.ARP))
	for index, entry := range state.ARP {
		arp[index] = map[string]any{
			"ip":        entry.IP,
			"mac":       entry.MAC,
			"interface": entry.Interface,
			"pending":   entry.Pending,
		}
	}
	mac := make([]any, len(state.MAC))
	for index, entry := range state.MAC {
		mac[index] = map[string]any{"mac": entry.MAC, "vlan": entry.VLAN, "interface": entry.Interface}
	}
	lldp := make([]any, len(state.LLDP))
	for index, neighbor := range state.LLDP {
		lldp[index] = map[string]any{
			"systemName":     neighbor.SystemName,
			"port":           neighbor.Port,
			"localInterface": neighbor.LocalInterface,
		}
	}
	stpPorts := make([]any, len(state.STP.Ports))
	for index, port := range state.STP.Ports {
		stpPorts[index] = map[string]any{
			"interface": port.Interface,
			"role":      port.Role,
			"state":     port.State,
			"pathCost":  port.PathCost,
			"portId":    port.PortID,
		}
	}
	stp := map[string]any{
		"enabled":      state.STP.Enabled,
		"priority":     state.STP.Priority,
		"bridgeId":     state.STP.BridgeID,
		"rootId":       state.STP.RootID,
		"isRoot":       state.STP.IsRoot,
		"rootPathCost": state.STP.RootPathCost,
		"rootPort":     state.STP.RootPort,
		"ports":        stpPorts,
	}
	var position any
	if state.Position != nil {
		position = map[string]any{"x": state.Position.X, "y": state.Position.Y}
	}
	return map[string]any{
		"name":           state.Name,
		"type":           string(state.Type),
		"position":       position,
		"loopback":       state.Loopback,
		"lldpEnabled":    state.LLDPEnabled,
		"interfaces":     interfaces,
		"vlanInterfaces": vlans,
		"routes":         routes,
		"arp":            arp,
		"mac":            mac,
		"lldp":           lldp,
		"stp":            stp,
	}
}

func pingResultToJS(result *PingResult) js.Value {
	if result == nil {
		return js.Null()
	}
	return js.ValueOf(map[string]any{
		"source":        result.Source,
		"destination":   result.Destination,
		"destinationIp": result.DestinationIP,
		"replyReceived": result.ReplyReceived,
		"errors":        icmpErrorsObject(result.Errors),
	})
}

// icmpErrorsObject exposes the error reports a ping provoked, so the UI can
// say why a ping failed instead of only that it did.
func icmpErrorsObject(reports []ICMPErrorReport) []any {
	errors := make([]any, len(reports))
	for index, report := range reports {
		errors[index] = map[string]any{
			"reason":        report.Reason,
			"reporterIp":    report.ReporterIP,
			"originalDstIp": report.OriginalDstIP,
			"type":          int(report.Type),
			"code":          int(report.Code),
		}
	}
	return errors
}

func pingTraceResultToJS(result *PingTraceResult) js.Value {
	if result == nil {
		return js.Null()
	}
	return js.ValueOf(map[string]any{
		"source":        result.Source,
		"destination":   result.Destination,
		"destinationIp": result.DestinationIP,
		"replyReceived": result.ReplyReceived,
		"errors":        icmpErrorsObject(result.Errors),
		"events":        simulationEventsObject(result.Events),
	})
}

func tracerouteTraceResultToJS(result *TracerouteTraceResult) js.Value {
	if result == nil {
		return js.Null()
	}
	hops := make([]any, len(result.Hops))
	for index, hop := range result.Hops {
		hops[index] = map[string]any{
			"ttl":      hop.TTL,
			"address":  hop.Address,
			"reached":  hop.Reached,
			"timedOut": hop.TimedOut,
			"reason":   hop.Reason,
		}
	}
	return js.ValueOf(map[string]any{
		"source":        result.Source,
		"destination":   result.Destination,
		"destinationIp": result.DestinationIP,
		"reached":       result.Reached,
		"hops":          hops,
		"events":        simulationEventsObject(result.Events),
	})
}

func lldpTraceResultToJS(result *LLDPTraceResult) js.Value {
	if result == nil {
		return js.Null()
	}
	advertisers := make([]any, len(result.Advertisers))
	for index, advertiser := range result.Advertisers {
		advertisers[index] = advertiser
	}
	return js.ValueOf(map[string]any{
		"advertisers": advertisers,
		"events":      simulationEventsObject(result.Events),
	})
}

func simulationEventsObject(events []SimulationEvent) []any {
	values := make([]any, len(events))
	for index, event := range events {
		values[index] = simulationEventObject(event)
	}
	return values
}

func simulationEventObject(event SimulationEvent) map[string]any {
	fields := stringMapToJS(event.Fields)
	value := map[string]any{
		"sequence": event.Sequence,
		"time":     event.Time,
		"protocol": event.Protocol,
		"node":     event.Node,
		"action":   event.Action,
	}
	if event.From != "" {
		value["from"] = event.From
	}
	if event.To != "" {
		value["to"] = event.To
	}
	if event.Interface != "" {
		value["interface"] = event.Interface
	}
	if event.PeerInterface != "" {
		value["peerInterface"] = event.PeerInterface
	}
	if event.SourceInterface != "" {
		value["sourceInterface"] = event.SourceInterface
	}
	if event.DestinationInterface != "" {
		value["destinationInterface"] = event.DestinationInterface
	}
	if event.Size > 0 {
		value["size"] = event.Size
	}
	if len(fields) > 0 {
		value["fields"] = fields
	}
	if event.Table != nil {
		table := map[string]any{
			"kind":   event.Table.Kind,
			"result": event.Table.Result,
			"query":  stringMapToJS(event.Table.Query),
		}
		if event.Table.Entry != nil {
			table["entry"] = stringMapToJS(event.Table.Entry)
		}
		value["table"] = table
	}
	return value
}

func stringMapToJS(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
