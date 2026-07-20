class WasmRuntime {
  constructor(workerUrl) {
    this.worker = new Worker(workerUrl);
    this.nextId = 1;
    this.pending = new Map();
    this.ready = new Promise((resolve, reject) => {
      this.resolveReady = resolve;
      this.rejectReady = reject;
    });
    this.worker.onmessage = ({ data }) => this.handleMessage(data);
    this.worker.onerror = (error) => this.rejectReady(error);
  }

  handleMessage(message) {
    if (message.type === "runtime-ready") {
      this.resolveReady();
      return;
    }
    if (message.type === "fatal") {
      const error = new Error(message.error);
      this.rejectReady(error);
      this.pending.forEach(({ reject }) => reject(error));
      this.pending.clear();
      return;
    }
    if (message.type !== "response") return;
    const request = this.pending.get(message.id);
    if (!request) return;
    this.pending.delete(message.id);
    if (message.error) request.reject(new Error(message.error));
    else request.resolve(message.result);
  }

  async command(command, ...args) {
    await this.ready;
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.worker.postMessage({ id, command, args });
    });
  }
}

const runtime = new WasmRuntime("wasm-worker.js");

const scenarios = {
  "switched-lan": {
    path: "scenarios/switched-lan.yaml"
  },
  "multi-switch-vlan-trunking": {
    path: "scenarios/multi-switch-vlan-trunking.yaml"
  },
  "access-vlan-mismatch": {
    path: "scenarios/access-vlan-mismatch.yaml"
  },
  "trunk-allowed-vlan-mismatch": {
    path: "scenarios/trunk-allowed-vlan-mismatch.yaml"
  },
  "inter-vlan-routing": {
    path: "scenarios/inter-vlan-routing.yaml"
  },
  "static-routing-between-sites": {
    path: "scenarios/static-routing-between-sites.yaml"
  },
  "default-gateway-misconfiguration": {
    path: "scenarios/default-gateway-misconfiguration.yaml"
  },
  "missing-return-route": {
    path: "scenarios/missing-return-route.yaml"
  },
  "longest-prefix-route-selection": {
    path: "scenarios/longest-prefix-route-selection.yaml"
  },
  "stp-redundant-topology": {
    path: "scenarios/stp-loop.yaml"
  },
  "stp-uplink-failover": {
    path: "scenarios/stp-uplink-failover.yaml"
  },
  "lldp-neighbor-discovery": {
    path: "scenarios/lldp-neighbor-discovery.yaml"
  },
  "campus-vlan-routing": {
    path: "scenarios/campus-vlan-routing.yaml"
  }
};

const defaultScenario = "inter-vlan-routing";
const DEFAULT_CANVAS_WIDTH = 1000;
const DEFAULT_CANVAS_HEIGHT = 520;
const TOPOLOGY_NODE_WIDTH = 146;
const TOPOLOGY_NODE_HEIGHT = 82;
const LINK_LABEL_GAP = 8;
const LAYOUT_COLUMN_WIDTH = 170;
const LAYOUT_ROW_HEIGHT = 220;
const LAYOUT_MARGIN = 110;
const STP_PRIORITY_STEP = 4096;
const STP_PRIORITIES = Array.from({ length: 16 }, (_, index) => index * STP_PRIORITY_STEP);

const state = {
  topology: {
    name: "",
    scenario: "Loading…",
    description: "Starting Go WebAssembly",
    canvas: { width: DEFAULT_CANVAS_WIDTH, height: DEFAULT_CANVAS_HEIGHT },
    defaultSource: "",
    defaultDestination: "",
    devices: [],
    links: []
  },
  events: [],
  rawEvents: [],
  capturedEvents: [],
  playbackEvents: [],
  playbackIndex: -1,
  tracerouteHops: [],
  timelineMode: "SUMMARY",
  selectedDevice: "",
  selectedEvent: -1,
  revealedThrough: -1,
  playing: false,
  timer: null,
  status: "loading",
  filter: "ALL",
  capturingTrace: false,
  lldpDiscoveries: new Set(),
  lldpBusy: false,
  stpBusy: false,
  linkBusy: false,
  tracePathLinks: new Set(),
  traceDropEvent: null,
  activeScenario: defaultScenario,
  loadingScenario: true,
  downloadingTemplate: false,
  customScenario: null
};

const elements = {
  appShell: document.querySelector("#appShell"),
  collapseToggles: Array.from(document.querySelectorAll("[data-collapse]")),
  scenarioSelect: document.querySelector("#scenarioSelect"),
  scenarioPicker: document.querySelector("#scenarioPicker"),
  scenarioMenu: document.querySelector("#scenarioMenu"),
  scenarioName: document.querySelector("#scenarioName"),
  scenarioMeta: document.querySelector("#scenarioMeta"),
  customScenarioOption: document.querySelector("#customScenarioOption"),
  downloadScenarioButton: document.querySelector("#downloadScenarioButton"),
  downloadScenarioName: document.querySelector("#downloadScenarioName"),
  loadButton: document.querySelector("#loadButton"),
  yamlInput: document.querySelector("#yamlInput"),
  previousEventButton: document.querySelector("#previousEventButton"),
  playPauseButton: document.querySelector("#playPauseButton"),
  nextEventButton: document.querySelector("#nextEventButton"),
  resetButton: document.querySelector("#resetButton"),
  traceButton: document.querySelector("#traceButton"),
  traceButtonLabel: document.querySelector("#traceButtonLabel"),
  traceType: document.querySelector("#traceType"),
  traceEndpoints: document.querySelector("#traceEndpoints"),
  tracerouteSummary: document.querySelector("#tracerouteSummary"),
  tracerouteHopList: document.querySelector("#tracerouteHopList"),
  sourceNode: document.querySelector("#sourceNode"),
  destinationNode: document.querySelector("#destinationNode"),
  destinationOptions: document.querySelector("#destinationOptions"),
  deviceSearch: document.querySelector("#deviceSearch"),
  deviceList: document.querySelector("#deviceList"),
  deviceCount: document.querySelector("#deviceCount"),
  protocolHelpButton: document.querySelector("#protocolHelpButton"),
  protocolHelpPopover: document.querySelector("#protocolHelpPopover"),
  protocolHelpClose: document.querySelector("#protocolHelpClose"),
  linkLayer: document.querySelector("#linkLayer"),
  nodeLayer: document.querySelector("#nodeLayer"),
  packetMarker: document.querySelector("#packetMarker"),
  dropMarker: document.querySelector("#dropMarker"),
  locationToast: document.querySelector("#locationToast"),
  inspectorHeading: document.querySelector("#inspectorHeading"),
  inspectorContent: document.querySelector("#inspectorContent"),
  lldpToggle: document.querySelector("#lldpToggle"),
  eventList: document.querySelector("#eventList"),
  eventCount: document.querySelector("#eventCount"),
  timelineSubtitle: document.querySelector("#timelineSubtitle"),
  timelineModeButtons: Array.from(document.querySelectorAll("[data-timeline-mode]")),
  downloadEventsButton: document.querySelector("#downloadEventsButton"),
  eventDetail: document.querySelector("#eventDetail"),
  runState: document.querySelector("#runState"),
  stepCounter: document.querySelector("#stepCounter"),
  notice: document.querySelector("#notice"),
  canvasSubtitle: document.querySelector("#canvasSubtitle"),
  topologyStage: document.querySelector("#topologyStage"),
  topologyScroll: document.querySelector("#topologyScroll"),
  topologySurface: document.querySelector("#topologySurface"),
  linkLabelLayer: document.querySelector("#linkLabelLayer")
};

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

const COLLAPSE_LABELS = {
  devices: "devices panel",
  inspector: "selected device panel",
  timeline: "packet timeline"
};

function collapseStorageKey(panel) {
  return `packet-tracer:collapsed:${panel}`;
}

function readStoredCollapsed(panel) {
  try {
    return localStorage.getItem(collapseStorageKey(panel)) === "true";
  } catch {
    return false;
  }
}

function storeCollapsed(panel, collapsed) {
  try {
    localStorage.setItem(collapseStorageKey(panel), String(collapsed));
  } catch {
    /* storage unavailable, collapse still works for this session */
  }
}

function applyPanelCollapsed(toggle, collapsed) {
  const panel = toggle.dataset.collapse;
  elements.appShell.classList.toggle(`is-${panel}-collapsed`, collapsed);
  toggle.setAttribute("aria-expanded", String(!collapsed));
  toggle.setAttribute("aria-label", `${collapsed ? "Expand" : "Collapse"} ${COLLAPSE_LABELS[panel] || panel}`);
}

function scenarioLabel(key) {
  return elements.scenarioMenu.querySelector(`[data-scenario="${key}"] .scenario-option-name`)?.textContent || "Selected scenario";
}

function setScenarioMenuOpen(open, { focusSelected = false } = {}) {
  const shouldOpen = Boolean(open) && !elements.scenarioSelect.disabled;
  elements.scenarioMenu.hidden = !shouldOpen;
  elements.scenarioSelect.setAttribute("aria-expanded", String(shouldOpen));
  if (shouldOpen && focusSelected) {
    elements.scenarioMenu.querySelector(`[data-scenario="${state.activeScenario}"]`)?.focus();
  }
}

function setProtocolHelpOpen(open, { returnFocus = false } = {}) {
  const shouldOpen = Boolean(open);
  elements.protocolHelpPopover.hidden = !shouldOpen;
  elements.protocolHelpButton.setAttribute("aria-expanded", String(shouldOpen));
  if (shouldOpen) elements.protocolHelpPopover.focus();
  else if (returnFocus) elements.protocolHelpButton.focus();
}

function syncScenarioPicker() {
  const label = scenarioLabel(state.activeScenario);
  elements.scenarioName.textContent = label;
  elements.downloadScenarioName.textContent = scenarios[state.activeScenario] ? label : "Built-in scenarios only";
  elements.scenarioMenu.querySelectorAll("[data-scenario]").forEach((option) => {
    option.setAttribute("aria-checked", String(option.dataset.scenario === state.activeScenario));
  });
}

function normalizeProtocol(protocol) {
  return protocol.toLowerCase();
}

function deviceIcon(type) {
  if (type === "switch") {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="7" width="18" height="10" rx="2"/><path d="M7 12h2m2 0h2m2 0h2M7 9.5h10"/></svg>';
  }
  if (type === "router") {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><ellipse cx="12" cy="12" rx="9" ry="6"/><path d="m8 12 2-2m-2 2 2 2m6-2-2-2m2 2-2 2M12 6v12"/></svg>';
  }
  return '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="4" width="16" height="12" rx="2"/><path d="M8 20h8m-4-4v4"/></svg>';
}

function getDevice(name) {
  return state.topology.devices.find((device) => device.name === name);
}

// Tier a device by role: routers on top, switches between them and the hosts.
// Falls back to promoting switches when a topology has no router at all.
function layoutTier(type, hasRouter) {
  if (type === "router") return 0;
  if (type === "switch") return hasRouter ? 1 : 0;
  return hasRouter ? 2 : 1;
}

// Positions are optional in YAML. Anything left unplaced gets a tiered layout:
// each tier is ordered by the average slot of its neighbours in the tier above,
// which keeps hosts sitting under their own switch instead of crossing the canvas.
function autoLayout(nodes, links) {
  const hasRouter = nodes.some((node) => node.type === "router");
  const neighbors = new Map(nodes.map((node) => [node.name, []]));
  links.forEach((link) => {
    neighbors.get(link.from)?.push(link.to);
    neighbors.get(link.to)?.push(link.from);
  });

  const byTier = [];
  nodes.forEach((node) => {
    const tier = layoutTier(node.type, hasRouter);
    (byTier[tier] ||= []).push(node);
  });
  // Drop empty ranks so a hosts-only topology renders as one row, not a gap.
  const tiers = byTier.filter((tier) => tier?.length);

  const slots = new Map();
  tiers.forEach((tier, index) => {
    if (index === 0) {
      tier.sort((a, b) => a.name.localeCompare(b.name));
    } else {
      const barycenter = (node) => {
        const above = neighbors.get(node.name).filter((name) => slots.has(name));
        if (!above.length) return Number.MAX_SAFE_INTEGER;
        return above.reduce((sum, name) => sum + slots.get(name), 0) / above.length;
      };
      tier.sort((a, b) => barycenter(a) - barycenter(b) || a.name.localeCompare(b.name));
    }
    tier.forEach((node, slot) => slots.set(node.name, slot));
  });

  // Widths are assigned bottom-up: the leaf tier spreads out evenly, then every
  // tier above centres each device over the children it actually serves. Centring
  // tiers independently instead would leave a six-switch row bunched in the middle
  // of a thirty-host row, dragging every uplink across the canvas.
  const xs = new Map();
  for (let index = tiers.length - 1; index >= 0; index--) {
    let previousX = null;
    tiers[index].forEach((node) => {
      const children = index === tiers.length - 1
        ? []
        : neighbors.get(node.name).filter((name) => xs.has(name));
      const floor = previousX === null ? 0 : previousX + LAYOUT_COLUMN_WIDTH;
      const centred = children.length
        ? children.reduce((sum, name) => sum + xs.get(name), 0) / children.length
        : floor;
      const x = Math.max(centred, floor);
      xs.set(node.name, x);
      previousX = x;
    });
  }

  const values = [...xs.values()];
  const span = Math.max(...values) - Math.min(...values);
  const width = Math.max(DEFAULT_CANVAS_WIDTH, span + LAYOUT_MARGIN * 2);
  const offset = (width - span) / 2 - Math.min(...values);
  const height = Math.max(DEFAULT_CANVAS_HEIGHT, (tiers.length - 1) * LAYOUT_ROW_HEIGHT + LAYOUT_MARGIN * 2);
  const rowGap = tiers.length > 1 ? (height - LAYOUT_MARGIN * 2) / (tiers.length - 1) : 0;

  const positions = new Map();
  tiers.forEach((tier, index) => {
    tier.forEach((node) => {
      positions.set(node.name, {
        x: Math.round(xs.get(node.name) + offset),
        y: Math.round(LAYOUT_MARGIN + index * rowGap)
      });
    });
  });
  return { positions, width, height };
}

function topologyFromState(snapshot) {
  const layout = autoLayout(snapshot.nodes, snapshot.links);
  const devices = snapshot.nodes.map((node) => deviceFromState({
    ...node,
    position: node.position || layout.positions.get(node.name)
  }));
  const links = snapshot.links.map((link, index) => ({
    id: `${link.from}-${link.to}-${index}`,
    from: link.from,
    to: link.to,
    fromPort: link.fromInterface,
    toPort: link.toInterface,
    cost: link.cost,
    up: link.up !== false
  }));
  return {
    name: snapshot.name,
    scenario: snapshot.name,
    description: snapshot.description,
    summary: snapshot.summary,
    defaultSource: snapshot.defaultSource,
    defaultDestination: snapshot.defaultDestination,
    canvas: snapshot.canvas || { width: layout.width, height: layout.height },
    devices,
    links
  };
}

