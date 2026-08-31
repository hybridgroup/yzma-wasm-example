// worker.js runs llama.cpp and the Go program in a Web Worker.
//
// Every call into llama.cpp is synchronous and one token takes milliseconds, so
// this work cannot go on the main thread of the page: it would stop the page.
// The worker sends each piece of the answer to the page as it comes.
//
// The page sends these messages to the worker:
//
//   { kind: "load",  url: "<model URL>" }
//   { kind: "ask",   text: "<question>" }
//   { kind: "reset" }
//
// The worker sends back { kind, text } messages, where kind is one of ready,
// status, progress, loaded, token, done, reset, or error.

// The build of llama.cpp with more than one thread starts each of its threads
// by running this very script again. Emscripten spawns a thread with
// new Worker(_scriptName), and inside a classic worker _scriptName is the URL
// of this file rather than of the module, so every thread arrives here. Such a
// worker is marked with the name "em-pthread", and all it must do is load
// llama.cpp: the page protocol and the Go program belong to the first worker
// alone. Without this the threads never register as threads, and llama.cpp
// waits for them for ever.
const isThread = globalThis.name === "em-pthread";

self.yzmaBase = ".";

// The page can choose the backend with a query on the URL of this worker, such
// as new Worker("./worker.js?mode=cpu"). The values are the ones that
// yzma-loader.js takes: auto, webgpu, or cpu. A thread is spawned from the
// whole URL of this file, query and all, so it makes the same choice.
const workerQuery = new URLSearchParams((self.location.search || "").slice(1));
if (workerQuery.get("mode")) {
  self.yzmaMode = workerQuery.get("mode");
}

// Only the build with more than one thread has threads, so a worker that is one
// has no choice to make. Saying so keeps it from asking the browser about its
// GPU once per thread, which is slow and fills the console.
if (isThread) {
  self.yzmaMode = "cpu";
}

importScripts("./yzma-loader.js");

if (!isThread) {
  // A failure with nobody to catch it must reach the page. Without this the
  // page only sees that nothing more happens.
  self.onerror = (event) => {
    self.postMessage({ kind: "error", text: String((event && event.message) || event) });
  };
  self.onunhandledrejection = (event) => {
    self.postMessage({ kind: "error", text: String((event && event.reason) || event) });
  };

  importScripts("./wasm_exec.js");

  // The Go program says when it is ready by sending its first message, which is
  // the one of kind "ready". Waiting for that is the only safe way to know that
  // it has set its functions: starting the backend takes a moment with the CPU
  // and much longer with WebGPU, where it has to find an adapter and make the
  // shaders.
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

    // The Go program blocks at the end of main, so it keeps running and the
    // page can call into it. Do not wait for this promise.
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
