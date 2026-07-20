# Go Network Simulator

An interactive Ethernet and IPv4 packet-flow simulator written in Go and WebAssembly.

[Open the browser simulator](https://hotpath-hooligan.github.io/packet_tracer/)

## What you can explore

- Follow ARP, ICMP, and LLDP packets one forwarding decision at a time
- Work through practical switching, VLAN trunking, inter-VLAN routing, static routing, route-selection, STP failover, LLDP, and campus scenarios
- Inspect device interfaces, STP port roles, MAC and ARP tables, routes, and LLDP neighbors
- Load your own YAML topology without uploading it
- Experiment with Layer 2 switching, 802.1D spanning tree, 802.1Q VLANs, static routing, RIP, and IP-in-IP paths

## Use the browser simulator

Choose a scenario and a trace type, then click **Run Ping** or **Run LLDP Discovery**. Ping runs one reachability test and captures its related packet events. LLDP runs one discovery round across every enabled, connected port. Either result starts playing automatically, with the packet timeline growing one event at a time as playback advances. Use the play/pause or step controls to inspect the packet timeline. Select a device or event to inspect its state and forwarding decision.

To run the site locally, install Go 1.24.6 or newer, Make, and Python 3:

```bash
git clone https://github.com/hotpath-hooligan/packet_tracer.git
cd packet_tracer
make ui
```

Open `http://localhost:8080`. To use another port, run `make ui UI_PORT=8090`.

Run `make dist` to build the production Pages distribution in `dist/`. It contains only the browser runtime, generated WebAssembly assets, and selectable scenario files.

## Load a custom topology

Start with [`topologies/sample-input.yaml`](topologies/sample-input.yaml), which documents every supported field, or use this minimal same-subnet example:

```yaml
topology:
  name: Two hosts
  description: Two hosts connected by one Ethernet link.
  summary: H1 → H2
  default_source: H1
  default_destination: H2
  canvas: {width: 1000, height: 520}

nodes:
  - name: H1
    type: host
    position: {x: 250, y: 260}
    interfaces:
      - name: eth0
        ip: 10.0.0.1
        mask: 24
  - name: H2
    type: host
    position: {x: 750, y: 260}
    interfaces:
      - name: eth0
        ip: 10.0.0.2
        mask: 24

links:
  - from_node: H1
    from_interface: eth0
    to_node: H2
    to_interface: eth0
```

In the browser, click **Load YAML** to open the file. Topologies can contain up to 100 devices, with up to 10 interfaces per device. 

Set `lldp: true` on a node to start LLDP when the topology loads. LLDP is disabled when the field is omitted and can still be enabled or disabled in the UI.

Switches run IEEE 802.1D spanning tree by default. Use `stp: false` on a switch to disable it, `stp_priority` to select a bridge priority from 0 through 61440 in steps of 4096, and `links[].cost` to influence the chosen path. The browser marks the root bridge and blocked port endpoints; its selected-device inspector shows the complete tree state and supports temporary runtime changes. Reset restores the YAML configuration.

More examples are available in [`topologies/`](topologies/) and [`ui/scenarios/`](ui/scenarios/).

## Use the CLI

The native CLI uses local UDP sockets to carry simulated frames and requires a Unix-like operating system.

```bash
make build
./tcp-ip-stack
```

Load a topology, inspect its devices, and run a ping:

```text
load topology topologies/triangle.yaml
show topology
show node route R0
show node arp R0
run node ping R0 10.1.1.2
```

Run `help` inside the shell for all commands, and `exit` or `quit` to stop the simulator.

## License

MIT; see [LICENSE](LICENSE).