function routingTableRow(item) {
  const source = item.source || "?";
  const direct = item.direct === true || item.direct === "true" || !item.gateway;
  const gateway = direct ? "connected" : item.gateway;
  const iface = item.interface || "local";
  return {
    key: `${item.destination}/${item.mask}|${source}`,
    primary: `${source} ${item.destination}/${item.mask}`,
    secondary: `${gateway} via ${iface} · AD ${item.adminDistance ?? "—"} · metric ${item.metric ?? "—"}`,
    data: item
  };
}

function arpTableRow(item) {
  const pending = item.pending === true || item.pending === "true";
  return {
    key: item.ip,
    primary: item.ip,
    secondary: `${pending ? "pending" : item.mac || "—"} via ${item.interface || "—"}`,
    data: item
  };
}

function macTableRow(item) {
  return {
    key: `${String(item.mac).toLowerCase()}|${item.vlan}`,
    primary: item.mac,
    secondary: `${item.interface || "—"} · VLAN ${item.vlan}`,
    data: item
  };
}

function stpPortRow(item) {
  const blocksData = item.state !== "forwarding";
  return {
    key: item.interface,
    primary: item.interface,
    secondary: `${item.role} · ${item.state} · cost ${item.pathCost} · port ${item.portId}`,
    tone: blocksData ? "stp-blocked" : "stp-forwarding",
    data: item
  };
}

function eventTableRow(reference) {
  if (!reference?.entry) return null;
  if (reference.kind === "routing") return routingTableRow(reference.entry);
  if (reference.kind === "arp") return arpTableRow(reference.entry);
  if (reference.kind === "mac") return macTableRow(reference.entry);
  return null;
}

function deviceFromState(node) {
  const nodeInterfaces = node.interfaces || [];
  const nodeVlanInterfaces = node.vlanInterfaces || [];
  const interfaces = nodeInterfaces.map((item) => {
    const allowedVlans = item.allowedVlans || [];
    const mode = item.mode === "access"
      ? `VLAN ${item.accessVlan}`
      : item.mode === "trunk"
        ? `trunk · native ${item.nativeVlan} · allowed ${allowedVlans.join(", ") || "none"}`
        : "routed";
    return {
      name: item.name,
      address: item.ip ? `${item.ip}/${item.mask}` : "",
      mode,
      up: item.up !== false
    };
  });
  const vlans = new Set(nodeVlanInterfaces.map((item) => Number(item.vlan)));
  nodeInterfaces.forEach((item) => {
    if (item.mode === "access" && item.accessVlan) vlans.add(Number(item.accessVlan));
    if (item.mode !== "trunk") return;
    if (item.nativeVlan) vlans.add(Number(item.nativeVlan));
    (item.allowedVlans || []).forEach((vlan) => vlans.add(Number(vlan)));
  });
  const vlanInterfaces = nodeVlanInterfaces.map((item) => `${item.vlan} · ${item.ip}/${item.mask}`);
  const destinationAddresses = [
    ...nodeInterfaces.filter((item) => item.ip).map((item) => ({
      interface: item.name,
      ip: item.ip,
      mask: item.mask
    })),
    ...nodeVlanInterfaces.filter((item) => item.ip).map((item) => ({
      interface: `VLAN ${item.vlan}`,
      ip: item.ip,
      mask: item.mask
    })),
    ...(node.loopback ? [{ interface: "loopback", ip: node.loopback, mask: null }] : [])
  ];
  const routes = (node.routes || []).map(routingTableRow);
  const arp = (node.arp || []).map(arpTableRow);
  const mac = (node.mac || []).map(macTableRow);
  const lldpNeighbors = node.lldp || [];
  const lldp = lldpNeighbors.map((item) => `${item.systemName || "neighbor"} ${item.port} → ${item.localInterface}`);
  const rawSTP = node.stp || {};
  const stpPorts = rawSTP.ports || [];
  const stp = {
    enabled: Boolean(rawSTP.enabled),
    priority: Number(rawSTP.priority ?? 32768),
    bridgeId: rawSTP.bridgeId || "",
    rootId: rawSTP.rootId || "",
    isRoot: Boolean(rawSTP.isRoot),
    rootPathCost: Number(rawSTP.rootPathCost || 0),
    rootPort: rawSTP.rootPort || "",
    ports: stpPorts,
    portRows: stpPorts.map(stpPortRow)
  };
  const type = node.type;
  return {
    name: node.name,
    type,
    role: type === "switch" ? "Network switch" : type === "router" ? "Router" : "Host",
    loopback: node.loopback || "",
    position: node.position,
    interfaces,
    vlans: [...vlans].sort((left, right) => left - right),
    vlanInterfaces,
    destinationAddresses,
    routes,
    arp,
    mac,
    lldp,
    lldpNeighbors,
    lldpEnabled: Boolean(node.lldpEnabled),
    stp,
  };
}

function updateEndpointOptions() {
  const previousSource = elements.sourceNode.value;
  const previousDestination = elements.destinationNode.value.trim();
  // Only addressable devices can source or answer a ping; offering the rest guarantees a failed trace.
  const endpoints = state.topology.devices.filter((device) => device.destinationAddresses?.length);
  elements.sourceNode.innerHTML = endpoints
    .map((device) => `<option value="${escapeHtml(device.name)}">${escapeHtml(device.name)}</option>`)
    .join("");
  const destinationValues = new Set();
  elements.destinationOptions.innerHTML = endpoints.map((device) => {
    destinationValues.add(device.name);
    const deviceOption = `<option value="${escapeHtml(device.name)}" label="${escapeHtml(`${device.name} · device`)}"></option>`;
    const addressOptions = (device.destinationAddresses || []).map((address) => {
      destinationValues.add(address.ip);
      const prefix = address.mask == null ? address.ip : `${address.ip}/${address.mask}`;
      const label = `${device.name} · ${address.interface} · ${prefix}`;
      return `<option value="${escapeHtml(address.ip)}" label="${escapeHtml(label)}"></option>`;
    }).join("");
    return `${deviceOption}${addressOptions}`;
  }).join("");
  const hosts = endpoints.filter((device) => device.type === "host");
  elements.sourceNode.value = endpoints.some((device) => device.name === state.topology.defaultSource)
    ? state.topology.defaultSource
    : endpoints.some((device) => device.name === previousSource) ? previousSource : hosts[0]?.name || endpoints[0]?.name || "";
  elements.destinationNode.value = destinationValues.has(state.topology.defaultDestination) && state.topology.defaultDestination !== elements.sourceNode.value
    ? state.topology.defaultDestination
    : destinationValues.has(previousDestination) && previousDestination !== elements.sourceNode.value
    ? previousDestination
    : hosts.find((device) => device.name !== elements.sourceNode.value)?.name || endpoints.find((device) => device.name !== elements.sourceNode.value)?.name || "";
}

function renderTracerouteHops() {
  const visible = elements.traceType.value === "traceroute" && state.tracerouteHops.length > 0;
  elements.tracerouteSummary.hidden = !visible;
  elements.tracerouteHopList.innerHTML = visible ? state.tracerouteHops.map((hop) => {
    const address = hop.timedOut ? "*" : hop.address || "*";
    const device = !hop.timedOut && state.topology.devices.find((candidate) =>
      candidate.destinationAddresses?.some((candidateAddress) => candidateAddress.ip === hop.address)
    );
    const statusClass = hop.reached ? "is-reached" : hop.timedOut ? "is-timeout" : "";
    const reason = String(hop.reason || (hop.timedOut ? "timeout" : "hop discovered")).replaceAll("_", " ");
    return `<li class="traceroute-hop ${statusClass}" title="TTL ${escapeHtml(hop.ttl)} · ${escapeHtml(reason)}">
      <span class="traceroute-hop-number">${escapeHtml(hop.ttl)}</span>
      ${device ? `<span class="traceroute-hop-device">${escapeHtml(device.name)}</span>` : ""}
      <span class="traceroute-hop-address">${escapeHtml(address)}</span>
    </li>`;
  }).join("") : "";
}

function clearTracerouteHops() {
  state.tracerouteHops = [];
  renderTracerouteHops();
}

function isValidIPv4(value) {
  const octets = value.split(".");
  return octets.length === 4 && octets.every((octet) => {
    if (!/^\d{1,3}$/.test(octet) || (octet.length > 1 && octet.startsWith("0"))) return false;
    return Number(octet) <= 255;
  });
}

function isKnownDestination(value) {
  return state.topology.devices.some((device) => device.name === value && device.destinationAddresses?.length);
}

function matchingLink(from, to) {
  return state.topology.links.find((link) => (link.from === from && link.to === to) || (link.from === to && link.to === from));
}

function matchingInterfaceLink(nodeName, interfaceName) {
  return state.topology.links.find((link) =>
    (link.from === nodeName && link.fromPort === interfaceName)
    || (link.to === nodeName && link.toPort === interfaceName)
  );
}

function lldpDiscoveryKey(linkId, nodeName) {
  return `${linkId}\u0000${nodeName}`;
}

function discoveredLinkIds() {
  return [...new Set(Array.from(state.lldpDiscoveries, (key) => key.split("\u0000", 1)[0]))];
}

function neighborMatchesLink(device, neighbor, link) {
  if (device.name === link.from) {
    return neighbor.localInterface === link.fromPort && neighbor.systemName === link.to && neighbor.port === link.toPort;
  }
  if (device.name === link.to) {
    return neighbor.localInterface === link.toPort && neighbor.systemName === link.from && neighbor.port === link.fromPort;
  }
  return false;
}

function syncLLDPDiscoveriesFromTopology() {
  const discoveries = new Set();
  state.topology.devices.forEach((device) => {
    (device.lldpNeighbors || []).forEach((neighbor) => {
      const link = state.topology.links.find((item) => neighborMatchesLink(device, neighbor, item));
      if (link?.up) discoveries.add(lldpDiscoveryKey(link.id, device.name));
    });
  });
  state.lldpDiscoveries = discoveries;
}

function matchingLLDPEventLink(event) {
  const fields = event.fields || {};
  const localNode = event.node;
  const localInterface = event.interface || fields.interface;
  const peerName = fields.systemName;
  const peerPort = fields.portId;
  if (!localNode || !localInterface) return null;
  return state.topology.links.find((link) => {
    if (link.from === localNode && link.fromPort === localInterface) {
      return (!peerName || link.to === peerName) && (!peerPort || link.toPort === peerPort);
    }
    if (link.to === localNode && link.toPort === localInterface) {
      return (!peerName || link.from === peerName) && (!peerPort || link.fromPort === peerPort);
    }
    return false;
  }) || null;
}

function updateLLDPDiscoveriesFromEvent(event) {
  if (event.protocol !== "LLDP") return false;
  if (event.action === "lldp_disabled") {
    const previousSize = state.lldpDiscoveries.size;
    state.lldpDiscoveries = new Set(Array.from(state.lldpDiscoveries).filter((key) => key.split("\u0000")[1] !== event.node));
    return state.lldpDiscoveries.size !== previousSize;
  }
  if (!["lldp_neighbor_discovered", "lldp_neighbor_refreshed", "lldp_neighbor_removed", "lldp_neighbor_expired"].includes(event.action)) {
    return false;
  }
  const link = matchingLLDPEventLink(event);
  if (!link) return false;
  const key = lldpDiscoveryKey(link.id, event.node);
  if (event.action === "lldp_neighbor_discovered" || event.action === "lldp_neighbor_refreshed") {
    const previousSize = state.lldpDiscoveries.size;
    state.lldpDiscoveries.add(key);
    return state.lldpDiscoveries.size !== previousSize;
  }
  return state.lldpDiscoveries.delete(key);
}

function displayFieldName(name) {
  return name
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/^./, (letter) => letter.toUpperCase());
}

function describeDrop(event) {
  const fields = event.fields || {};
  const node = event.node || event.from || "This device";
  const destination = fields.destinationIp || fields.destinationMac || "the destination";
  const descriptions = {
    arp_resolution_timeout: `${node} dropped the frame because ARP could not resolve ${fields.destinationIp || "the next hop"} before the pending-queue deadline.`,
    bad_header_checksum: `${node} discarded the packet because its IPv4 header checksum is invalid. The packet was corrupted before or while reaching this device.`,
    destination_mac_not_accepted: `${node} dropped the frame because destination MAC ${fields.destinationMac || "—"} is neither its interface MAC nor a broadcast address.`,
    egress_matches_ingress: `${node} dropped the frame because its VLAN ${fields.vlan || "—"} MAC-table lookup points back to the ingress interface ${event.interface || "—"}.`,
    frame_too_small: `${node} dropped the frame because it is shorter than the 14-byte Ethernet header.`,
    invalid_ethernet_header: `${node} dropped the frame because the Ethernet header could not be parsed.`,
    invalid_lldp_frame: `${node} dropped the frame because its LLDP data is malformed or incomplete.`,
    lldp_disabled: `${node} ignored the LLDP frame because LLDP is disabled locally.`,
    no_eligible_egress: `${node} dropped the flooded frame because no other Layer 2 interface permits VLAN ${fields.vlan || "—"}.`,
    no_egress_interface: `${node} matched a route to ${destination}, but its configured outgoing interface is unavailable.`,
    no_route: `${node} dropped the packet because its routing table has no matching route for ${destination}.`,
    protocol_not_implemented: `${node} dropped the frame because ${event.protocol || "this protocol"} processing is not implemented.`,
    transport_error: `${node} dropped the frame because the link transport failed between ${event.from || "the sender"} and ${event.to || "the peer"}: ${fields.error || "delivery failed"}.`,
    link_down: `${node} dropped the frame at ${event.from || "the sender"} because the link to ${event.to || "the peer"} is down.`,
    ttl_expired: `${node} dropped the packet to ${destination} because its TTL reached zero before forwarding.`,
    stp_port_not_forwarding: `${node} did not forward the frame on ${event.interface || "the selected interface"} because spanning tree has that port in ${fields.stpState || "a non-forwarding state"}.`,
    unsupported_ethertype: `${node} dropped the frame because EtherType ${fields.etherType || "—"} is unsupported.`,
    vlan_not_allowed: `${node} dropped the frame because VLAN ${fields.vlan || "—"} is not permitted on interface ${event.interface || "—"}.`
  };
  return descriptions[fields.reason]
    || (fields.reason
      ? `${node} dropped the ${event.action === "packet_dropped" ? "packet" : "frame"}: ${fields.reason.replaceAll("_", " ")}.`
      : `${node} dropped the frame because the link transport could not deliver it from ${event.from || "the sender"} to ${event.to || "the peer"}.`);
}

