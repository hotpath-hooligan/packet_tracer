importScripts("wasm_exec.js");

const go = new Go();
const queuedCommands = [];
let runtimeReady = false;
const commandHandlers = {
  loadTopology: (...args) => self.loadTopology(...args),
  ping: (...args) => self.ping(...args),
  tracePing: (...args) => self.tracePing(...args),
  traceTraceroute: (...args) => self.traceTraceroute(...args),
  traceLLDP: (...args) => self.traceLLDP(...args),
  getNodeState: (...args) => self.getNodeState(...args),
  setLLDP: (...args) => self.setLLDP(...args),
  setSTP: (...args) => self.setSTP(...args),
  setSTPPriority: (...args) => self.setSTPPriority(...args),
  setLinkState: (...args) => self.setLinkState(...args),
  reset: (...args) => self.reset(...args)
};

self.onmessage = (message) => {
  if (!runtimeReady) {
    queuedCommands.push(message.data);
    return;
  }
  runCommand(message.data);
};

async function runCommand(request) {
  const { id, command, args = [] } = request;
  try {
    const handler = commandHandlers[command];
    if (typeof handler !== "function") throw new Error(`Unknown simulator command: ${command}`);
    const result = await handler(...args);
    self.postMessage({ type: "response", id, result });
  } catch (error) {
    self.postMessage({ type: "response", id, error: String(error?.message || error) });
  }
}

async function instantiateWasm() {
  if ("DecompressionStream" in self) {
    try {
      const compressed = await fetch("packet-tracer.wasm.gz");
      if (compressed.ok) {
        const stream = compressed.body.pipeThrough(new DecompressionStream("gzip"));
        return WebAssembly.instantiate(await new Response(stream).arrayBuffer(), go.importObject);
      }
    } catch {
    }
  }
  const response = await fetch("packet-tracer.wasm");
  if (!response.ok) throw new Error(`WebAssembly request failed with ${response.status}`);
  if (WebAssembly.instantiateStreaming) {
    try {
      return await WebAssembly.instantiateStreaming(response.clone(), go.importObject);
    } catch (error) {
      if (!String(error).includes("MIME")) throw error;
    }
  }
  return WebAssembly.instantiate(await response.arrayBuffer(), go.importObject);
}

instantiateWasm()
  .then(({ instance }) => {
    go.run(instance);
    waitForRuntime();
  })
  .catch((error) => self.postMessage({ type: "fatal", error: String(error?.message || error) }));

function waitForRuntime() {
  if (typeof self.loadTopology !== "function") {
    setTimeout(waitForRuntime, 0);
    return;
  }
  runtimeReady = true;
  self.postMessage({ type: "runtime-ready" });
  queuedCommands.splice(0).forEach(runCommand);
}
