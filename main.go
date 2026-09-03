//go:build js && wasm

// Package main is a chat that runs a language model in the browser.
// It drives llama.cpp through yzma and sends each piece of the answer to the page.
package main

import (
	"fmt"
	"strings"
	"syscall/js"
	"time"

	"github.com/hybridgroup/yzma/pkg/llamawasm"
)

const (
	// modelPath is where the model goes in the filesystem of llama.cpp.
	modelPath = "/models/model.gguf"

	// defaultSystem opens a conversation until the page sends another one.
	defaultSystem = "You are a helpful assistant running inside a web browser. Keep your answers short."

	nCtx   = 4096
	nBatch = 512

	// defaultMaxTokens caps one answer. The page can ask for another number.
	defaultMaxTokens = 512
)

// turn is one message of the conversation.
type turn struct {
	Role    string
	Content string
}

var (
	model   llamawasm.Model
	ctx     llamawasm.Context
	vocab   llamawasm.Vocab
	sampler llamawasm.Sampler

	history = []turn{{Role: "system", Content: defaultSystem}}
)

func main() {
	if err := llamawasm.Load(""); err != nil {
		post("error", err.Error())
		return
	}

	llamawasm.LogSet(llamawasm.LogSilent())
	llamawasm.Init()

	// The page calls these.
	js.Global().Set("yzmaLoadModel", js.FuncOf(loadModel))
	js.Global().Set("yzmaOpenModel", js.FuncOf(openModel))
	js.Global().Set("yzmaAsk", js.FuncOf(ask))
	js.Global().Set("yzmaReset", js.FuncOf(reset))
	js.Global().Set("yzmaSetSystem", js.FuncOf(setSystem))

	post("system", defaultSystem)
	post("ready", backendReport())

	// Keep the program alive so that the page can call into it.
	<-make(chan struct{})
}

// loadModel(url) gets a model over the network and makes a context for it.
func loadModel(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		post("error", "loadModel needs a URL")
		return nil
	}
	url := args[0].String()

	go func() {
		post("status", "downloading the model")

		err := llamawasm.FetchModelFile(modelPath, url, func(done, total int64) {
			if total > 0 {
				post("progress", fmt.Sprintf("%d%%", done*100/total))
			}
		})
		if err != nil {
			post("error", err.Error())
			return
		}

		open(modelPath)
	}()

	return nil
}

// openModel(path) loads a model that is already in the filesystem of llama.cpp.
// The page downloads one instead, but the test puts it there itself.
func openModel(this js.Value, args []js.Value) any {
	path := modelPath
	if len(args) > 0 && args[0].Truthy() {
		path = args[0].String()
	}

	go open(path)

	return nil
}

// open loads the model at path, makes a context, and builds the sampler.
func open(path string) {
	post("status", "loading the model")

	params := llamawasm.ModelDefaultParams()

	// A build with WebGPU has a device, so put every layer on it.
	// A build on the CPU has none, and the value does nothing.
	if llamawasm.GPUDevice() != "" {
		params.NGpuLayers = 999
	}

	var err error
	if model, err = llamawasm.ModelLoadFromFile(path, params); err != nil {
		post("error", err.Error())
		return
	}

	ctxParams := llamawasm.ContextDefaultParams()
	ctxParams.NCtx = nCtx
	ctxParams.NBatch = nBatch

	if ctx, err = llamawasm.InitFromModel(model, ctxParams); err != nil {
		post("error", err.Error())
		return
	}

	vocab = llamawasm.ModelGetVocab(model)

	// One sampler for the whole conversation. This chain gives the variety
	// that a chat needs and the greedy sampler does not.
	if sampler != 0 {
		llamawasm.SamplerFree(sampler)
	}
	sampler = llamawasm.SamplerChainInit(llamawasm.SamplerChainDefaultParams())
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitPenalties(llamawasm.VocabNTokens(vocab), 64, 1.1, 0, 0))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitTopK(40))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitTopP(0.95, 1))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitTemp(0.7))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitDist(uint32(time.Now().UnixNano())))

	history = history[:1]

	// A base model has no chat template, and its answers in a chat are poor.
	if llamawasm.ModelChatTemplate(model, "") == "" {
		post("status", "this model has no chat template, so the answers will wander")
	}

	post("loaded", llamawasm.ModelDesc(model)+", "+backendReport())
}

// backendReport says what does the computation.
func backendReport() string {
	if device := llamawasm.GPUDevice(); device != "" {
		return fmt.Sprintf("backend: %s (%s)", llamawasm.Backend(), device)
	}
	return fmt.Sprintf("backend: %s, %d threads", llamawasm.Backend(), llamawasm.Threads())
}