function describeSimulationDecision(event) {
  const fields = event.fields || {};
  const node = event.node || event.from || "The simulator";
  const iface = fields.egressInterface || fields.interface || event.interface || "the selected interface";

  if (event.action === "frame_dropped" || event.action === "packet_dropped") {
    return { label: "Precise drop reason", text: describeDrop(event) };
  }

  if (event.action === "route_selected") {
    const destination = fields.destinationIp || "the destination";
    const direct = fields.nextHop === destination;
    const nextHop = direct ? `the directly connected destination ${destination}` : `gateway ${fields.nextHop || "—"}`;
    const routeSources = { C: "connected", S: "static", R: "RIP", O: "OSPF", I: "IS-IS", B: "BGP", i: "iBGP" };
    const source = fields.routeSource ? ` ${routeSources[fields.routeSource] || fields.routeSource} route` : " route";
    const ttl = fields.ttlBefore && fields.ttlAfter
      ? ` TTL changes from ${fields.ttlBefore} to ${fields.ttlAfter} at this routing hop.`
      : "";
    return {
      label: "Selected route and gateway",
      text: `${node} selected${source} ${fields.route || "—"} for ${destination}; send via ${fields.egressInterface || fields.interface || iface} to ${nextHop}.${ttl}`
    };
  }

  const arpDescriptions = {
    arp_lookup_missed: `${node} found no complete ARP entry for ${fields.targetIp || "the next hop"} on ${iface}; it must create a pending entry and resolve the address.`,
    arp_entry_selected: `${node} used ARP entry ${fields.ip || "—"} → ${fields.mac || "—"} on ${iface} to build the Ethernet frame.`,
    arp_request_started: `${node} needs the MAC address for ${fields.targetIp || "the next hop"}; no usable ARP entry was available, so it broadcast a request on ${iface}.`,
    arp_request_retried: `${node} still has no MAC address for ${fields.targetIp || "the next hop"}; it sent ARP request ${fields.attempt || "—"} of ${fields.maxAttempts || "—"} on ${iface}.`,
    arp_resolution_failed: `${node} could not resolve ${fields.targetIp || "the next hop"} after ${fields.attempts || "the configured number of"} request(s); ${fields.pendingPackets || "the"} queued packet(s) were notified and dropped.`,
    arp_request_received: `${fields.targetIp || "The requested IP"} matches a local address on ${node}; it will answer ${fields.senderIp || "the requester"} (${fields.senderMac || "unknown MAC"}) on ${iface}.`,
    arp_reply_created: `${node} replied that ${fields.senderIp || "the requested IP"} is at ${fields.senderMac || "its interface MAC"}, sending the result to ${fields.receiverIp || "the requester"} on ${iface}.`,
    arp_resolved: `${node} resolved ${fields.ip || "the ARP target"} to ${fields.mac || "—"} on ${iface}; the cache was ${fields.cacheState || "updated"}${fields.pendingPackets ? ` and ${fields.pendingPackets} queued packet(s) were waiting` : ""}.`,
    pending_packets_released: `${node} released ${fields.count || "the"} packet(s) queued for ${fields.targetIp || "the resolved next hop"} after ARP resolution completed.`
  };
  if (arpDescriptions[event.action]) {
    return { label: "ARP target and result", text: arpDescriptions[event.action] };
  }

  if (event.action === "inter_vlan_route_selected") {
    const ttl = fields.ttlBefore && fields.ttlAfter
      ? ` Routing decrements TTL from ${fields.ttlBefore} to ${fields.ttlAfter}.`
      : "";
    return {
      label: "VLAN forwarding decision",
      text: `${node} received ${fields.sourceIp || "the packet"} → ${fields.destinationIp || "the destination"} on ${fields.ingressInterface || event.interface || "the ingress"} in VLAN ${fields.sourceVlan || "—"}. It selected ${fields.route || "the matching route"} through ${fields.egressInterface || `VLAN ${fields.destinationVlan || "—"}`}.${ttl}`
    };
  }
  if (event.action === "inter_vlan_forwarding_started") {
    return {
      label: "VLAN forwarding decision",
      text: `${node} is forwarding ${fields.destinationIp || "the packet"} from VLAN ${fields.sourceVlan || "—"} into VLAN ${fields.destinationVlan || "—"} through ${iface}; Layer 2 delivery will use ARP for the next hop.`
    };
  }

  if (event.action === "frame_flooding_completed") {
    return {
      label: "Broadcast forwarding result",
      text: `${node} processed the VLAN ${fields.vlan || "—"} broadcast locally. No additional eligible ports existed, so no extra copies were needed; this is not a packet failure.`
    };
  }

  if (event.action === "mac_table_updated") {
    return {
      label: "MAC-table decision",
      text: `${node} learned source MAC ${fields.mac || "—"} on ${fields.interface || iface} in VLAN ${fields.vlan || "—"}; future matching frames can use this unicast egress.`
    };
  }
  if (event.action === "frame_forwarding_started") {
    return {
      label: "MAC-table decision",
      text: `${node} found destination MAC ${fields.destinationMac || "—"} in the VLAN ${fields.vlan || "—"} MAC table; forward from ${fields.ingress || "the ingress"} only through ${iface}, where that VLAN is allowed.`
    };
  }
  if (event.action === "frame_flooding_started") {
    const reason = fields.reason === "broadcast_destination"
      ? `destination MAC ${fields.destinationMac || "ff:ff:ff:ff:ff:ff"} is broadcast`
      : `destination MAC ${fields.destinationMac || "—"} has no VLAN ${fields.vlan || "—"} MAC-table entry`;
    return {
      label: "MAC-table flooding reason",
      text: `${node} is flooding within VLAN ${fields.vlan || "—"}, except ingress ${fields.ingress || iface}, because ${reason}. Only ports that allow this VLAN are eligible.`
    };
  }

  if (event.action === "packet_forwarding_started") {
    return {
      label: "Forwarding decision",
      text: `${node} is forwarding toward ${fields.destinationIp || "the destination"} through ${fields.interface || iface} via ${fields.nextHop || "the directly connected target"}; TTL is now ${fields.ttl || "—"}.`
    };
  }

  const eventDescriptions = {
    ero_ping_started: `${node} created an explicit-route ping to ${fields.destinationIp || "the destination"} through tunnel endpoint ${fields.eroIp || "—"}.`,
    frame_sent: fields.sourceIp
      ? `${event.from} sent ${String(fields.icmpType || event.protocol).replaceAll("_", " ")} to ${event.to} on ${event.sourceInterface || event.interface || "—"}. IP stays ${fields.sourceIp} → ${fields.destinationIp}; this hop uses Ethernet ${fields.sourceMac || "—"} → ${fields.destinationMac || "—"} with TTL ${fields.ttl || "—"}.`
      : `${event.from} transmitted the ${event.protocol} frame on ${event.sourceInterface || event.interface || "—"} over the connected link to ${event.to} ${event.destinationInterface || event.peerInterface || ""}.`,
    frame_received: fields.sourceIp
      ? `${event.to} received ${String(fields.icmpType || event.protocol).replaceAll("_", " ")} from ${event.from} on ${event.destinationInterface || event.interface || "—"}; IP ${fields.sourceIp} → ${fields.destinationIp}, Ethernet ${fields.sourceMac || "—"} → ${fields.destinationMac || "—"}, TTL ${fields.ttl || "—"}.`
      : `${event.to} accepted the ${event.protocol} frame from ${event.from} on ${event.destinationInterface || event.interface || "—"} for local Layer 2 processing.`,
    ping_started: `${event.from || node} created an ICMP echo request for ${fields.destinationIp || event.to || "the destination"}; this run records the forwarding decisions that follow.`,
    ping_reply_received: `${event.to || node} received the ICMP echo reply from ${event.from || "the destination"}; the trace succeeded.`,
    ping_no_reply: `${event.to || node} received no ICMP echo reply from ${event.from || "the destination"}; inspect the preceding drop or forwarding event for the cause.`,
    traceroute_started: `${node} started tracing the route to ${fields.destinationIp || "the destination"}, with a limit of ${fields.maxHops || "30"} hops.`,
    traceroute_probe_started: `${node} created an ICMP echo probe to ${fields.destinationIp || "the destination"} with TTL ${fields.ttl || "—"}.`,
    traceroute_hop_discovered: `${fields.address || "A router"} returned ICMP ${String(fields.reason || "time exceeded").replaceAll("_", " ")} for probe TTL ${fields.ttl || "—"}.`,
    traceroute_probe_timed_out: `Probe TTL ${fields.ttl || "—"} received no ICMP response, so this hop is shown as an asterisk.`,
    traceroute_destination_reached: `${node} received the destination echo reply from ${fields.address || fields.destinationIp || "the destination"} at hop ${fields.ttl || "—"}.`,
    traceroute_completed: `${node} completed traceroute after ${fields.hops || "—"} hop${fields.hops === "1" ? "" : "s"}; destination reached: ${fields.reached || "false"}.`,
    icmp_echo_request_received: `${node} owns destination IP ${fields.destinationIp || "—"}, so it delivered the echo request locally instead of routing it onward.`,
    icmp_echo_reply_created: `${node} created an ICMP echo reply addressed to ${fields.destinationIp || "the request source"}.`,
    icmp_echo_reply_received: `${node} received the echo reply from ${fields.sourceIp || "the destination"} on ${iface} with TTL ${fields.ttl || "—"}.`,
    icmp_error_created: `${node} created an ICMP ${String(fields.icmpType || "error").replaceAll("_", " ")} report for ${fields.destinationIp || "the original sender"}.`,
    icmp_error_received: `${node} received ICMP ${String(fields.icmpType || "error").replaceAll("_", " ")} from ${fields.sourceIp || "a router"} for traffic to ${fields.destinationIp || "the destination"}.`,
    lldp_enabled: `${node} enabled LLDP and can now advertise local identity and learn directly connected neighbors.`,
    lldp_disabled: `${node} disabled LLDP and withdrew its active neighbor advertisements.`,
    lldp_advertisement_created: `${node} advertised its LLDP identity on ${iface} with TTL ${fields.ttl || "—"} seconds.`,
    lldp_shutdown_advertisement_created: `${node} sent a zero-TTL LLDP advertisement on ${iface}, instructing the peer to remove this neighbor.`,
    lldp_frame_ignored: `${node} ignored the LLDP frame on ${iface} because LLDP is disabled locally.`,
    lldp_neighbor_discovered: `${node} learned LLDP neighbor ${fields.systemName || fields.chassisId || "—"}, port ${fields.portId || "—"}, on ${iface} for ${fields.ttl || "—"} seconds.`,
    lldp_neighbor_refreshed: `${node} refreshed LLDP neighbor ${fields.systemName || fields.chassisId || "—"}, port ${fields.portId || "—"}, on ${iface} for another ${fields.ttl || "—"} seconds.`,
    lldp_neighbor_removed: `${node} removed LLDP neighbor ${fields.chassisId || "—"}, port ${fields.portId || "—"}, after receiving its shutdown advertisement.`,
    lldp_neighbor_expired: `${node} removed LLDP neighbor ${fields.systemName || fields.chassisId || "—"} on ${fields.interface || iface} because its advertised lifetime expired.`,
    rip_enabled: `${node} enabled RIP route exchange.`,
    rip_disabled: `${node} disabled RIP and stopped exchanging dynamic routes.`,
    rip_update_received: `${node} received ${fields.entries || "an unknown number of"} RIP route entries from ${fields.sourceIp || "a neighbor"} on ${iface}.`,
    rip_route_learned: `${node} installed RIP route ${fields.route || "—"} via ${fields.nextHop || "—"} on ${fields.interface || iface} with metric ${fields.metric || "—"}.`,
    rip_route_refreshed: `${node} refreshed RIP route ${fields.route || "—"} via ${fields.nextHop || "—"}; its metric is ${fields.metric || "—"}.`,
    rip_route_expired: `${node} marked RIP route ${fields.route || "—"} unreachable${fields.metric ? ` at metric ${fields.metric}` : ""} because it expired or its neighbor advertised infinity.`,
    rip_route_removed: `${node} removed expired RIP route ${fields.route || "—"} after the garbage-collection interval.`
  };
  if (eventDescriptions[event.action]) {
    return { label: "Event result", text: eventDescriptions[event.action] };
  }

  const details = Object.entries(fields).map(([key, value]) => `${displayFieldName(key).toLowerCase()} ${value}`).join(", ");
  return {
    label: "Protocol decision",
    text: `${node} performed ${event.action.replaceAll("_", " ")}${details ? `: ${details}` : ""}.`
  };
}

