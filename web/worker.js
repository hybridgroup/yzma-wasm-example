// worker.js runs llama.cpp and the Go program in a Web Worker.
// Every call into llama.cpp is synchronous, so this work would stop the page.
//
// The page sends { kind, url } or { kind, text } messages of kind load, ask,
// system, or reset. The worker sends back { kind, text } messages of kind
// ready, status, progress, loaded, token, done, reset, system, or error.

// The build with more than one thread runs this script again for each thread.
// Such a worker has the name "em-pthread" and must only load llama.cpp.
const isThread = globalThis.name === "em-pthread";

self.yzmaBase = ".";

// The page chooses the backend with a query on the URL of this worker.
// yzma-loader.js takes the values auto, webgpu, or cpu.
const workerQuery = new URLSearchParams((self.location.search || "").slice(1));

// llama.cpp computes wrong values with WebGPU in Firefox, and the model then
// stops before the first word. The CPU is the choice there until that is
// repaired. ?mode=webgpu still gives the GPU, thus a test of the repair is easy.
const isFirefox = /firefox/i.test((self.navigator && self.navigator.userAgent) || "");

if (workerQuery.get("mode")) {
  self.yzmaMode = workerQuery.get("mode");
} else if (isFirefox) {
  self.yzmaMode = "cpu";
}

// A thread has no choice to make. This keeps it from asking the browser
// about the GPU once per thread, which is slow.
if (isThread) {
  self.yzmaMode = "cpu";
}

importScripts("./yzma-loader.js");

if (!isThread) {
  // A failure with nobody to catch it must reach the page.
  self.onerror = (event) => {
    self.postMessage({ kind: "error", text: String((event && event.message) || event) });
  };
  self.onunhandledrejection = (event) => {
    self.postMessage({ kind: "error", text: String((event && event.reason) || event) });
  };

  importScripts("./wasm_exec.js");

  // The Go program sends a message of kind "ready" when it has set its
  // functions. Starting the backend takes much longer with WebGPU.
  let programIsReady;
  const programReady = new Promise((resolve) => {
    programIsReady = resolve;
  });

  const sendToPage = self.postMessage.bind(self);
  self.postMessage = (message) => {
    if (message && (message.kind === "ready" || message.kind === "error")) {
      programIsReady();
    }
    sendToPage(message);
  };

  const started = (async () => {
    // llama.cpp must be ready before the Go program calls Load.
    await self.yzmaReady;

    const go = new Go();
    const result = await WebAssembly.instantiateStreaming(fetch("./yzma.wasm"), go.importObject);

    // The Go program blocks at the end of main, so do not wait for this.
    go.run(result.instance);

    await programReady;

    if (typeof self.yzmaAsk !== "function") {
      throw new Error("the Go program did not set its functions");
    }
  })();

  self.onmessage = async (event) => {
    const message = event.data || {};

    try {
      await started;

      switch (message.kind) {
        case "load":
          self.yzmaLoadModel(message.url);
          break;
        case "ask":
          self.yzmaAsk(message.text);
          break;
        case "system":
          self.yzmaSetSystem(message.text);
          break;
        case "reset":
          self.yzmaReset();
          break;
        default:
          self.postMessage({ kind: "error", text: "unknown message: " + message.kind });
      }
    } catch (err) {
      self.postMessage({ kind: "error", text: String(err) });
    }
  };
}
