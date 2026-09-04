# 100% local chat with a model in your browser using WebAssembly written in Go

[![yzma in browser](./images/yzma-in-browser.png)](https://hybridgroup.github.io/yzma-wasm-example)

A chat page that runs the GGUF language model completely in the local browser using WebAssembly. There is no server, no API key, and no data goes anywhere. Uses GPU with WebGPU when available, otherwise uses CPU. Chrome and Edge have WebGPU support, Firefox currently CPU only. Written in Go using [yzma](https://github.com/hybridgroup/yzma) on [TinyGo](http://tinygo.org).

**<https://hybridgroup.github.io/yzma-wasm-example/>**

### Does it run on mobile? Yes.

[![yzma in browser](./images/yzma-on-android.jpeg)](https://hybridgroup.github.io/yzma-wasm-example)

**<https://hybridgroup.github.io/yzma-wasm-example/>**

## How it works

[![yzma logo](https://raw.githubusercontent.com/hybridgroup/yzma/refs/heads/main/images/yzma-logo-full-color-small.png)](https://github.com/hybridgroup/yzma)

The code is written in Go and compiled by TinyGo. The page uses the
[yzma](https://github.com/hybridgroup/yzma) package to run
[llama.cpp](https://github.com/ggml-org/llama.cpp), which is compiled into a
WebAssembly module. All of it runs in a local Web Worker for the best browser
performance.

```
   index.html
       |  postMessage
   worker.js
     |            \
   yzma.wasm       yzma_wasm*.js
   (Go, TinyGo) -> (llama.cpp, Emscripten)
```

The page keeps no record of the conversation. The conversation stays in the
TinyGo WASM module. Each turn sends the full conversation through the model
again.

## Build and run

You need [TinyGo](https://tinygo.org/getting-started/install/) 0.41 or later, Go
1.26, and `node` for the test.

```
make build
make serve
```

Open <http://localhost:8080>, push **Load**, and wait for the download of the
model. The browser caches it.

The list gives four small models to start with.

| Model | Size | Thoughts |
| --- | --- | --- |
| [Qwen2.5 0.5B Instruct](https://huggingface.co/bartowski/Qwen2.5-0.5B-Instruct-GGUF) | approximately 380 MB | no |
| [LFM2.5 350M](https://huggingface.co/LiquidAI/LFM2.5-350M-GGUF) | approximately 220 MB | no |
| [Qwen3.5 0.8B](https://huggingface.co/unsloth/Qwen3.5-0.8B-GGUF) | approximately 510 MB | yes |
| [Gemma 3 1B Heretic Uncensored Thinking](https://huggingface.co/Andycurrent/Gemma-3-1B-it-GLM-4.7-Flash-Heretic-Uncensored-Thinking_GGUF) | approximately 770 MB | no |

Choose **Another URL** to type your own. Any GGUF URL operates if the host sends
CORS headers. Hugging Face sends them.

`make build` downloads approximately 13 MB of llama.cpp into `build/`, compiles
the Go program, and copies the page. No binary files are in the repository.

The download comes from
[llama-cpp-builder](https://github.com/hybridgroup/llama-cpp-builder) and takes
the newest build. To pin a build, name its tag.

```
make build LLAMA_VERSION=b10780
```

## The test

The test holds a two turn conversation in Node, with no browser. The second
question makes sense only if the first question is still in the prompt. Thus a
sensible answer shows that the chat template is correct.

```
make test MODEL=~/models/Qwen2.5-0.5B-Instruct-Q4_K_M.gguf
```

Add `--think` to let a model that reasons think before it answers.

```
node test/chat.js --dir build --model ~/models/Qwen3.5-0.8B-Q4_K_M.gguf --mt --think
```

## WebGPU, threads, and the service worker

llama.cpp has three WebAssembly builds. `yzma-loader.js` selects the best build
that the browser can run.

| Build | What it needs |
| --- | --- |
| `yzma_wasm_webgpu` | WebGPU with f16 shaders, and JSPI. Chrome and Edge 137 or later, or Firefox 153 or later with two switches. |
| `yzma_wasm_mt` | `SharedArrayBuffer`, thus a page with the COOP and COEP headers. |
| `yzma_wasm` | Nothing. It runs everywhere. |

The WebGPU build computes on the GPU and is the fastest of the three. It needs
an adapter with `shader-f16` and it needs JSPI, thus a page can have WebGPU
while llama.cpp still has no device. In that case the loader goes to the CPU,
because a slow page is better than a page that does not run.

GitHub Pages cannot send the COOP and COEP headers. Without help, the browser
gives the page no `SharedArrayBuffer`, and llama.cpp runs on one thread. In Node
that is the difference between 0.9 and 9.6 tokens a second on Qwen2.5 0.5B.

Thus the page loads
[`coi-serviceworker.js`](https://github.com/gzuidhof/coi-serviceworker) first. It
registers a service worker that adds the two headers and reloads the page once.
After that the page is cross origin isolated, and the loader selects the build
with all of the threads. This also operates on localhost, thus a usual static
server is sufficient for development. `make serve` sets the headers too, which
makes no difference but does no damage.

Cross origin isolation makes it necessary for the model to come from a host that
sends CORS headers. Hugging Face sends them.

The line at the top right of the page shows the selected build. To force a
build, add `?mode=cpu` or `?mode=webgpu` to the URL. With `?mode=webgpu` the
console of the worker says which part is missing when the loader goes to the
CPU.

### Firefox

Firefox runs the WebGPU build. WebGPU is not yet on by default, thus set both of
these in `about:config` and start the browser again.

| Switch | Why |
| --- | --- |
| `dom.webgpu.enabled` | WebGPU on Linux is still behind this switch. |
| `dom.webgpu.workers.enabled` | llama.cpp loads in the worker, thus WebGPU in the page alone is not sufficient. |

JSPI came in Firefox 153, thus 153 or later needs no switch for it. Firefox 153
or later with the two switches shows `backend: webgpu` on the page.

Firefox gives an empty `adapter.info`, thus the page shows the plain word
`webgpu` and no name for the card. This is not a failure. Firefox also gives no
subgroups, thus llama.cpp takes the plain f16 shaders and the same card is
slower than it is in Chrome.

The two CPU builds run in Firefox with no switch at all.

## Notes

- **Thinking** lets a model that reasons think before it answers. The page shows
  the thoughts in a block that folds away, and it keeps them out of the
  conversation, because the template of the model drops the thoughts of the
  turns before this one. The list sets the box for you, on for a model that
  reasons and off for the others.
- The box does nothing for a model that does not reason. `main.go` looks for a
  place for thoughts in the chat template of the model and ignores the box when
  there is none. With the box on, the prompt ends with an open block of
  thoughts. With the box off, it ends with an empty one, which tells the model
  to answer at once. A template such as the one of Qwen3 writes the empty block
  itself, thus `main.go` takes that one away first.
- The GGUF of the Gemma model in the list holds the plain Gemma template, which
  has no place for thoughts. The box does nothing for it, even though the name
  of the model says thinking.
- The system prompt box tells the model how to answer. The page sends the prompt
  with each question, thus a change applies to the next answer. An empty box
  gives the default prompt again.
- The model must be smaller than 2 GB. One JavaScript ArrayBuffer holds no more,
  thus a larger model must be in GGUF splits.
- Use a model with a chat template. A base model has no template. The page shows
  a message, but the answers are poor.
- `ChatApplyTemplate` formats one message at a time. Thus `prompt` in `main.go`
  puts the conversation together one message after the other. This is correct
  for a chatml model such as Qwen. It is not fully correct for a model whose
  template puts something one time at the top of a conversation, such as Gemma,
  which folds the system message into the first user turn.
- A discrete NVIDIA card does not give f16 shaders in Chrome. Such a machine
  falls back to the CPU unless you start Chrome with
  `--enable-dawn-features=vulkan_enable_f16_on_nvidia`.
- Firefox uses wgpu and Chrome uses Dawn, thus the two do not always give the
  same adapter or the same features on one machine. On a machine with two cards,
  `__NV_PRIME_RENDER_OFFLOAD=1 __GLX_VENDOR_LIBRARY_NAME=nvidia firefox` or
  `MESA_VK_DEVICE_SELECT=<vendor>:<device> firefox` selects the card.
- The [yzma WebAssembly guide](https://github.com/hybridgroup/yzma/blob/main/wasm/README.md)
  holds more on the backends, the browsers, and the speed of each one.

## Deploying

`.github/workflows/pages.yml` builds and deploys on each push to `main`. Set
**Settings → Pages → Source** to **GitHub Actions** one time. That is all.

## License

Apache 2.0, the same as yzma. `web/min.css` ([min](https://mincss.com)) and
`web/coi-serviceworker.js` are MIT, and they keep their own notices.