function normalizeSimulationEvent(event) {
  const actionLabels = {
    ping_started: ["ICMP echo request created", "Started"],
    ping_reply_received: ["ICMP echo reply received", "Success"],
    ping_no_reply: ["ICMP echo completed without a reply", "No reply"],
    traceroute_started: ["Traceroute started", "Started"],
    traceroute_probe_started: ["Traceroute probe created", "Probing"],
    traceroute_hop_discovered: ["Traceroute hop discovered", "Hop found"],
    traceroute_probe_timed_out: ["Traceroute probe timed out", "No response"],
    traceroute_destination_reached: ["Traceroute destination reached", "Reached"],
    traceroute_completed: ["Traceroute completed", "Complete"],
    frame_sent: [`${event.protocol} frame sent`, "Sent"],
    frame_received: [`${event.protocol} frame received`, "Received"],
    frame_dropped: [`${event.protocol} frame dropped`, "Dropped"],
    frame_forwarding_started: [`${event.protocol} frame forwarding started`, "Forwarding"],
    frame_flooding_started: [`${event.protocol} frame flooding started`, "Flooding"],
    frame_flooding_completed: [`${event.protocol} broadcast processed`, "Complete"],
    mac_table_updated: ["MAC table updated", "Updated"],
    arp_request_started: ["ARP request started", "Requesting"],
    arp_request_retried: ["ARP request retried", "Retrying"],
    arp_resolution_failed: ["ARP resolution failed", "Timed out"],
    arp_lookup_missed: ["ARP cache lookup missed", "No match"],
    arp_entry_selected: ["ARP cache entry selected", "Matched"],
    arp_request_received: ["ARP request matched a local address", "Matched"],
    arp_reply_created: ["ARP reply created", "Created"],
    arp_resolved: ["ARP cache resolved", "Learned"],
    pending_packets_released: ["Queued packets released", "Released"],
    route_selected: ["Route selected", "Matched"],
    packet_forwarding_started: [`${event.protocol} packet forwarding started`, "Routing"],
    packet_dropped: [`${event.protocol} packet dropped`, "Dropped"],
    inter_vlan_route_selected: ["Inter-VLAN route selected", "Matched"],
    inter_vlan_forwarding_started: ["Inter-VLAN forwarding started", "Routing"],
    icmp_echo_request_received: ["ICMP echo request received", "Delivered"],
    icmp_echo_reply_created: ["ICMP echo reply created", "Created"],
    icmp_echo_reply_received: ["ICMP echo reply received", "Success"],
    icmp_error_created: ["ICMP error report created", "Reporting"],
    icmp_error_received: ["ICMP error report received", "Failed"],
    ero_ping_started: ["Explicit-route ping started", "Started"],
    rip_enabled: ["RIP enabled", "Enabled"],
    rip_disabled: ["RIP disabled", "Disabled"],
    rip_update_received: ["RIP update received", "Received"],
    rip_route_learned: ["RIP route learned", "Installed"],
    rip_route_refreshed: ["RIP route refreshed", "Refreshed"],
    rip_route_expired: ["RIP route expired", "Expired"],
    rip_route_removed: ["RIP route removed", "Removed"],
    lldp_enabled: ["LLDP enabled", "Enabled"],
    lldp_disabled: ["LLDP disabled", "Disabled"],
    lldp_advertisement_created: ["LLDP advertisement created", "Created"],
    lldp_shutdown_advertisement_created: ["LLDP shutdown advertisement created", "Withdrawn"],
    lldp_frame_ignored: ["LLDP frame ignored", "Ignored"],
    lldp_neighbor_discovered: ["LLDP neighbor discovered", "Discovered"],
    lldp_neighbor_refreshed: ["LLDP neighbor refreshed", "Refreshed"],
    lldp_neighbor_removed: ["LLDP neighbor removed", "Removed"],
    lldp_neighbor_expired: ["LLDP neighbor expired", "Expired"]
  };
  const [title, result] = actionLabels[event.action] || [event.action.replaceAll("_", " "), "Observed"];
  const explanation = describeSimulationDecision(event);
  const link = event.from && event.to ? matchingLink(event.from, event.to) : null;
  const tracerouteResultEvent = [
    "traceroute_hop_discovered",
    "traceroute_probe_timed_out",
    "traceroute_destination_reached"
  ].includes(event.action);
  const tracerouteDevice = tracerouteResultEvent && event.fields?.address
    ? state.topology.devices.find((device) =>
      device.destinationAddresses?.some((address) => address.ip === event.fields.address)
    )
    : null;
  const sourceInterface = event.sourceInterface
    || (event.action === "frame_received" ? event.peerInterface : event.interface);
  const destinationInterface = event.destinationInterface
    || (event.action === "frame_received" ? event.interface : event.peerInterface);
  const hasOutgoingInterface = Boolean(event.interface && event.fields?.interface);
  const decisionFields = Object.fromEntries(Object.entries(event.fields || {}).map(([key, value]) => [
    key === "interface" && hasOutgoingInterface ? "Outgoing interface" : key,
    value
  ]));
  const fields = {
    Action: event.action,
    ...(event.from && event.to && sourceInterface ? { "Source interface": sourceInterface } : {}),
    ...(event.from && event.to && destinationInterface ? { "Destination interface": destinationInterface } : {}),
    ...(!(event.from && event.to) && event.interface ? { [hasOutgoingInterface ? "Ingress interface" : "Interface"]: event.interface } : {}),
    ...(event.size ? { "Frame size": `${event.size} bytes` } : {}),
    ...decisionFields
  };
  const path = tracerouteResultEvent
    ? event.action === "traceroute_probe_timed_out"
      ? `TTL ${event.fields?.ttl || "—"} · no reply`
      : `${tracerouteDevice?.name || "Hop"} · ${event.fields?.address || "unknown address"}`
    : event.from && event.to
    ? `${event.from}${sourceInterface ? ` ${sourceInterface}` : ""} → ${event.to}${destinationInterface ? ` ${destinationInterface}` : ""}`
    : event.node || state.topology.name;
  const location = tracerouteDevice
    ? { node: tracerouteDevice.name }
    : link
    ? {
        from: event.from,
        to: event.to,
        progress: event.action === "frame_received" ? 0.9
          : event.action === "frame_dropped" || event.action === "packet_dropped" ? 0.5 : 0.7
      }
    : { node: event.node };
  return {
    time: event.time || `#${String(event.sequence).padStart(6, "0")}`,
    protocol: event.action === "frame_dropped" || event.action === "packet_dropped" ? "DROP" : event.protocol,
    filterProtocol: event.protocol,
    path,
    title,
    result,
    device: tracerouteDevice?.name || event.node,
    location,
    activeLinks: link ? [link.id] : [],
    discoveredLinks: discoveredLinkIds(),
    decisionLabel: explanation.label,
    decision: explanation.text,
    fields,
    tableReference: event.table || null
  };
}

const CUSTOMER_TRACE_ACTIONS = new Set([
  "arp_resolution_failed",
  "icmp_echo_reply_created",
  "icmp_echo_request_received",
  "icmp_error_created",
  "icmp_error_received",
  "inter_vlan_route_selected",
  "packet_dropped",
  "ping_no_reply",
  "ping_reply_received",
  "ping_started",
  "route_selected",
  "traceroute_completed",
  "traceroute_destination_reached",
  "traceroute_hop_discovered",
  "traceroute_probe_started",
  "traceroute_probe_timed_out",
  "traceroute_started"
]);

const TRACEROUTE_SUMMARY_ACTIONS = new Set([
  "traceroute_started",
  "traceroute_hop_discovered",
  "traceroute_probe_timed_out",
  "traceroute_destination_reached",
  "traceroute_completed"
]);

function customerTraceEvents(events) {
  if (events.some((event) => event.action === "traceroute_started")) {
    return events.filter((event) => TRACEROUTE_SUMMARY_ACTIONS.has(event.action));
  }
  if (!events.some((event) => event.action === "ping_started" || event.action === "traceroute_started")) return events;
  return events.filter((event) => {
    if (CUSTOMER_TRACE_ACTIONS.has(event.action)) return true;
    if (event.action === "frame_dropped") return event.fields?.reason !== "no_eligible_egress";
    if (event.action === "frame_sent") return event.protocol === "ICMP";
    if (event.action === "arp_resolved") return Number(event.fields?.pendingPackets || 0) > 0;
    return false;
  });
}

function syncTimelineModeButtons() {
  elements.timelineModeButtons.forEach((button) => {
    const active = button.dataset.timelineMode === state.timelineMode;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-pressed", String(active));
    button.disabled = !state.capturedEvents.length || state.capturingTrace || state.loadingScenario;
  });
  elements.timelineSubtitle.textContent = state.timelineMode === "SUMMARY"
    ? "Important troubleshooting decisions, in wire order"
    : "Every protocol and forwarding event, in wire order";
}

function applyTimelineMode() {
  const events = state.timelineMode === "DETAILS"
    ? state.capturedEvents
    : customerTraceEvents(state.capturedEvents);
  state.rawEvents = events;
  state.events = events.map(normalizeSimulationEvent);
  syncTimelineProgress();
  syncTimelineModeButtons();
}

function syncTimelineProgress() {
  state.selectedEvent = -1;
  state.revealedThrough = -1;
  if (state.playbackIndex < 0) return;
  const capturedPositions = new Map(state.capturedEvents.map((event, index) => [event, index]));
  state.rawEvents.forEach((event, index) => {
    const capturedPosition = capturedPositions.get(event);
    if (capturedPosition <= state.playbackIndex) {
      state.revealedThrough = index;
      if (eventMatchesFilter(state.events[index])) state.selectedEvent = index;
    }
  });
}

function setTimelineMode(mode) {
  if (!state.capturedEvents.length || !["SUMMARY", "DETAILS"].includes(mode) || mode === state.timelineMode) return;
  state.timelineMode = mode;
  applyTimelineMode();
  renderTimeline();
  updatePlaybackControls();
  showNotice(mode === "SUMMARY"
    ? `Showing ${state.events.length} high-value troubleshooting events.`
    : `Showing all ${state.events.length} protocol events.`);
}

function useCapturedEvents(events = [], { failed = false } = {}) {
  state.capturedEvents = events;
  state.playbackEvents = events.map(normalizeSimulationEvent);
  state.playbackIndex = -1;
  events.forEach(updateLLDPDiscoveriesFromEvent);
  updateTraceOutcome(events, failed);
  applyTimelineMode();
}

function clearTraceOutcome() {
  state.tracePathLinks = new Set();
  state.traceDropEvent = null;
  renderTraceOutcome();
}

function updateTraceOutcome(events, failed) {
  state.tracePathLinks = new Set();
  const traversals = events.filter((event) => event.action === "frame_received" && event.from && event.to);
  const packetTraversals = traversals.filter((event) => event.protocol === "ICMP" || event.protocol === "IPIP");
  (packetTraversals.length ? packetTraversals : traversals).forEach((event) => {
    const link = matchingLink(event.from, event.to);
    if (link) state.tracePathLinks.add(link.id);
  });
  const prioritizedDrops = failed ? [
    events.filter((event) => event.action === "frame_dropped" && event.fields?.reason === "link_down"),
    events.filter((event) => event.action === "packet_dropped"),
    events.filter((event) => event.action === "frame_dropped" && event.fields?.reason !== "no_eligible_egress"),
    events.filter((event) => event.action === "arp_resolution_failed")
  ] : [];
  const drops = prioritizedDrops.find((candidates) => candidates.length) || [];
  const drop = drops[drops.length - 1];
  state.traceDropEvent = drop
    ? { event: normalizeSimulationEvent(drop), rawEvent: drop }
    : null;
  renderTraceOutcome();
}

function renderTraceOutcome() {
  document.querySelectorAll(".link-line").forEach((line) => {
    line.classList.toggle("is-trace-path", state.tracePathLinks.has(line.id.replace(/^link-/, "")));
  });
  const dropIndex = state.traceDropEvent
    ? state.capturedEvents.indexOf(state.traceDropEvent.rawEvent)
    : -1;
  const event = dropIndex >= 0 && dropIndex <= state.playbackIndex
    ? state.traceDropEvent.event
    : null;
  const position = event ? locationPosition(event.location) : null;
  if (!position) {
    elements.dropMarker.className = "drop-marker is-hidden";
    return;
  }
  const canvas = state.topology.canvas || { width: DEFAULT_CANVAS_WIDTH, height: DEFAULT_CANVAS_HEIGHT };
  elements.dropMarker.className = "drop-marker";
  elements.dropMarker.style.left = `${position.x / canvas.width * 100}%`;
  elements.dropMarker.style.top = `${position.y / canvas.height * 100}%`;
  elements.dropMarker.title = event.decision;
  elements.dropMarker.setAttribute("aria-label", `Packet drop point: ${event.decision}`);
}

function renderDeviceList(search = "") {
  const query = search.trim().toLowerCase();
  const devices = state.topology.devices.filter((device) => `${device.name} ${device.role} ${device.type}`.toLowerCase().includes(query));
  elements.deviceCount.textContent = state.topology.devices.length;
  elements.deviceList.innerHTML = devices.map((device) => `
    <button class="device-list-item ${device.name === state.selectedDevice ? "is-selected" : ""}" data-device="${escapeHtml(device.name)}" type="button">
      <span class="list-icon">${deviceIcon(device.type)}</span>
      <span class="device-list-copy"><strong>${escapeHtml(device.name)}</strong><span>${escapeHtml(device.role)}</span></span>
    </button>
  `).join("");
}

function deviceCardSummary(device) {
  const address = device.destinationAddresses?.[0];
  if (address) {
    const prefix = address.mask == null ? address.ip : `${address.ip}/${address.mask}`;
    const interfaceName = address.interface === "loopback" ? "Loopback" : address.interface;
    return `${interfaceName} · ${prefix}`;
  }
  const interfaceCount = device.interfaces?.length || 0;
  return interfaceCount
    ? `${interfaceCount} interface${interfaceCount === 1 ? "" : "s"}`
    : "No interfaces";
}

function createSvgElement(tag, attributes = {}) {
  const element = document.createElementNS("http://www.w3.org/2000/svg", tag);
  Object.entries(attributes).forEach(([key, value]) => element.setAttribute(key, value));
  return element;
}

function createLinkLabel(className, text, title, tagName = "span") {
  const label = document.createElement(tagName);
  label.className = className;
  label.textContent = text;
  label.title = title;
  if (tagName === "button") label.type = "button";
  return label;
}

function stpEndpointStatus(device, interfaceName) {
  if (!device?.stp?.enabled || !interfaceName) return null;
  const port = device.stp.ports.find((item) => item.interface === interfaceName);
  if (!port) return null;
  return {
    ...port,
    blocksData: port.state !== "forwarding"
  };
}

function surfacePoint(position, canvas) {
  return {
    x: position.x / canvas.width * elements.topologySurface.clientWidth,
    y: position.y / canvas.height * elements.topologySurface.clientHeight
  };
}

function positionEndpointLabel(label, node, origin, unitX, unitY) {
  const nodeWidth = node?.offsetWidth || TOPOLOGY_NODE_WIDTH;
  const nodeHeight = node?.offsetHeight || TOPOLOGY_NODE_HEIGHT;
  const horizontalClearance = Math.abs(unitX) > 0.001
    ? (nodeWidth / 2 + label.offsetWidth / 2 + LINK_LABEL_GAP) / Math.abs(unitX)
    : Number.POSITIVE_INFINITY;
  const verticalClearance = Math.abs(unitY) > 0.001
    ? (nodeHeight / 2 + label.offsetHeight / 2 + LINK_LABEL_GAP) / Math.abs(unitY)
    : Number.POSITIVE_INFINITY;
  const distance = Math.min(horizontalClearance, verticalClearance);
  label.style.left = `${origin.x + unitX * distance}px`;
  label.style.top = `${origin.y + unitY * distance}px`;
}

function positionLinkLabels(canvas = state.topology.canvas || { width: DEFAULT_CANVAS_WIDTH, height: DEFAULT_CANVAS_HEIGHT }) {
  state.topology.links.forEach((link, index) => {
    const from = getDevice(link.from);
    const to = getDevice(link.to);
    if (!from || !to) return;

    const fromPoint = surfacePoint(from.position, canvas);
    const toPoint = surfacePoint(to.position, canvas);
    const dx = toPoint.x - fromPoint.x;
    const dy = toPoint.y - fromPoint.y;
    const length = Math.hypot(dx, dy) || 1;
    const unitX = dx / length;
    const unitY = dy / length;
    const labels = elements.linkLabelLayer.querySelectorAll(`[data-link-index="${index}"]`);
    const fromLabel = labels[0];
    const toLabel = labels[1];
    const costLabel = labels[2];
    const fromNode = elements.nodeLayer.querySelector(`[data-device="${CSS.escape(link.from)}"]`);
    const toNode = elements.nodeLayer.querySelector(`[data-device="${CSS.escape(link.to)}"]`);

    if (fromLabel) positionEndpointLabel(fromLabel, fromNode, fromPoint, unitX, unitY);
    if (toLabel) positionEndpointLabel(toLabel, toNode, toPoint, -unitX, -unitY);
    if (costLabel) {
      let normalX = -unitY;
      let normalY = unitX;
      if (normalY > 0 || (Math.abs(normalY) < 0.001 && normalX < 0)) {
        normalX *= -1;
        normalY *= -1;
      }
      costLabel.style.left = `${fromPoint.x + dx / 2 + normalX * 24}px`;
      costLabel.style.top = `${fromPoint.y + dy / 2 + normalY * 24}px`;
    }
  });
}

