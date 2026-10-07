//go:build js && wasm

// Package main is a chat that runs a language model in the browser.
// It drives llama.cpp through yzma and streams the answer to the page.
package main

import (
	"fmt"
	"strings"
	"syscall/js"
	"time"
	"unicode/utf8"

	"github.com/hybridgroup/yzma/pkg/llamawasm"
)

const (
	// modelPath is where the model goes in the llama.cpp filesystem.
	modelPath = "/models/model.gguf"

	// defaultSystem is the system message until the page sends another one.
	defaultSystem = "You are a helpful assistant running inside a web browser. Keep your answers short."

	nCtx   = 4096
	nBatch = 512

	// defaultMaxTokens caps one answer. The page can ask for a different limit.
	defaultMaxTokens = 512

	// thinkingMaxTokens caps an answer with thinking. It is much larger
	// because the thoughts come before the reply.
	thinkingMaxTokens = 1536

	// thinkOpen and thinkClose mark the thoughts of a reasoning model.
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// turn is one message in the conversation.
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

	// reasons is true when the model chat template supports thinking.
	// thinking is what the page asks for.
	reasons  bool
	thinking bool
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
	js.Global().Set("yzmaSetThinking", js.FuncOf(setThinking))

	post("system", defaultSystem)
	post("ready", backendReport())

	// Keep the program alive so that the page can call into it.
	<-make(chan struct{})
}

// loadModel(url) downloads a model and creates a context for it.
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

// openModel(path) loads a model that is already in the llama.cpp filesystem.
// The page downloads its model, but the test writes the file there directly.
func openModel(this js.Value, args []js.Value) any {
	path := modelPath
	if len(args) > 0 && args[0].Truthy() {
		path = args[0].String()
	}

	go open(path)

	return nil
}

// open loads the model at path and creates a context.
func open(path string) {
	post("status", "loading the model")

	params := llamawasm.ModelDefaultParams()

	// The WebGPU build has a device, so put every layer on it.
	// The CPU builds have none, and the value is ignored.
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

	// The sampler is tied to the model, and each answer creates a new one.
	freeSampler()

	history = history[:1]

	template := llamawasm.ModelChatTemplate(model, "")

	// Only a template that writes the marker itself belongs to a reasoning
	// model. For other models such as LFM2.5 the thinking toggle is ignored,
	// because an unknown marker makes the answer worse.
	reasons = strings.Contains(template, thinkOpen)

	// A base model has no chat template and gives poor chat answers.
	if template == "" {
		post("status", "this model has no chat template, so the answers will wander")
	}

	post("loaded", llamawasm.ModelDesc(model)+", "+backendReport())
}

// makeSampler creates the sampler chain for one answer. It gives the variety
// a chat needs, which the greedy sampler does not.
//
// Each answer gets a new seed, so asking the same question twice does not
// give the same words twice.
func makeSampler() {
	freeSampler()

	sampler = llamawasm.SamplerChainInit(llamawasm.SamplerChainDefaultParams())
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitPenalties(llamawasm.VocabNTokens(vocab), 64, 1.1, 0, 0))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitTopK(40))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitTopP(0.95, 1))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitTemp(0.7))
	llamawasm.SamplerChainAdd(sampler, llamawasm.SamplerInitDist(uint32(time.Now().UnixNano())))
}

// freeSampler frees the sampler chain of the previous answer.
func freeSampler() {
	if sampler != 0 {
		llamawasm.SamplerFree(sampler)
		sampler = 0
	}
}

// backendReport describes the backend that runs the model.
func backendReport() string {
	if device := llamawasm.GPUDevice(); device != "" {
		return fmt.Sprintf("backend: %s (%s)", llamawasm.Backend(), device)
	}
	return fmt.Sprintf("backend: %s, %d threads", llamawasm.Backend(), llamawasm.Threads())
}

// setSystem(text) replaces the system message, starting with the next answer.
// An empty text restores the default.
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

// setThinking(on) sets whether a reasoning model thinks before it answers.
// Other models are not affected.
func setThinking(this js.Value, args []js.Value) any {
	thinking = len(args) > 0 && args[0].Truthy()
	return nil
}

// reset() clears the conversation and keeps only the system message.
func reset(this js.Value, args []js.Value) any {
	history = history[:1]
	post("reset", "")
	return nil
}

