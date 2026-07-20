# Packet tracer UI

This dependency-free browser client runs the Go simulator in a Web Worker and renders its real packet events.

Run it from the repository root:

```bash
make ui
```

Then open `http://localhost:8080`.

Use `make ui UI_PORT=8090` if port 8080 is already occupied.

`make wasm` strips debug metadata and creates both the WebAssembly binary and a gzip-compressed transfer asset. `make dist` assembles those generated files with the browser runtime and selectable scenarios in a runtime-only `dist/` directory for deployment. Generated assets and `dist/` are ignored by Git; use `make clean` to remove them.

The scenario selector includes practical network-engineering examples for switched LAN connectivity, multi-switch VLAN trunking, access and trunk VLAN mismatches, inter-VLAN routing, multi-hop static routing, default-gateway and return-route failures, longest-prefix route selection, STP redundancy and uplink failover, LLDP neighbor discovery, and an integrated campus topology. Each scenario presets a useful source and destination and states whether the baseline trace should succeed or fail. **Run Ping** performs one reachability test and captures its related ARP, ICMP, and Ethernet events. **Run Traceroute** sends ICMP echo probes with increasing TTLs (up to 30), stops when the destination replies, and displays each router response or timeout in a compact hop list. **Run LLDP Discovery** captures one discovery round across every enabled, connected port. Each captured result starts playing automatically and can be paused or resumed until it finishes. Enable or disable LLDP from the selected device's inspector; configuration changes and periodic background refreshes update simulator state without replacing or extending a packet capture.

For switches, the inspector reports the IEEE 802.1D root bridge, root port and cost, bridge identifiers, and every port's role, state, and path cost. The canvas marks the root bridge and any endpoint that STP prevents from forwarding data. The inspector can temporarily enable or disable STP and change bridge priority; **Reset simulation** restores `stp` and `stp_priority` from the loaded YAML. Link `cost` values are used as STP path costs, with the default cost of 19 used when YAML supplies zero or omits the field.

A ping or traceroute destination can be a device name, one of a device's configured physical-interface, VLAN-interface, or loopback addresses, or any manually entered IPv4 address. Open the scenario selector and use `Download selected YAML` to save the active template locally. `Load YAML` reads the project's topology format in the browser, and every timeline item comes from a finite Go simulation snapshot rather than a hard-coded or live event stream. Run executes the selected protocol, captures a new snapshot, and starts playback. Play does the same when no trace has been captured yet; afterward it pauses, resumes, or replays that capture. Timeline rows appear as playback reaches each forwarding decision. Use Play/Pause to control automatic playback, or the previous and next controls to inspect the captured trace one event at a time. `Download JSON` saves the timeline's raw simulator events as a formatted JSON array and respects the active protocol filter.

The browser does not contain topology-specific render data. Each scenario YAML supplies its description, summary, default ping endpoints, canvas dimensions, and every node's `position`; the Go parser validates these values and returns them with the WebAssembly topology snapshot.