function renderTopology() {
  const canvas = state.topology.canvas || { width: DEFAULT_CANVAS_WIDTH, height: DEFAULT_CANVAS_HEIGHT };
  elements.topologySurface.style.width = canvas.width > DEFAULT_CANVAS_WIDTH ? `${canvas.width}px` : "100%";
  elements.topologySurface.style.height = canvas.height > DEFAULT_CANVAS_HEIGHT ? `${canvas.height}px` : "100%";
  elements.linkLayer.setAttribute("viewBox", `0 0 ${canvas.width} ${canvas.height}`);
  elements.linkLayer.querySelectorAll(":scope > :not(defs)").forEach((element) => element.remove());
  elements.linkLabelLayer.innerHTML = "";
  state.topology.links.forEach((link, index) => {
    const from = getDevice(link.from);
    const to = getDevice(link.to);
    if (!from || !to) return;
    const fromSTP = stpEndpointStatus(from, link.fromPort);
    const toSTP = stpEndpointStatus(to, link.toPort);
    const stpBlocked = fromSTP?.blocksData || toSTP?.blocksData;
    const line = createSvgElement("line", {
      id: `link-${link.id}`,
      class: `link-line${stpBlocked ? " is-stp-blocked" : ""}${link.up ? "" : " is-down"}`,
      x1: from.position.x,
      y1: from.position.y,
      x2: to.position.x,
      y2: to.position.y
    });
    elements.linkLayer.append(line);

    const dx = to.position.x - from.position.x;
    const dy = to.position.y - from.position.y;
    const fromLabel = createLinkLabel(
      `link-port-label${fromSTP?.blocksData ? " is-stp-blocked" : ""}${link.up ? "" : " is-down"}`,
      link.fromPort || "port",
      !link.up
        ? `${link.from} · ${link.fromPort || "port"} · link down`
        : fromSTP
        ? `${link.from} · ${link.fromPort || "port"} · STP ${fromSTP.role}/${fromSTP.state}`
        : `${link.from} · ${link.fromPort || "port"}`
    );
    const toLabel = createLinkLabel(
      `link-port-label${toSTP?.blocksData ? " is-stp-blocked" : ""}${link.up ? "" : " is-down"}`,
      link.toPort || "port",
      !link.up
        ? `${link.to} · ${link.toPort || "port"} · link down`
        : toSTP
        ? `${link.to} · ${link.toPort || "port"} · STP ${toSTP.role}/${toSTP.state}`
        : `${link.to} · ${link.toPort || "port"}`
    );
    const costLabel = createLinkLabel(
      `link-cost-label link-state-toggle ${link.up ? "is-up" : "is-down"}`,
      `${link.up ? "Up" : "Down"} · Cost ${link.cost}`,
      `${link.up ? "Bring down" : "Bring up"} ${link.from}:${link.fromPort} ↔ ${link.to}:${link.toPort}`,
      "button"
    );
    costLabel.dataset.linkId = link.id;
    costLabel.setAttribute("aria-pressed", String(link.up));
    costLabel.disabled = Boolean(state.linkBusy) || state.loadingScenario || state.capturingTrace;
    [fromLabel, toLabel, costLabel].forEach((label) => {
      label.dataset.linkIndex = index;
      elements.linkLabelLayer.append(label);
    });

    const badge = createSvgElement("g", {
      id: `badge-${link.id}`,
      class: "lldp-badge",
      transform: `translate(${from.position.x + dx * 0.5}, ${from.position.y + dy * 0.5})`
    });
    badge.innerHTML = '<circle r="10"/><path d="m-4 0 3 3 6-7"/>';
    badge.style.display = "none";
    elements.linkLayer.append(badge);
  });

  elements.nodeLayer.innerHTML = state.topology.devices.map((device) => `
    <button
      class="topology-node ${device.name === state.selectedDevice ? "is-selected" : ""} ${device.stp?.enabled && device.stp.isRoot ? "is-stp-root" : ""}"
      data-device="${escapeHtml(device.name)}"
      data-type="${escapeHtml(device.type)}"
      type="button"
      style="left:${device.position.x / canvas.width * 100}%;top:${device.position.y / canvas.height * 100}%"
    >
      <span class="node-main">
        <span class="node-icon">${deviceIcon(device.type)}</span>
        <span class="node-copy"><strong>${escapeHtml(device.name)}</strong><span title="${escapeHtml(deviceCardSummary(device))}">${escapeHtml(deviceCardSummary(device))}</span></span>
      </span>
      <span class="node-footer">
        <span>${escapeHtml(device.role)}</span>
        ${device.stp?.enabled && device.stp.isRoot ? '<span class="stp-root-badge">STP root</span>' : ""}
      </span>
    </button>
  `).join("");
  positionLinkLabels(canvas);
  renderDiscoveredLinks();
  renderTraceOutcome();
}

function renderMiniRows(rows) {
  if (!rows?.length) return '<div class="empty-row">No entries learned yet.</div>';
  return rows.map((row) => {
    if (typeof row === "string") {
      const [primary, ...rest] = row.split(" → ");
      return `<div class="mini-row"><strong>${escapeHtml(primary)}</strong><span>${escapeHtml(rest.join(" → ") || "active")}</span></div>`;
    }
    const toneClass = row.tone === "stp-blocked"
      ? "is-stp-blocked"
      : row.tone === "stp-forwarding" ? "is-stp-forwarding" : "";
    return `
      <div class="mini-row ${row.isEventEntry ? "is-event-entry" : ""} ${toneClass}">
        <strong>${escapeHtml(row.primary)}${row.isEventEntry ? '<small class="used-entry-badge">Used</small>' : ""}</strong>
        <span title="${escapeHtml(row.secondary)}">${escapeHtml(row.secondary)}</span>
      </div>
    `;
  }).join("");
}

function selectedTableReference(device) {
  const event = state.playbackEvents[state.playbackIndex];
  return event?.device === device.name ? event.tableReference : null;
}

function tableQueryText(reference) {
  const query = reference?.query || {};
  if (reference?.kind === "routing") return `destination ${query.destinationIp || "—"}`;
  if (reference?.kind === "arp") return `IP ${query.ip || "—"}`;
  if (reference?.kind === "mac") return `MAC ${query.mac || "—"} in VLAN ${query.vlan || "—"}`;
  return "the requested key";
}

function tableResultText(reference) {
  const resultLabels = {
    hit: "Matched entry used by this event",
    learned: "Entry learned by this event",
    updated: "Entry updated by this event",
    pending: "Pending entry used by this event",
    expired: "Entry expired by this event",
    removed: "Entry removed by this event",
    miss: "No usable matching entry",
    broadcast: "Broadcast destination; lookup bypassed"
  };
  return resultLabels[reference?.result] || "Table decision for this event";
}

function tableRowsForEvent(rows, kind, reference) {
  let result = (rows || []).map((row) => (typeof row === "string" ? row : { ...row }));
  if (reference?.kind !== kind) return result;
  if (!reference.entry && reference.result === "miss") {
    const query = reference.query || {};
    const queryKey = kind === "arp"
      ? query.ip
      : kind === "mac" ? `${String(query.mac).toLowerCase()}|${query.vlan}` : null;
    if (queryKey) result = result.filter((row) => row.key !== queryKey);
  }
  if (!reference.entry) return result;
  const selectedRow = eventTableRow(reference);
  if (!selectedRow) return result;
  const index = result.findIndex((row) => row.key === selectedRow.key);
  selectedRow.isEventEntry = true;
  if (index === -1) result.unshift(selectedRow);
  else result[index] = selectedRow;
  return result;
}

function updateLLDPToggle(device) {
  const enabled = Boolean(device?.lldpEnabled);
  elements.lldpToggle.disabled = !device || state.lldpBusy || state.stpBusy || state.linkBusy || state.loadingScenario || state.capturingTrace;
  elements.lldpToggle.classList.toggle("is-enabled", enabled);
  elements.lldpToggle.setAttribute("aria-pressed", String(enabled));
  elements.lldpToggle.setAttribute("aria-busy", String(state.lldpBusy));
  elements.lldpToggle.textContent = state.lldpBusy ? "Updating LLDP…" : enabled ? "Disable LLDP" : "Enable LLDP";
}

function stpPriorityOptions(priority) {
  return STP_PRIORITIES.map((value) => `
    <option value="${value}" ${value === priority ? "selected" : ""}>${value}</option>
  `).join("");
}

function renderInspector() {
  const device = getDevice(state.selectedDevice) || state.topology.devices[0];
  updateLLDPToggle(device);
  if (!device) {
    elements.inspectorContent.innerHTML = '<div class="empty-detail">No device is selected.</div>';
    return;
  }
  state.selectedDevice = device.name;
  elements.inspectorHeading.textContent = device.name;
  const tableReference = selectedTableReference(device);
  const interfaceRows = (device.interfaces || []).map((item) => {
    const link = matchingInterfaceLink(device.name, item.name);
    return `
    <div class="interface-row">
      <strong>${escapeHtml(item.name)}</strong>
      <span>${escapeHtml(item.address || item.mode || "unassigned")}</span>
      <button
        class="interface-state-toggle ${item.up ? "is-up" : "is-down"}"
        type="button"
        data-link-interface="${escapeHtml(item.name)}"
        aria-pressed="${item.up}"
        title="${item.up ? "Bring this link down" : "Bring this link up"}"
        ${!link || state.linkBusy || state.lldpBusy || state.stpBusy || state.loadingScenario || state.capturingTrace ? "disabled" : ""}
      >${item.up ? "Up" : "Down"}</button>
    </div>
  `;
  }).join("") || '<div class="empty-row">No interfaces configured.</div>';
  const table = (title, rows, open = false, kind = "") => {
    const isReferenced = tableReference?.kind === kind;
    const eventRows = tableRowsForEvent(rows, kind, tableReference);
    const queryAction = tableReference?.result === "broadcast" ? "Evaluated" : "Looked up";
    const status = isReferenced && !tableReference.entry
      ? `<div class="table-event-status is-${escapeHtml(tableReference.result)}"><strong>${escapeHtml(tableResultText(tableReference))}</strong><span>At this event: ${queryAction.toLowerCase()} ${escapeHtml(tableQueryText(tableReference))}</span></div>`
      : "";
    return `
    <details class="data-disclosure ${isReferenced ? "is-event-table" : ""}" ${open || isReferenced ? "open" : ""}>
      <summary>${title}<span class="disclosure-count">${eventRows.length}</span></summary>
      ${status}
      <div class="mini-table">${renderMiniRows(eventRows)}</div>
    </details>
  `;
  };
  const stp = device.stp || { enabled: false, ports: [], portRows: [] };
  const stpControlsDisabled = state.stpBusy || state.lldpBusy || state.linkBusy || state.loadingScenario || state.capturingTrace;
  const blockedPortCount = stp.enabled ? stp.ports.filter((port) => port.state !== "forwarding").length : 0;
  const stpIdentityRows = stp.enabled ? [
    { primary: "Bridge ID", secondary: stp.bridgeId || "—" },
    { primary: "Root ID", secondary: stp.rootId || "—" }
  ] : [];
  const stpPanel = device.type === "switch" ? `
    <section class="inspector-section stp-section">
      <div class="stp-section-heading">
        <h3>Spanning Tree</h3>
        <span class="stp-status ${stp.enabled ? "is-enabled" : ""}">${stp.enabled ? stp.isRoot ? "Root bridge" : "Enabled" : "Disabled"}</span>
      </div>
      <div class="stp-controls">
        <button
          class="stp-toggle ${stp.enabled ? "is-enabled" : ""}"
          type="button"
          data-stp-action="toggle"
          aria-pressed="${stp.enabled}"
          aria-busy="${state.stpBusy}"
          ${stpControlsDisabled ? "disabled" : ""}
        >${state.stpBusy ? "Updating STP…" : stp.enabled ? "Disable STP" : "Enable STP"}</button>
        <label class="stp-priority-field">
          <span>Bridge priority</span>
          <select data-stp-action="priority" ${stpControlsDisabled ? "disabled" : ""}>
            ${stpPriorityOptions(stp.priority)}
          </select>
        </label>
      </div>
      <div class="inspector-grid stp-metrics">
        <div class="metric-card"><span>Root port</span><strong>${escapeHtml(stp.enabled ? stp.rootPort || "This bridge" : "—")}</strong></div>
        <div class="metric-card"><span>Root cost</span><strong>${stp.enabled ? stp.rootPathCost : "—"}</strong></div>
        <div class="metric-card"><span>Forwarding</span><strong>${stp.enabled ? stp.ports.length - blockedPortCount : "—"}</strong></div>
        <div class="metric-card"><span>Blocked</span><strong>${stp.enabled ? blockedPortCount : "—"}</strong></div>
      </div>
      ${table("STP ports", stp.enabled ? stp.portRows : [], true)}
      ${table("STP identifiers", stpIdentityRows)}
    </section>
  ` : "";
  elements.inspectorContent.innerHTML = `
    <div class="inspector-summary">
      <span class="inspector-icon">${deviceIcon(device.type)}</span>
      <span class="inspector-summary-copy"><strong>${escapeHtml(device.role)}</strong>${device.loopback ? `<span>Loopback ${escapeHtml(device.loopback)}</span>` : ""}</span>
    </div>
    <div class="inspector-tags">
      <span class="inspector-tag">${escapeHtml(device.type.toUpperCase())}</span>
      <span class="inspector-tag">${device.interfaces?.length || 0} interfaces</span>
      <span class="inspector-tag">${device.vlans?.length || 0} VLANs</span>
      ${device.type === "switch" ? `<span class="inspector-tag ${stp.enabled ? "is-stp-enabled" : ""}">STP ${stp.enabled ? stp.isRoot ? "root" : "on" : "off"}</span>` : ""}
    </div>
    ${stpPanel}
    <section class="inspector-section">
      <h3>Interfaces</h3>
      <div class="interface-table">${interfaceRows}</div>
    </section>
    <div class="inspector-grid">
      <div class="metric-card"><span>VLANs</span><strong>${escapeHtml((device.vlans || []).join(", ") || "—")}</strong></div>
      <div class="metric-card"><span>LLDP peers</span><strong>${device.lldp?.length || 0}</strong></div>
      <div class="metric-card"><span>ARP entries</span><strong>${device.arp?.length || 0}</strong></div>
      <div class="metric-card"><span>MAC entries</span><strong>${device.mac?.length || 0}</strong></div>
    </div>
    ${table("VLAN interfaces", device.vlanInterfaces, true)}
    ${table("Routing table", device.routes, true, "routing")}
    ${table("ARP table", device.arp, false, "arp")}
    ${table("MAC address table", device.mac, false, "mac")}
    ${table("LLDP neighbors", device.lldp)}
  `;
  if (tableReference) {
    window.requestAnimationFrame(() => {
      elements.inspectorContent.querySelector(".is-event-entry, .table-event-status")?.scrollIntoView({ block: "nearest" });
    });
  }
}