// ask(text, maxTokens) adds a question and streams the answer to the page.
func ask(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		post("error", "ask needs a question")
		return nil
	}
	question := args[0].String()

	maxTokens := int32(defaultMaxTokens)
	if reasons && thinking {
		maxTokens = thinkingMaxTokens
	}
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

		// Each turn sends the whole conversation again, so clear the state
		// of the last turn.
		if err := llamawasm.MemoryClear(ctx, true); err != nil {
			post("error", err.Error())
			return
		}
		makeSampler()

		var (
			// The prompt opens the thinking block, so the answer starts
			// inside it.
			answer = splitter{think: reasons && thinking}
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

			if n := llamawasm.TokenToPiece(vocab, token, buf, 0, true); n > 0 {
				answer.write(string(buf[:n]))
			}

			count++
			batch = llamawasm.BatchGetOne([]llamawasm.Token{token})
		}

		answer.close()

		// Only the reply goes into the history. The model template drops
		// the thoughts of earlier turns anyway.
		history = append(history, turn{Role: "assistant", Content: answer.reply.String()})

		// A model that stops at the first token gives nothing to read. Report
		// it, because a count of zero alone looks like the page did nothing.
		if count == 0 {
			post("error", "the model gave no answer, so the backend may compute wrong values")
			return
		}

		// A run that spends every token on thinking leaves no reply, and an
		// empty answer alone looks like a failure.
		note := ""
		if reasons && thinking && answer.reply.Len() == 0 {
			note = ", the thoughts used every token"
		}

		if elapsed := time.Since(start).Seconds(); elapsed > 0 {
			post("done", fmt.Sprintf("%d tokens, %.1f tokens/s%s", count, float64(count)/elapsed, note))
			return
		}
		post("done", fmt.Sprintf("%d tokens%s", count, note))
	}()

	return nil
}

// prompt formats the conversation with the model chat template and tokenizes it.
// It drops the oldest turns until there is room for an answer.
func prompt(maxTokens int32) []llamawasm.Token {
	for {
		var text strings.Builder
		for i, message := range history {
			formatted, err := llamawasm.ChatApplyTemplate(model, message.Role, message.Content, i == len(history)-1)
			if err != nil {
				// The model has no template, or llama.cpp is too old for this
				// call. Fall back to the bare question.
				return llamawasm.Tokenize(vocab, history[len(history)-1].Content, true, false)
			}
			text.WriteString(formatted)
		}

		// parseSpecial must be true, or the template markers tokenize as
		// plain text and the model sees no chat.
		tokens := llamawasm.Tokenize(vocab, thoughtPrefill(text.String()), true, true)

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

// thoughtPrefill ends the prompt with the thinking block the page asks for.
//
// A template that supports thinking may write its own block, and Qwen3 writes
// an empty one when thinking is off. yzma renders one message at a time and
// cannot pass options to the template, so this removes that block and writes
// the one the page wants.
func thoughtPrefill(text string) string {
	if !reasons {
		return text
	}

	text = stripEmptyThoughts(text)

	// An open block makes the model think. An empty block makes it answer
	// right away.
	if thinking {
		return text + thinkOpen + "\n"
	}

	return text + thinkOpen + "\n\n" + thinkClose + "\n\n"
}

// stripEmptyThoughts removes an empty thinking block from the end of text.
func stripEmptyThoughts(text string) string {
	trimmed := strings.TrimRight(text, " \t\r\n")
	if !strings.HasSuffix(trimmed, thinkClose) {
		return text
	}

	body := trimmed[:len(trimmed)-len(thinkClose)]

	start := strings.LastIndex(body, thinkOpen)
	if start < 0 || strings.TrimSpace(body[start+len(thinkOpen):]) != "" {
		return text
	}

	return body[:start]
}

// splitter streams the answer to the page. It keeps the thoughts separate
// from the reply, because the page shows them differently and only the reply
// goes into the history.
type splitter struct {
	buf   string
	think bool
	reply strings.Builder
}

// write takes one piece of the answer and sends what is complete. It holds
// back the last few bytes, because a marker or a character can be split
// across two pieces.
func (s *splitter) write(piece string) {
	s.buf += piece

	for {
		marker := thinkOpen
		if s.think {
			marker = thinkClose
		}

		i := strings.Index(s.buf, marker)
		if i < 0 {
			break
		}

		s.emit(s.buf[:i])
		s.buf = s.buf[i+len(marker):]
		s.think = !s.think
	}

	if keep := len(thinkClose) - 1; len(s.buf) > keep {
		cut := len(s.buf) - keep

		// A multibyte character can be split across pieces, so move the cut
		// back to its first byte.
		for cut > 0 && !utf8.RuneStart(s.buf[cut]) {
			cut--
		}

		s.emit(s.buf[:cut])
		s.buf = s.buf[cut:]
	}
}

// close sends what is left.
func (s *splitter) close() {
	s.emit(s.buf)
	s.buf = ""
}

// emit sends one part of the answer as a think or token message.
func (s *splitter) emit(text string) {
	if text == "" {
		return
	}

	if s.think {
		post("think", text)
		return
	}

	// A reply that comes after the thoughts starts with blank lines.
	if s.reply.Len() == 0 {
		if text = strings.TrimLeft(text, " \t\r\n"); text == "" {
			return
		}
	}

	s.reply.WriteString(text)
	post("token", text)
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