// setSystem(text) replaces the system message. An empty text gives the default
// one back. The next answer uses it.
func setSystem(this js.Value, args []js.Value) any {
	text := defaultSystem
	if len(args) > 0 && args[0].Truthy() {
		if trimmed := strings.TrimSpace(args[0].String()); trimmed != "" {
			text = trimmed
		}
	}

	history[0] = turn{Role: "system", Content: text}

	return nil
}

// reset() forgets the conversation and starts again from the system message.
func reset(this js.Value, args []js.Value) any {
	history = history[:1]
	post("reset", "")
	return nil
}

// ask(text, maxTokens) adds a question and sends the answer to the page piece
// by piece.
func ask(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		post("error", "ask needs a question")
		return nil
	}
	question := args[0].String()

	maxTokens := int32(defaultMaxTokens)
	if len(args) > 1 && args[1].Truthy() {
		maxTokens = int32(args[1].Int())
	}

	go func() {
		if model == 0 || ctx == 0 {
			post("error", "load a model first")
			return
		}

		history = append(history, turn{Role: "user", Content: question})

		tokens := prompt(maxTokens)
		if len(tokens) == 0 {
			post("error", "the prompt has no tokens")
			return
		}

		// The whole conversation goes in again each turn, so the state of
		// the last turn has to go.
		if err := llamawasm.MemoryClear(ctx, true); err != nil {
			post("error", err.Error())
			return
		}
		llamawasm.SamplerReset(sampler)

		var (
			answer strings.Builder
			count  int32
		)
		batch := llamawasm.BatchGetOne(tokens)
		buf := make([]byte, 64)
		start := time.Now()

		for count < maxTokens {
			if _, err := llamawasm.Decode(ctx, batch); err != nil {
				post("error", err.Error())
				return
			}

			token := llamawasm.SamplerSample(sampler, ctx, -1)
			if llamawasm.VocabIsEOG(vocab, token) {
				break
			}
			llamawasm.SamplerAccept(sampler, token)

			if n := llamawasm.TokenToPiece(vocab, token, buf, 0, true); n > 0 {
				piece := string(buf[:n])
				answer.WriteString(piece)
				post("token", piece)
			}

			count++
			batch = llamawasm.BatchGetOne([]llamawasm.Token{token})
		}

		history = append(history, turn{Role: "assistant", Content: answer.String()})

		// A model that stops at the first token gives nothing to read. Say so,
		// because a count of zero alone looks like the page did nothing.
		if count == 0 {
			post("error", "the model gave no answer, so the backend may compute wrong values")
			return
		}

		if elapsed := time.Since(start).Seconds(); elapsed > 0 {
			post("done", fmt.Sprintf("%d tokens, %.1f tokens/s", count, float64(count)/elapsed))
			return
		}
		post("done", fmt.Sprintf("%d tokens", count))
	}()

	return nil
}

// prompt puts the conversation into the chat format of the model and tokenizes it.
// It drops the oldest turns until what is left has room for an answer.
func prompt(maxTokens int32) []llamawasm.Token {
	for {
		var text strings.Builder
		for i, message := range history {
			formatted, err := llamawasm.ChatApplyTemplate(model, message.Role, message.Content, i == len(history)-1)
			if err != nil {
				// The model has no template, or llama.cpp is too old to have
				// the call. The bare question is all that is left.
				return llamawasm.Tokenize(vocab, history[len(history)-1].Content, true, false)
			}
			text.WriteString(formatted)
		}

		// parseSpecial has to be true, or the markers of the template
		// tokenize as ordinary text and the model sees no chat.
		tokens := llamawasm.Tokenize(vocab, text.String(), true, true)

		if int32(len(tokens)) <= nCtx-maxTokens || len(history) <= 2 {
			return tokens
		}

		// Too long. Drop the oldest exchange, keeping the system message.
		drop := 2
		if len(history) < 1+drop {
			drop = len(history) - 1
		}
		history = append(history[:1], history[1+drop:]...)
	}
}

// post sends a message to the page in a Web Worker, or to the harness in Node.
func post(kind, text string) {
	message := map[string]any{"kind": kind, "text": text}

	if fn := js.Global().Get("postMessage"); fn.Type() == js.TypeFunction {
		fn.Invoke(message)
		return
	}
	if fn := js.Global().Get("yzmaOnMessage"); fn.Type() == js.TypeFunction {
		fn.Invoke(message)
		return
	}
	fmt.Printf("%s: %s\n", kind, text)
}