function protocolClass(protocol) {
  return `protocol-${normalizeProtocol(protocol)}`;
}

function eventMatchesFilter(event) {
  return state.filter === "ALL" || (event.filterProtocol || event.protocol) === state.filter;
}

function visibleEventIndexes() {
  return state.events.reduce((indexes, event, index) => {
    if (eventMatchesFilter(event)) indexes.push(index);
    return indexes;
  }, []);
}

function syncProtocolFilterButtons() {
  document.querySelectorAll(".filter-chip").forEach((chip) => {
    const active = chip.dataset.filter === state.filter;
    chip.classList.toggle("is-active", active);
    chip.setAttribute("aria-pressed", String(active));
  });
}

function updateStepCounter() {
  const position = state.playbackIndex;
  elements.stepCounter.textContent = position >= 0
    ? `${position + 1} / ${state.playbackEvents.length}`
    : `0 / ${state.playbackEvents.length}`;
}

function renderTimeline() {
  const indexes = visibleEventIndexes();
  const revealedIndexes = indexes.filter((index) => index <= state.revealedThrough);
  const selectedPosition = revealedIndexes.indexOf(state.selectedEvent);
  const visibleEvents = revealedIndexes.map((index) => ({ event: state.events[index], index }));
  const eventLabel = state.timelineMode === "SUMMARY" ? "key events" : "events";
  elements.eventCount.textContent = `${visibleEvents.length} / ${indexes.length} ${eventLabel}`;
  elements.eventList.innerHTML = visibleEvents.map(({ event, index }, position) => {
    const completed = selectedPosition >= 0 && position < selectedPosition;
    const current = position === selectedPosition;
    const dropped = event.protocol === "DROP";
    return `
      <button class="event-row ${completed ? "is-completed" : ""} ${current ? "is-current" : ""}" data-event="${index}" type="button">
        <span class="event-time">${escapeHtml(event.time)}</span>
        <span class="event-protocol ${protocolClass(event.protocol)}">${escapeHtml(event.protocol)}</span>
        <span class="event-path">${escapeHtml(event.path)}</span>
        <span class="event-copy">${escapeHtml(event.title)}</span>
        <span class="event-result ${dropped ? "is-dropped" : ""}">${escapeHtml(event.result)}</span>
      </button>
    `;
  }).join("");
  updateStepCounter();
  renderEventDetail();
}

function renderEventDetail() {
  const event = state.events[state.selectedEvent];
  if (!event) {
    const hasFilteredEvents = visibleEventIndexes().length > 0;
    const message = state.events.length && state.filter !== "ALL"
      ? hasFilteredEvents
        ? `No ${escapeHtml(state.filter)} events have appeared in playback yet.`
        : `No ${escapeHtml(state.filter)} events are present in this trace.`
      : "Select a packet event to see why it happened.";
    elements.eventDetail.innerHTML = `<div class="empty-detail">${message}</div>`;
    return;
  }
  const fields = Object.entries(event.fields || {}).map(([label, value]) => `
    <div class="detail-field"><span>${escapeHtml(displayFieldName(label))}</span><strong title="${escapeHtml(value)}">${escapeHtml(value)}</strong></div>
  `).join("");
  const tableReference = event.tableReference;
  const tableName = { routing: "Routing table", arp: "ARP table", mac: "MAC address table" }[tableReference?.kind];
  const tableDecision = tableReference ? `
    <div class="event-table-reference is-${escapeHtml(tableReference.result)}">
      <span>${escapeHtml(tableName || "Table")}</span>
      <strong>${escapeHtml(tableResultText(tableReference))}</strong>
      <small>${escapeHtml(tableQueryText(tableReference))}</small>
    </div>
  ` : "";
  elements.eventDetail.innerHTML = `
    <div class="detail-title">
      <h3>${escapeHtml(event.title)}</h3>
      <span class="detail-protocol ${protocolClass(event.protocol)}">${escapeHtml(event.protocol)}</span>
    </div>
    <div class="decision-box"><strong>${escapeHtml(event.decisionLabel)}</strong>${escapeHtml(event.decision)}</div>
    ${tableDecision}
    <div class="detail-grid">${fields}</div>
  `;
}

function locationPosition(location) {
  if (!location) return null;
  if (location.node) {
    const node = getDevice(location.node)?.position;
    return node ? { x: node.x + 59, y: node.y - 31, angle: 0 } : null;
  }
  const from = getDevice(location.from);
  const to = getDevice(location.to);
  if (!from || !to) return null;
  const progress = location.progress ?? 0.5;
  return {
    x: from.position.x + (to.position.x - from.position.x) * progress,
    y: from.position.y + (to.position.y - from.position.y) * progress,
    angle: Math.atan2(to.position.y - from.position.y, to.position.x - from.position.x) * 180 / Math.PI
  };
}

function renderDiscoveredLinks(linkIds = discoveredLinkIds()) {
  document.querySelectorAll(".link-line").forEach((line) => line.classList.remove("is-discovered"));
  document.querySelectorAll(".lldp-badge").forEach((badge) => { badge.style.display = "none"; });
  linkIds.forEach((id) => {
    document.querySelector(`#link-${CSS.escape(id)}`)?.classList.add("is-discovered");
    const badge = document.querySelector(`#badge-${CSS.escape(id)}`);
    if (badge) badge.style.display = "block";
  });
}

function applyEventVisuals(event) {
  document.querySelectorAll(".link-line").forEach((line) => {
    line.classList.remove("is-discovered", "is-active", "is-broadcast", "is-lldp", "is-drop");
  });
  renderDiscoveredLinks(event.discoveredLinks || []);
  (event.activeLinks || []).forEach((id) => {
    const line = document.querySelector(`#link-${CSS.escape(id)}`);
    line?.classList.add(event.protocol === "ARP" ? "is-broadcast" : event.protocol === "LLDP" ? "is-lldp" : event.protocol === "DROP" ? "is-drop" : "is-active");
  });

  document.querySelectorAll(".topology-node").forEach((node) => node.classList.remove("is-packet-location"));
  const currentNode = event.location?.node || event.device;
  document.querySelector(`.topology-node[data-device="${CSS.escape(currentNode)}"]`)?.classList.add("is-packet-location");

  const position = locationPosition(event.location);
  if (position) {
    const canvas = state.topology.canvas || { width: DEFAULT_CANVAS_WIDTH, height: DEFAULT_CANVAS_HEIGHT };
    elements.packetMarker.className = `packet-marker ${protocolClass(event.protocol === "SYSTEM" ? "ICMP" : event.protocol)}`;
    elements.packetMarker.style.left = `${position.x / canvas.width * 100}%`;
    elements.packetMarker.style.top = `${position.y / canvas.height * 100}%`;
    elements.packetMarker.style.setProperty("--packet-angle", `${position.angle || 0}deg`);
  } else {
    elements.packetMarker.className = "packet-marker is-hidden";
  }

  elements.locationToast.innerHTML = `
    <span class="location-protocol ${protocolClass(event.protocol)}">${escapeHtml(event.protocol)}</span>
    <div><small>Packet location</small><strong>${escapeHtml(event.path)} · ${escapeHtml(event.title)}</strong></div>
  `;
  renderTraceOutcome();
}

function showPacketActivity(status, message) {
  document.querySelectorAll(".link-line").forEach((line) => {
    line.classList.remove("is-active", "is-broadcast", "is-lldp", "is-drop");
  });
  renderDiscoveredLinks();
  document.querySelectorAll(".topology-node").forEach((node) => node.classList.remove("is-packet-location"));
  elements.packetMarker.className = "packet-marker is-hidden";
  elements.locationToast.innerHTML = `
    <span class="location-protocol protocol-system">${escapeHtml(status)}</span>
    <div><small>Packet activity</small><strong>${escapeHtml(message)}</strong></div>
  `;
}

function revealDeviceOnCanvas(name) {
  const node = elements.nodeLayer.querySelector(`[data-device="${CSS.escape(name)}"]`);
  if (!node) return;
  const viewport = elements.topologyScroll.getBoundingClientRect();
  const bounds = node.getBoundingClientRect();
  const margin = 24;
  let left = elements.topologyScroll.scrollLeft;
  let top = elements.topologyScroll.scrollTop;

  if (bounds.left < viewport.left + margin) left -= viewport.left + margin - bounds.left;
  else if (bounds.right > viewport.right - margin) left += bounds.right - viewport.right + margin;
  if (bounds.top < viewport.top + margin) top -= viewport.top + margin - bounds.top;
  else if (bounds.bottom > viewport.bottom - margin) top += bounds.bottom - viewport.bottom + margin;

  elements.topologyScroll.scrollTo({ left, top, behavior: "smooth" });
}

function selectDevice(name, { reveal = false } = {}) {
  if (!getDevice(name)) return;
  state.selectedDevice = name;
  renderDeviceList(elements.deviceSearch.value);
  document.querySelectorAll(".topology-node").forEach((node) => node.classList.toggle("is-selected", node.dataset.device === name));
  renderInspector();
  if (reveal) revealDeviceOnCanvas(name);
}

function selectPlaybackEvent(index, options = {}) {
  const nextIndex = Number(index);
  if (!Number.isInteger(nextIndex) || !state.playbackEvents[nextIndex]) return;
  if (options.pause !== false) pauseSimulation(true);
  state.playbackIndex = nextIndex;
  syncTimelineProgress();
  const event = state.playbackEvents[nextIndex];
  if (event.device && getDevice(event.device)) selectDevice(event.device);
  renderTimeline();
  applyEventVisuals(event);
  updatePlaybackControls();
  if (options.scroll) {
    elements.eventList.querySelector(`[data-event="${state.selectedEvent}"]`)?.scrollIntoView({ block: "nearest" });
  }
}

function selectEvent(index, options = {}) {
  const timelineIndex = Number(index);
  if (!Number.isInteger(timelineIndex) || !state.events[timelineIndex] || !eventMatchesFilter(state.events[timelineIndex])) return;
  const playbackIndex = state.capturedEvents.indexOf(state.rawEvents[timelineIndex]);
  if (playbackIndex >= 0) selectPlaybackEvent(playbackIndex, options);
}

function stepEvent(offset) {
  selectPlaybackEvent(state.playbackIndex + offset, { scroll: true });
}

function updatePlaybackControls() {
  const tracing = state.capturingTrace;
  const traceType = elements.traceType.value;
  const tracingLLDP = traceType === "lldp";
  const tracingTraceroute = traceType === "traceroute";
  const protocolTracing = tracing && state.status === "tracing";
  const loading = state.loadingScenario;
  const busy = tracing || loading;
  const controlsBusy = busy || state.lldpBusy || state.stpBusy || Boolean(state.linkBusy);
  const indexes = visibleEventIndexes();
  const hasPreviousEvent = state.playbackIndex > 0;
  const hasNextEvent = state.playbackIndex >= 0 && state.playbackIndex < state.playbackEvents.length - 1;
  const canRunTrace = !state.playbackEvents.length;
  const canReplayTrace = state.playbackEvents.length > 1 && state.playbackIndex === state.playbackEvents.length - 1;
  elements.previousEventButton.disabled = busy || !hasPreviousEvent;
  elements.playPauseButton.disabled = !state.playing && (controlsBusy || (!canRunTrace && !hasNextEvent && !canReplayTrace));
  elements.nextEventButton.disabled = busy || !hasNextEvent;
  elements.downloadEventsButton.disabled = busy || !indexes.length;
  elements.downloadEventsButton.title = indexes.length
    ? `Download ${indexes.length} ${state.filter === "ALL" ? "timeline" : state.filter} event${indexes.length === 1 ? "" : "s"} as JSON`
    : state.events.length ? `No ${state.filter} events to download` : "Run a trace before downloading events";
  elements.downloadEventsButton.setAttribute("aria-label", elements.downloadEventsButton.title);
  elements.scenarioSelect.disabled = controlsBusy;
  if (controlsBusy) setScenarioMenuOpen(false);
  const selectedScenario = scenarios[state.activeScenario];
  const selectedScenarioName = scenarioLabel(state.activeScenario);
  elements.downloadScenarioButton.disabled = controlsBusy || state.downloadingTemplate || !selectedScenario;
  elements.downloadScenarioButton.title = selectedScenario
    ? `Download ${selectedScenarioName} YAML template`
    : "Built-in templates can be downloaded";
  elements.downloadScenarioButton.setAttribute("aria-label", elements.downloadScenarioButton.title);
  syncScenarioPicker();
  elements.loadButton.disabled = controlsBusy;
  elements.resetButton.disabled = controlsBusy;
  elements.traceType.disabled = controlsBusy;
  elements.sourceNode.disabled = controlsBusy;
  elements.destinationNode.disabled = controlsBusy;
  elements.traceButton.disabled = controlsBusy || state.playing;
  elements.traceButton.setAttribute("aria-busy", String(protocolTracing || loading));
  elements.traceButton.setAttribute("aria-label", tracingLLDP
    ? "Run LLDP discovery"
    : tracingTraceroute ? "Run traceroute path discovery" : "Run ping reachability test");
  elements.traceButtonLabel.textContent = loading
    ? state.status === "resetting" ? "Resetting…" : "Loading…"
    : protocolTracing ? tracingLLDP ? "Discovering…" : tracingTraceroute ? "Tracing Route…" : "Running Ping…"
      : tracingLLDP ? "Run LLDP Discovery" : tracingTraceroute ? "Run Traceroute" : "Run Ping";
  elements.playPauseButton.dataset.playing = String(state.playing);
  elements.playPauseButton.setAttribute("aria-label", state.playing ? "Pause trace" : "Play trace");
  elements.playPauseButton.title = state.playing
    ? "Pause trace playback"
    : hasNextEvent ? "Play the captured trace"
      : canReplayTrace ? "Replay the captured trace from the beginning"
        : state.playbackEvents.length ? "Trace playback has no next event"
          : tracingLLDP ? "Run LLDP discovery and play the captured trace"
            : tracingTraceroute ? "Run traceroute and play the captured trace"
              : "Run ping and play the captured trace";
  const statusLabels = {
    ready: "Ready to run",
    captured: "Trace captured",
    loading: "Loading network",
    resetting: "Resetting network",
    tracing: tracingLLDP ? "Discovering neighbors" : tracingTraceroute ? "Tracing route" : "Running ping",
    lldp_updating: "Updating LLDP",
    stp_updating: "Updating spanning tree",
    link_updating: "Reconverging topology",
    trace_empty: "No packet events",
    playing: "Playing trace",
    paused: "Trace paused",
    complete: "Trace complete",
    error: "Error"
  };
  elements.runState.textContent = statusLabels[state.status] || state.status;
  const liveBadge = document.querySelector(".live-badge");
  if (liveBadge) {
    liveBadge.dataset.state = state.status;
    liveBadge.classList.toggle("is-running", ["loading", "resetting", "tracing", "lldp_updating", "stp_updating", "link_updating", "playing"].includes(state.status));
  }
  syncTimelineModeButtons();
  elements.topologyStage.classList.toggle("is-playing", state.playing);
}

