// chat.js holds a two turn conversation in Node, with no browser.
// It tests the chat template, which is correct if the answers make sense.
//
// Usage:
//   node test/chat.js --dir build --model ~/models/some-instruct-model.gguf \
//       [--tokens 64] [--mt] [--think] [--system "how to answer"]

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

function option(name, fallback) {
  const i = process.argv.indexOf("--" + name);
  return i >= 0 && i + 1 < process.argv.length ? process.argv[i + 1] : fallback;
}

const dir = path.resolve(option("dir", "build"));
const modelFile = option("model", "");
const maxTokens = parseInt(option("tokens", "64"), 10);
const mt = process.argv.includes("--mt");
const think = process.argv.includes("--think");
const system = option("system", "");

// A follow-up that only makes sense if the first turn is still in the prompt.
const questions = ["What is the capital of France?", "What river runs through it?"];

if (!modelFile) {
  console.error("give a model with --model");
  process.exit(2);
}

let onMessage = () => {};
globalThis.yzmaOnMessage = (message) => onMessage(message);

// waitFor resolves on the first message of one of these kinds.
function waitFor(...kinds) {
  return new Promise((resolve) => {
    let text = "";
    let thoughts = "";
    onMessage = (message) => {
      if (message.kind === "token") {
        text += message.text;
        process.stdout.write(message.text);
        return;
      }
      if (message.kind === "think") {
        thoughts += message.text;
        process.stdout.write(message.text);
        return;
      }
      console.log("[" + message.kind + "] " + message.text);
      if (kinds.includes(message.kind)) {
        resolve({ ...message, thoughts, text: message.kind === "done" ? text : message.text });
      }
    };
  });
}

async function main() {
  const threads = mt ? Math.max(1, Math.min(os.cpus().length, 16)) : 1;

  globalThis.crossOriginIsolated = mt;
  globalThis.yzmaBase = dir;
  globalThis.yzmaThreaded = mt;
  globalThis.yzmaThreads = threads;
  globalThis.yzmaBackend = mt ? "cpu-threads" : "cpu";

  const factory = require(path.join(dir, mt ? "yzma_wasm_mt.js" : "yzma_wasm.js"));
  const llamaModule = await factory({
    locateFile: (file) => path.join(dir, file),
    print: () => {},
    printErr: () => {},
    pthreadPoolSize: threads,
  });

  globalThis.yzmaModule = llamaModule;
  globalThis.yzmaReady = Promise.resolve(llamaModule);

  // The page downloads the model with FetchModelFile. Node has it already.
  llamaModule.FS.mkdirTree("/models");
  llamaModule.FS.writeFile("/models/model.gguf", fs.readFileSync(modelFile));

  require(path.join(dir, "wasm_exec.js"));

  const go = new Go();
  const binary = fs.readFileSync(path.join(dir, "yzma.wasm"));
  const result = await WebAssembly.instantiate(binary, go.importObject);

  const ready = waitFor("ready", "error");
  go.run(result.instance); // the program blocks at the end of main
  if ((await ready).kind === "error") process.exit(1);

  const loaded = waitFor("loaded", "error");
  globalThis.yzmaOpenModel("/models/model.gguf");
  if ((await loaded).kind === "error") process.exit(1);

  globalThis.yzmaSetThinking(think);
  if (system) globalThis.yzmaSetSystem(system);

  for (const question of questions) {
    console.log("\n> " + question);
    const done = waitFor("done", "error");
    globalThis.yzmaAsk(question, maxTokens);
    const answer = await done;
    if (answer.kind === "error") process.exit(1);
    if ((answer.text + answer.thoughts).trim().length === 0) {
      console.error("no tokens came out");
      process.exit(1);
    }
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