function settleTopologyVisuals() {
  document.querySelectorAll(".link-line").forEach((line) => {
    line.classList.remove("is-active", "is-broadcast", "is-lldp");
  });
  elements.packetMarker.classList.add("is-settled");
}

function advanceSimulation() {
  if (state.playbackIndex < 0 || state.playbackIndex >= state.playbackEvents.length - 1) {
    pauseSimulation();
    state.status = "complete";
    settleTopologyVisuals();
    updatePlaybackControls();
    showNotice("Trace playback complete.");
    return;
  }
  selectPlaybackEvent(state.playbackIndex + 1, { pause: false, scroll: true });
}

function startSimulation() {
  if (!state.playbackEvents.length || state.playbackIndex < 0 || state.playbackIndex >= state.playbackEvents.length - 1) return;
  state.playing = true;
  state.status = "playing";
  clearInterval(state.timer);
  state.timer = setInterval(advanceSimulation, 1250);
  updatePlaybackControls();
}

function pauseSimulation(forceStatus = false) {
  const wasPlaying = state.playing;
  state.playing = false;
  clearInterval(state.timer);
  state.timer = null;
  if (wasPlaying || forceStatus) state.status = "paused";
  updatePlaybackControls();
}

function togglePlayback() {
  if (state.playing) {
    pauseSimulation();
    return;
  }
  if (!state.playbackEvents.length) {
    runSelectedTrace();
    return;
  }
  if (state.playbackEvents.length > 1 && state.playbackIndex === state.playbackEvents.length - 1) {
    resetSimulation();
  }
  startSimulation();
}

function resetSimulation() {
  pauseSimulation();
  state.playbackIndex = state.playbackEvents.length ? 0 : -1;
  syncTimelineProgress();
  state.status = "ready";
  const firstEvent = state.playbackEvents[state.playbackIndex];
  if (firstEvent?.device && getDevice(firstEvent.device)) selectDevice(firstEvent.device);
  else renderInspector();
  renderTimeline();
  if (firstEvent) applyEventVisuals(firstEvent);
  else if (state.events.length && state.filter !== "ALL") {
    showPacketActivity(`NO ${state.filter} EVENTS`, "Choose another packet filter to view this trace");
  } else {
    showPacketActivity("NO PACKET", "Choose endpoints above, then click Run Ping");
  }
  updatePlaybackControls();
  elements.eventList.scrollTop = 0;
}

function setProtocolFilter(filter) {
  if (!filter || filter === state.filter) return;
  state.filter = filter;
  syncProtocolFilterButtons();
  syncTimelineProgress();
  renderTimeline();
  updatePlaybackControls();
}

function validatedTraceEndpoints(traceName) {
  const source = elements.sourceNode.value;
  const destination = elements.destinationNode.value.trim();
  elements.destinationNode.value = destination;
  elements.destinationNode.setAttribute("aria-invalid", "false");
  if (!source) {
    showError(`Choose a source node for ${traceName}.`);
    return null;
  }
  if (!destination) {
    elements.destinationNode.setAttribute("aria-invalid", "true");
    elements.destinationNode.focus();
    showError("Choose a destination node/interface or enter an IPv4 address.");
    return null;
  }
  if (source === destination) {
    elements.destinationNode.setAttribute("aria-invalid", "true");
    elements.destinationNode.focus();
    showError("Choose a destination other than the source node.");
    return null;
  }
  if (!isKnownDestination(destination) && !isValidIPv4(destination)) {
    elements.destinationNode.setAttribute("aria-invalid", "true");
    elements.destinationNode.focus();
    showError("Enter a valid IPv4 address or choose a destination from the list.");
    return null;
  }
  return { source, destination };
}

async function tracePacket() {
  const endpoints = validatedTraceEndpoints("the ping");
  if (!endpoints) return;
  const { source, destination } = endpoints;
  clearTracerouteHops();
  pauseSimulation();
  state.events = [];
  state.rawEvents = [];
  state.capturedEvents = [];
  state.playbackEvents = [];
  state.playbackIndex = -1;
  state.selectedEvent = -1;
  state.revealedThrough = -1;
  clearTraceOutcome();
  renderTimeline();
  state.capturingTrace = true;
  state.status = "tracing";
  showPacketActivity("TRACING", `Finding a packet path from ${source} to ${destination}…`);
  updatePlaybackControls();
  showNotice(`Running trace ${source} → ${destination} in Go WebAssembly.`);
  try {
    const result = await runtime.command("tracePing", source, destination);
    useCapturedEvents(result.events, { failed: !result.replyReceived });
    state.capturingTrace = false;
    await refreshNodeStates();
    resetSimulation();
    if (!state.playbackEvents.length) {
      state.status = "trace_empty";
      showPacketActivity("NO EVENTS", "The trace completed without any packet events");
      updatePlaybackControls();
      showNotice("Trace completed without packet events.");
      return;
    }
    state.status = "captured";
    startSimulation();
    showNotice(result.replyReceived
      ? `Ping complete: reply received from ${result.destinationIp}. Playing the captured trace.`
      : `Ping complete: no reply from ${result.destinationIp}. Playing the captured trace for inspection.`);
  } catch (error) {
    state.status = "error";
    showPacketActivity("ERROR", "The packet trace could not be completed");
    updatePlaybackControls();
    showError(`Ping failed: ${error.message}`);
  } finally {
    state.capturingTrace = false;
    updatePlaybackControls();
  }
}

async function traceTraceroute() {
  const endpoints = validatedTraceEndpoints("traceroute");
  if (!endpoints) return;
  const { source, destination } = endpoints;
  clearTracerouteHops();
  pauseSimulation();
  state.events = [];
  state.rawEvents = [];
  state.capturedEvents = [];
  state.playbackEvents = [];
  state.playbackIndex = -1;
  state.selectedEvent = -1;
  state.revealedThrough = -1;
  clearTraceOutcome();
  renderTimeline();
  state.capturingTrace = true;
  state.status = "tracing";
  showPacketActivity("TRACEROUTE", `Probing successive hops from ${source} to ${destination}…`);
  updatePlaybackControls();
  showNotice(`Running traceroute ${source} → ${destination} in Go WebAssembly.`);
  try {
    const result = await runtime.command("traceTraceroute", source, destination);
    state.tracerouteHops = result.hops || [];
    renderTracerouteHops();
    useCapturedEvents(result.events, { failed: !result.reached });
    state.capturingTrace = false;
    await refreshNodeStates();
    resetSimulation();
    if (!state.playbackEvents.length) {
      state.status = "trace_empty";
      showPacketActivity("NO EVENTS", "Traceroute completed without any packet events");
      updatePlaybackControls();
      showNotice("Traceroute completed without packet events.");
      return;
    }
    state.status = "captured";
    startSimulation();
    showNotice(result.reached
      ? `Traceroute complete: reached ${result.destinationIp} in ${result.hops.length} hop${result.hops.length === 1 ? "" : "s"}. Playing the captured trace.`
      : `Traceroute complete: stopped after ${result.hops.length} hops without reaching ${result.destinationIp}. Playing the captured trace for inspection.`);
  } catch (error) {
    state.status = "error";
    showPacketActivity("ERROR", "Traceroute could not be completed");
    updatePlaybackControls();
    showError(`Traceroute failed: ${error.message}`);
  } finally {
    state.capturingTrace = false;
    updatePlaybackControls();
  }
}

async function traceLLDP() {
  const enabledDevices = state.topology.devices.filter((device) => device.lldpEnabled);
  if (!enabledDevices.length) {
    showError("Enable LLDP on at least one device before running discovery.");
    return;
  }
  clearTracerouteHops();
  pauseSimulation();
  state.events = [];
  state.rawEvents = [];
  state.capturedEvents = [];
  state.playbackEvents = [];
  state.playbackIndex = -1;
  state.selectedEvent = -1;
  state.revealedThrough = -1;
  clearTraceOutcome();
  renderTimeline();
  state.capturingTrace = true;
  state.status = "tracing";
  showPacketActivity("DISCOVERING", "Capturing one LLDP discovery round across all enabled connected ports…");
  updatePlaybackControls();
  showNotice(`Running LLDP discovery across ${enabledDevices.map((device) => device.name).join(", ")}.`);
  try {
    const result = await runtime.command("traceLLDP");
    useCapturedEvents(result.events);
    state.capturingTrace = false;
    await refreshNodeStates();
    resetSimulation();
    if (!state.playbackEvents.length) {
      state.status = "trace_empty";
      showPacketActivity("NO ACTIVITY", "LLDP discovery completed without advertisements or neighbor updates");
      updatePlaybackControls();
      showNotice("LLDP discovery completed without activity.");
      return;
    }
    const advertisements = result.events.filter((event) => event.action === "lldp_advertisement_created").length;
    const neighborUpdates = result.events.filter((event) =>
      event.action === "lldp_neighbor_discovered" || event.action === "lldp_neighbor_refreshed"
    ).length;
    state.status = "captured";
    startSimulation();
    showNotice(
      `LLDP discovery captured: ${advertisements} advertisement${advertisements === 1 ? "" : "s"} from `
      + `${result.advertisers.length} device${result.advertisers.length === 1 ? "" : "s"}; `
      + `${neighborUpdates} neighbor record${neighborUpdates === 1 ? "" : "s"} updated. Playing the captured trace.`
    );
  } catch (error) {
    state.status = "error";
    showPacketActivity("ERROR", "LLDP discovery could not be completed");
    updatePlaybackControls();
    showError(`LLDP discovery failed: ${error.message}`);
  } finally {
    state.capturingTrace = false;
    updatePlaybackControls();
  }
}

function runSelectedTrace() {
  if (elements.traceType.value === "lldp") traceLLDP();
  else if (elements.traceType.value === "traceroute") traceTraceroute();
  else tracePacket();
}

function updateTraceInputs() {
  const tracingLLDP = elements.traceType.value === "lldp";
  const tracingTraceroute = elements.traceType.value === "traceroute";
  elements.traceEndpoints.hidden = tracingLLDP;
  elements.traceButton.title = tracingLLDP
    ? "Capture one LLDP discovery round across all enabled connected ports"
    : tracingTraceroute
      ? "Discover each routed hop with increasing ICMP probe TTLs"
      : "Run one ping reachability test and capture its ARP, ICMP, and Ethernet events";
  renderTracerouteHops();
  updatePlaybackControls();
}

async function toggleLLDP() {
  const device = getDevice(state.selectedDevice);
  if (!device || state.lldpBusy) return;
  const enable = !device.lldpEnabled;
  const previousStatus = state.status;
  state.lldpBusy = true;
  state.status = "lldp_updating";
  renderInspector();
  updatePlaybackControls();
  showNotice(`${enable ? "Enabling" : "Disabling"} LLDP on ${device.name}.`);
  try {
    await runtime.command("setLLDP", device.name, enable);
    await refreshNodeStates();
    showNotice(`LLDP ${enable ? "enabled" : "disabled"} on ${device.name}.`);
  } catch (error) {
    showError(`Could not ${enable ? "enable" : "disable"} LLDP on ${device.name}: ${error.message}`);
  } finally {
    state.lldpBusy = false;
    state.status = previousStatus;
    renderInspector();
    updatePlaybackControls();
  }
}

async function updateSTP(command, args, pendingMessage, completeMessage, errorMessage) {
  if (state.stpBusy) return;
  const previousStatus = state.status;
  state.stpBusy = true;
  state.status = "stp_updating";
  renderInspector();
  updatePlaybackControls();
  showNotice(pendingMessage);
  try {
    await runtime.command(command, ...args);
    await refreshNodeStates();
    showNotice(completeMessage);
  } catch (error) {
    showError(`${errorMessage}: ${error.message}`);
  } finally {
    state.stpBusy = false;
    state.status = previousStatus;
    renderInspector();
    updatePlaybackControls();
  }
}

function toggleSTP() {
  const device = getDevice(state.selectedDevice);
  if (!device || device.type !== "switch" || state.stpBusy) return;
  const enable = !device.stp.enabled;
  updateSTP(
    "setSTP",
    [device.name, enable],
    `${enable ? "Enabling" : "Disabling"} STP on ${device.name}.`,
    `STP ${enable ? "enabled" : "disabled"} on ${device.name}.`,
    `Could not ${enable ? "enable" : "disable"} STP on ${device.name}`
  );
}

function changeSTPPriority(priority) {
  const device = getDevice(state.selectedDevice);
  if (!device || device.type !== "switch" || state.stpBusy || !STP_PRIORITIES.includes(priority)) return;
  if (device.stp.priority === priority) return;
  updateSTP(
    "setSTPPriority",
    [device.name, priority],
    `Changing ${device.name} STP priority to ${priority}.`,
    `${device.name} STP priority changed to ${priority}; the tree has reconverged.`,
    `Could not change STP priority on ${device.name}`
  );
}

function applyRuntimeTopologySnapshot(snapshot) {
  const selectedDevice = state.selectedDevice;
  state.topology = topologyFromState(snapshot);
  state.selectedDevice = getDevice(selectedDevice)?.name || state.topology.devices[0]?.name || "";
  syncLLDPDiscoveriesFromTopology();
  renderDeviceList(elements.deviceSearch.value);
  renderTopology();
  renderInspector();
  const currentEvent = state.playbackEvents[state.playbackIndex];
  if (currentEvent) applyEventVisuals(currentEvent);
}

async function toggleLink(link) {
  if (!link || state.linkBusy || state.lldpBusy || state.stpBusy || state.loadingScenario || state.capturingTrace) return;
  const enable = !link.up;
  const previousStatus = state.playing ? "paused" : state.status;
  pauseSimulation();
  state.linkBusy = link.id;
  state.status = "link_updating";
  renderTopology();
  renderInspector();
  updatePlaybackControls();
  showNotice(`${enable ? "Bringing up" : "Bringing down"} ${link.from}:${link.fromPort} ↔ ${link.to}:${link.toPort}.`);
  try {
    const snapshot = await runtime.command(
      "setLinkState",
      link.from,
      link.fromPort,
      link.to,
      link.toPort,
      enable
    );
    applyRuntimeTopologySnapshot(snapshot);
    showNotice(
      `${link.from}:${link.fromPort} ↔ ${link.to}:${link.toPort} is ${enable ? "up" : "down"}; the topology has reconverged.`
    );
  } catch (error) {
    showError(`Could not bring the link ${enable ? "up" : "down"}: ${error.message}`);
  } finally {
    state.linkBusy = false;
    state.status = previousStatus;
    renderTopology();
    renderInspector();
    if (state.playbackEvents[state.playbackIndex]) applyEventVisuals(state.playbackEvents[state.playbackIndex]);
    updatePlaybackControls();
  }
}

function toggleInterfaceLink(nodeName, interfaceName) {
  toggleLink(matchingInterfaceLink(nodeName, interfaceName));
}

async function refreshNodeStates() {
  const nodes = await Promise.all(state.topology.devices.map((device) => runtime.command("getNodeState", device.name)));
  // Topologies without explicit YAML coordinates get their positions from autoLayout on load and
  // the runtime reports them back as null, so keep the laid-out position when it has none to offer.
  const positions = new Map(state.topology.devices.map((device) => [device.name, device.position]));
  state.topology.devices = nodes.map((node) => deviceFromState({
    ...node,
    position: node.position || positions.get(node.name)
  }));
  syncLLDPDiscoveriesFromTopology();
  renderDeviceList(elements.deviceSearch.value);
  renderTopology();
  renderInspector();
  if (state.playbackEvents[state.playbackIndex]) applyEventVisuals(state.playbackEvents[state.playbackIndex]);
}

async function resetRuntime() {
  pauseSimulation();
  clearTracerouteHops();
  state.loadingScenario = true;
  state.status = "resetting";
  state.events = [];
  state.rawEvents = [];
  state.capturedEvents = [];
  state.playbackEvents = [];
  state.playbackIndex = -1;
  state.selectedEvent = -1;
  state.revealedThrough = -1;
  clearTraceOutcome();
  renderTimeline();
  showPacketActivity("RESETTING", "Restoring the network to its initial state…");
  updatePlaybackControls();
  try {
    const topology = await runtime.command("reset");
    if (topology) applyTopologyState(topology, "reset topology");
    showNotice("Go simulation state reset.");
  } catch (error) {
    state.status = "error";
    showPacketActivity("ERROR", "The network could not be reset");
    updatePlaybackControls();
    showError(`Reset failed: ${error.message}`);
  } finally {
    state.loadingScenario = false;
    renderInspector();
    updatePlaybackControls();
  }
}

let noticeTimer;
function showNotice(message, { type = "info", duration = 3200 } = {}) {
  elements.notice.textContent = message;
  elements.notice.classList.toggle("is-error", type === "error");
  elements.notice.setAttribute("role", type === "error" ? "alert" : "status");
  elements.notice.classList.add("is-visible");
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => elements.notice.classList.remove("is-visible"), duration);
}

function showError(message) {
  showNotice(message, { type: "error", duration: 12000 });
}

async function loadYaml(file) {
  const previousScenario = state.activeScenario;
  try {
    const text = await file.text();
    pauseSimulation();
    clearTracerouteHops();
    state.loadingScenario = true;
    state.status = "loading";
    state.events = [];
    state.rawEvents = [];
    state.capturedEvents = [];
    state.playbackEvents = [];
    state.playbackIndex = -1;
    state.selectedEvent = -1;
    state.revealedThrough = -1;
    renderTimeline();
    showPacketActivity("LOADING", "Loading network devices and endpoints…");
    updatePlaybackControls();
    const topology = await runtime.command("loadTopology", text);
    state.customScenario = { name: file.name, yaml: text };
    state.activeScenario = "custom";
    elements.customScenarioOption.hidden = false;
    elements.customScenarioOption.querySelector(".scenario-option-name").textContent = `Custom · ${topology.name}`;
    applyTopologyState(topology, file.name);
    showNotice(`Topology loaded · ${topology.nodes.length} nodes · ${topology.links.length} links`);
  } catch (error) {
    state.activeScenario = previousScenario;
    syncScenarioPicker();
    state.status = "error";
    showPacketActivity("ERROR", "The YAML network could not be loaded");
    showError(`Could not load ${file.name}: ${error.message}`);
  } finally {
    state.loadingScenario = false;
    elements.yamlInput.value = "";
    renderInspector();
    updatePlaybackControls();
  }
}

function applyTopologyState(snapshot, sourceName) {
  const topology = topologyFromState(snapshot);
  clearTracerouteHops();
  state.topology = topology;
  state.tracePathLinks = new Set();
  state.traceDropEvent = null;
  state.selectedDevice = topology.devices[0]?.name || "";
  state.selectedEvent = -1;
  state.revealedThrough = -1;
  state.filter = "ALL";
  syncProtocolFilterButtons();
  syncLLDPDiscoveriesFromTopology();
  const activeScenarioName = elements.scenarioMenu.querySelector(`[data-scenario="${state.activeScenario}"] .scenario-option-name`);
  if (activeScenarioName) {
    activeScenarioName.textContent = state.activeScenario === "custom" ? `Custom · ${topology.name}` : topology.name;
  }
  syncScenarioPicker();
  elements.scenarioMeta.textContent = topology.summary || `${topology.devices.length} devices · ${topology.links.length} links`;
  elements.canvasSubtitle.textContent = topology.description || `${topology.devices.length} devices loaded from ${sourceName} into Go WebAssembly.`;
  elements.deviceSearch.value = "";
  updateEndpointOptions();
  renderDeviceList();
  renderTopology();
  elements.topologyScroll.scrollTo({ left: 0, top: 0 });
  renderInspector();
  resetSimulation();
}

async function loadScenario(key) {
  const scenario = scenarios[key];
  if (!scenario) return;
  const previousScenario = state.activeScenario;
  pauseSimulation();
  clearTracerouteHops();
  state.loadingScenario = true;
  state.status = "loading";
  showPacketActivity("LOADING", "Loading network devices and endpoints…");
  updatePlaybackControls();
  try {
    const response = await fetch(scenario.path);
    if (!response.ok) throw new Error(`scenario request failed with ${response.status}`);
    const topology = await runtime.command("loadTopology", await response.text());
    state.activeScenario = key;
    state.events = [];
    state.rawEvents = [];
    state.capturedEvents = [];
    state.playbackEvents = [];
    state.playbackIndex = -1;
    state.selectedEvent = -1;
    state.revealedThrough = -1;
    applyTopologyState(topology, scenario.path);
    showNotice(`${scenarioLabel(key)} loaded · ${topology.nodes.length} nodes · ${topology.links.length} links`);
  } catch (error) {
    state.activeScenario = previousScenario;
    syncScenarioPicker();
    state.status = "error";
    showPacketActivity("ERROR", "The selected network could not be loaded");
    showError(`Could not load scenario: ${error.message}`);
  } finally {
    state.loadingScenario = false;
    renderInspector();
    updatePlaybackControls();
  }
}

async function loadCustomScenario() {
  if (!state.customScenario) return;
  const previousScenario = state.activeScenario;
  pauseSimulation();
  clearTracerouteHops();
  state.loadingScenario = true;
  state.status = "loading";
  showPacketActivity("LOADING", "Loading network devices and endpoints…");
  updatePlaybackControls();
  try {
    const topology = await runtime.command("loadTopology", state.customScenario.yaml);
    state.activeScenario = "custom";
    state.events = [];
    state.rawEvents = [];
    state.capturedEvents = [];
    state.playbackEvents = [];
    state.playbackIndex = -1;
    state.selectedEvent = -1;
    state.revealedThrough = -1;
    applyTopologyState(topology, state.customScenario.name);
    showNotice(`Custom topology loaded · ${topology.nodes.length} nodes · ${topology.links.length} links`);
  } catch (error) {
    state.activeScenario = previousScenario;
    syncScenarioPicker();
    state.status = "error";
    showPacketActivity("ERROR", "The custom network could not be loaded");
    showError(`Could not load custom topology: ${error.message}`);
  } finally {
    state.loadingScenario = false;
    renderInspector();
    updatePlaybackControls();
  }
}

async function downloadScenarioTemplate() {
  const key = state.activeScenario;
  const scenario = scenarios[key];
  if (!scenario) return;
  state.downloadingTemplate = true;
  updatePlaybackControls();
  try {
    const response = await fetch(scenario.path);
    if (!response.ok) throw new Error(`template request failed with ${response.status}`);
    const url = URL.createObjectURL(await response.blob());
    const link = document.createElement("a");
    link.href = url;
    link.download = scenario.path.split("/").pop() || `${key}.yaml`;
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
    showNotice(`${link.download} downloaded`);
  } catch (error) {
    showError(`Could not download template: ${error.message}`);
  } finally {
    state.downloadingTemplate = false;
    updatePlaybackControls();
  }
}

function timelineEventsFilename() {
  const topologyName = String(state.topology.name || "packet-trace")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "") || "packet-trace";
  const protocol = state.filter === "ALL" ? "" : `-${state.filter.toLowerCase()}`;
  const detailLevel = state.timelineMode === "SUMMARY" ? "-summary" : "-detailed";
  return `${topologyName}${protocol}${detailLevel}-events.json`;
}

function downloadTimelineEvents() {
  const indexes = visibleEventIndexes();
  if (!indexes.length) return;
  const events = indexes.map((index) => state.rawEvents[index] ?? state.events[index]);
  const filename = timelineEventsFilename();
  const blob = new Blob([`${JSON.stringify(events, null, 2)}\n`], { type: "application/json;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
  showNotice(`${filename} downloaded · ${events.length} event${events.length === 1 ? "" : "s"}`);
}

async function initializeRuntime() {
  try {
    await runtime.ready;
    await loadScenario(defaultScenario);
  } catch (error) {
    state.loadingScenario = false;
    state.status = "error";
    showPacketActivity("UNAVAILABLE", "The simulator could not start");
    updatePlaybackControls();
    showError(`WebAssembly failed to start: ${error.message}`);
  }
}

elements.deviceList.addEventListener("click", (event) => {
  const button = event.target.closest("[data-device]");
  if (button) selectDevice(button.dataset.device, { reveal: true });
});

elements.nodeLayer.addEventListener("click", (event) => {
  const button = event.target.closest("[data-device]");
  if (button) selectDevice(button.dataset.device);
});

elements.linkLabelLayer.addEventListener("click", (event) => {
  const button = event.target.closest("[data-link-id]");
  if (!button) return;
  toggleLink(state.topology.links.find((link) => link.id === button.dataset.linkId));
});

elements.eventList.addEventListener("click", (event) => {
  const button = event.target.closest("[data-event]");
  if (button) selectEvent(button.dataset.event, { scroll: false });
});

elements.collapseToggles.forEach((toggle) => {
  applyPanelCollapsed(toggle, readStoredCollapsed(toggle.dataset.collapse));
  toggle.addEventListener("click", () => {
    const collapsed = toggle.getAttribute("aria-expanded") === "true";
    applyPanelCollapsed(toggle, collapsed);
    storeCollapsed(toggle.dataset.collapse, collapsed);
  });
});

elements.deviceSearch.addEventListener("input", (event) => renderDeviceList(event.target.value));
elements.protocolHelpButton.addEventListener("click", () => {
  setProtocolHelpOpen(elements.protocolHelpPopover.hidden);
});
elements.protocolHelpClose.addEventListener("click", () => {
  setProtocolHelpOpen(false, { returnFocus: true });
});
elements.scenarioSelect.addEventListener("click", () => {
  setScenarioMenuOpen(elements.scenarioMenu.hidden);
});
elements.scenarioSelect.addEventListener("keydown", (event) => {
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  event.preventDefault();
  setScenarioMenuOpen(true, { focusSelected: true });
});
elements.scenarioMenu.addEventListener("click", (event) => {
  const option = event.target.closest("[data-scenario]");
  if (!option) return;
  const key = option.dataset.scenario;
  setScenarioMenuOpen(false);
  elements.scenarioSelect.focus();
  if (key === state.activeScenario) return;
  if (key === "custom") loadCustomScenario();
  else loadScenario(key);
});
elements.scenarioMenu.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    event.preventDefault();
    setScenarioMenuOpen(false);
    elements.scenarioSelect.focus();
    return;
  }
  if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
  const items = [...elements.scenarioMenu.querySelectorAll("button:not([hidden]):not(:disabled)")];
  const currentIndex = items.indexOf(document.activeElement);
  const nextIndex = event.key === "Home"
    ? 0
    : event.key === "End"
      ? items.length - 1
      : (currentIndex + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
  event.preventDefault();
  items[nextIndex]?.focus();
});
document.addEventListener("click", (event) => {
  if (!elements.scenarioPicker.contains(event.target)) setScenarioMenuOpen(false);
  if (!elements.protocolHelpPopover.hidden && !event.target.closest(".protocol-key")) setProtocolHelpOpen(false);
});
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape" || elements.protocolHelpPopover.hidden) return;
  event.preventDefault();
  setProtocolHelpOpen(false, { returnFocus: true });
});
elements.downloadScenarioButton.addEventListener("click", () => {
  setScenarioMenuOpen(false);
  downloadScenarioTemplate();
});
elements.downloadEventsButton.addEventListener("click", downloadTimelineEvents);
elements.timelineModeButtons.forEach((button) => button.addEventListener("click", () => {
  setTimelineMode(button.dataset.timelineMode);
}));
elements.loadButton.addEventListener("click", () => elements.yamlInput.click());
elements.yamlInput.addEventListener("change", (event) => {
  const file = event.target.files?.[0];
  if (file) loadYaml(file);
});
elements.previousEventButton.addEventListener("click", () => stepEvent(-1));
elements.playPauseButton.addEventListener("click", togglePlayback);
elements.nextEventButton.addEventListener("click", () => stepEvent(1));
elements.resetButton.addEventListener("click", resetRuntime);
elements.traceButton.addEventListener("click", runSelectedTrace);
elements.traceType.addEventListener("change", () => {
  clearTracerouteHops();
  updateTraceInputs();
});
elements.destinationNode.addEventListener("input", () => elements.destinationNode.setAttribute("aria-invalid", "false"));
elements.destinationNode.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !elements.traceButton.disabled) runSelectedTrace();
});
elements.lldpToggle.addEventListener("click", toggleLLDP);
elements.inspectorContent.addEventListener("click", (event) => {
  const interfaceToggle = event.target.closest("[data-link-interface]");
  if (interfaceToggle) {
    toggleInterfaceLink(state.selectedDevice, interfaceToggle.dataset.linkInterface);
    return;
  }
  if (event.target.closest('[data-stp-action="toggle"]')) toggleSTP();
});
elements.inspectorContent.addEventListener("change", (event) => {
  if (!event.target.matches('[data-stp-action="priority"]')) return;
  changeSTPPriority(Number(event.target.value));
});

let linkLabelResizeFrame;
const topologyResizeObserver = new ResizeObserver(() => {
  cancelAnimationFrame(linkLabelResizeFrame);
  linkLabelResizeFrame = requestAnimationFrame(() => positionLinkLabels());
});
topologyResizeObserver.observe(elements.topologySurface);

document.querySelectorAll(".filter-chip").forEach((chip) => chip.addEventListener("click", () => {
  setProtocolFilter(chip.dataset.filter);
}));

syncTimelineModeButtons();
renderDeviceList();
renderTopology();
renderInspector();
renderTimeline();
if (state.playbackEvents[0]) applyEventVisuals(state.playbackEvents[0]);
updatePlaybackControls();
updateEndpointOptions();
updateTraceInputs();
initializeRuntime();
