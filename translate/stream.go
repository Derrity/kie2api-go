package translate

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/Derrity/kie2api-go/sse"
)

// StreamFormat identifies which SSE shape a producer/consumer expects.
type StreamFormat int

const (
	FmtOpenAIChat StreamFormat = iota
	FmtOpenAIResponses
	FmtAnthropic
)

// TranslateStream reads SSE events from `body` (in `from` format) and writes
// translated events to `out` (in `to` format). When from == to, prefer
// sse.Passthrough on the raw body for fidelity instead of calling this.
//
// Tool-use streaming across protocols is best-effort: deltas surface as text
// fragments noting the tool, while the final stop event carries the right
// stop reason.
func TranslateStream(body io.Reader, from, to StreamFormat, modelID string, out *sse.Writer) error {
	// Extract a stream of canonical events:
	//   - text delta
	//   - tool start / tool args delta / tool stop  (kept simple)
	//   - usage info
	//   - end of stream
	// then re-emit in the target format.
	emitter := newEmitter(to, modelID, out)
	if err := emitter.start(); err != nil {
		return err
	}
	switch from {
	case FmtOpenAIChat:
		if err := drainOpenAIChat(body, emitter); err != nil {
			return err
		}
	case FmtOpenAIResponses:
		if err := drainResponses(body, emitter); err != nil {
			return err
		}
	case FmtAnthropic:
		if err := drainAnthropic(body, emitter); err != nil {
			return err
		}
	}
	return emitter.end()
}

// ----- canonical emitter -----

type streamEmitter struct {
	target     StreamFormat
	modelID    string
	w          *sse.Writer
	id         string
	startedCB  bool // anthropic content block started
	usage      map[string]any
	stopReason string

	// Tool call tracking. Keyed by upstream item id so a single delta event
	// can be matched to the call it belongs to.
	toolByItem map[string]*toolCall
	toolOrder  []*toolCall
	haveTools  bool
}

type toolCall struct {
	index     int
	itemID    string
	callID    string
	name      string
	startedCB bool // anthropic tool_use block started
}

func newEmitter(target StreamFormat, modelID string, w *sse.Writer) *streamEmitter {
	return &streamEmitter{target: target, modelID: modelID, w: w, id: "msg_" + randID(12), toolByItem: map[string]*toolCall{}}
}

// toolCallStart emits the leading delta announcing a new function tool call.
func (e *streamEmitter) toolCallStart(itemID, callID, name string) error {
	if _, ok := e.toolByItem[itemID]; ok {
		return nil
	}
	tc := &toolCall{index: len(e.toolOrder), itemID: itemID, callID: callID, name: name}
	e.toolByItem[itemID] = tc
	e.toolOrder = append(e.toolOrder, tc)
	e.haveTools = true
	switch e.target {
	case FmtOpenAIChat:
		chunk := map[string]any{
			"id":      "chatcmpl-" + randID(12),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   e.modelID,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index":    tc.index,
						"id":       callID,
						"type":     "function",
						"function": map[string]any{"name": name, "arguments": ""},
					}},
				},
			}},
		}
		b, _ := json.Marshal(chunk)
		return e.w.Write("", string(b))
	case FmtAnthropic:
		idx := tc.index
		if e.startedCB {
			idx = tc.index + 1
		}
		start := map[string]any{
			"type":  "content_block_start",
			"index": idx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    callID,
				"name":  name,
				"input": map[string]any{},
			},
		}
		tc.startedCB = true
		b, _ := json.Marshal(start)
		return e.w.Write("content_block_start", string(b))
	case FmtOpenAIResponses:
		ev := map[string]any{
			"type":         "response.output_item.added",
			"output_index": tc.index,
			"item": map[string]any{
				"id":        itemID,
				"type":      "function_call",
				"call_id":   callID,
				"name":      name,
				"arguments": "",
			},
		}
		b, _ := json.Marshal(ev)
		return e.w.Write("response.output_item.added", string(b))
	}
	return nil
}

func (e *streamEmitter) toolCallArgsDelta(itemID, delta string) error {
	if delta == "" {
		return nil
	}
	tc := e.toolByItem[itemID]
	if tc == nil {
		return nil
	}
	switch e.target {
	case FmtOpenAIChat:
		chunk := map[string]any{
			"id":      "chatcmpl-" + randID(12),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   e.modelID,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index":    tc.index,
						"function": map[string]any{"arguments": delta},
					}},
				},
			}},
		}
		b, _ := json.Marshal(chunk)
		return e.w.Write("", string(b))
	case FmtAnthropic:
		idx := tc.index
		if e.startedCB {
			idx = tc.index + 1
		}
		d := map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": delta},
		}
		b, _ := json.Marshal(d)
		return e.w.Write("content_block_delta", string(b))
	case FmtOpenAIResponses:
		ev := map[string]any{
			"type":    "response.function_call_arguments.delta",
			"item_id": itemID,
			"delta":   delta,
		}
		b, _ := json.Marshal(ev)
		return e.w.Write("response.function_call_arguments.delta", string(b))
	}
	return nil
}

func (e *streamEmitter) start() error {
	switch e.target {
	case FmtOpenAIChat:
		// emit a leading role chunk
		chunk := map[string]any{
			"id":      "chatcmpl-" + randID(12),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   e.modelID,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"role": "assistant"},
			}},
		}
		b, _ := json.Marshal(chunk)
		return e.w.Write("", string(b))
	case FmtAnthropic:
		msgStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            e.id,
				"type":          "message",
				"role":          "assistant",
				"model":         e.modelID,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		}
		b, _ := json.Marshal(msgStart)
		return e.w.Write("message_start", string(b))
	case FmtOpenAIResponses:
		// optional: emit response.created
		ev := map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id":     "resp_" + randID(12),
				"model":  e.modelID,
				"status": "in_progress",
			},
		}
		b, _ := json.Marshal(ev)
		return e.w.Write("response.created", string(b))
	}
	return nil
}

func (e *streamEmitter) textDelta(text string) error {
	if text == "" {
		return nil
	}
	switch e.target {
	case FmtOpenAIChat:
		chunk := map[string]any{
			"id":      "chatcmpl-" + randID(12),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   e.modelID,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"content": text},
			}},
		}
		b, _ := json.Marshal(chunk)
		return e.w.Write("", string(b))
	case FmtAnthropic:
		if !e.startedCB {
			start := map[string]any{
				"type":          "content_block_start",
				"index":         0,
				"content_block": map[string]any{"type": "text", "text": ""},
			}
			b, _ := json.Marshal(start)
			if err := e.w.Write("content_block_start", string(b)); err != nil {
				return err
			}
			e.startedCB = true
		}
		delta := map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text},
		}
		b, _ := json.Marshal(delta)
		return e.w.Write("content_block_delta", string(b))
	case FmtOpenAIResponses:
		ev := map[string]any{
			"type":  "response.output_text.delta",
			"delta": text,
		}
		b, _ := json.Marshal(ev)
		return e.w.Write("response.output_text.delta", string(b))
	}
	return nil
}

func (e *streamEmitter) setUsage(u map[string]any) {
	if u != nil {
		e.usage = u
	}
}

func (e *streamEmitter) setStopReason(r string) {
	if r != "" {
		e.stopReason = r
	}
}

func (e *streamEmitter) end() error {
	switch e.target {
	case FmtOpenAIChat:
		fr := e.stopReason
		if fr == "" {
			if e.haveTools {
				fr = "tool_calls"
			} else {
				fr = "stop"
			}
		} else {
			fr = mapStopReason(fr)
		}
		final := map[string]any{
			"id":      "chatcmpl-" + randID(12),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   e.modelID,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": fr,
			}},
		}
		b, _ := json.Marshal(final)
		if err := e.w.Write("", string(b)); err != nil {
			return err
		}
		return e.w.Done()
	case FmtAnthropic:
		if e.startedCB {
			stop := map[string]any{"type": "content_block_stop", "index": 0}
			b, _ := json.Marshal(stop)
			if err := e.w.Write("content_block_stop", string(b)); err != nil {
				return err
			}
		}
		stopReason := e.stopReason
		if stopReason == "" {
			stopReason = "end_turn"
		}
		md := map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		}
		if e.usage != nil {
			md["usage"] = e.usage
		}
		b, _ := json.Marshal(md)
		if err := e.w.Write("message_delta", string(b)); err != nil {
			return err
		}
		return e.w.Write("message_stop", `{"type":"message_stop"}`)
	case FmtOpenAIResponses:
		usage := e.usage
		if usage == nil {
			usage = map[string]any{}
		}
		ev := map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":     "resp_" + randID(12),
				"model":  e.modelID,
				"status": "completed",
				"usage":  usage,
			},
		}
		b, _ := json.Marshal(ev)
		if err := e.w.Write("response.completed", string(b)); err != nil {
			return err
		}
		return e.w.Done()
	}
	return nil
}

// ----- drainers (parse upstream SSE) -----

func drainOpenAIChat(body io.Reader, e *streamEmitter) error {
	r := sse.NewReader(body)
	for {
		ev, err := r.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if ev == nil {
			continue
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			if data == "[DONE]" {
				return nil
			}
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			continue
		}
		choices, _ := obj["choices"].([]any)
		for _, c := range choices {
			cm, _ := c.(map[string]any)
			if d, ok := cm["delta"].(map[string]any); ok {
				if t, ok := d["content"].(string); ok && t != "" {
					if err := e.textDelta(t); err != nil {
						return err
					}
				}
			}
			if fr, ok := cm["finish_reason"].(string); ok && fr != "" {
				e.setStopReason(mapFinishReasonToAnthropic(fr, ""))
			}
		}
		if u, ok := obj["usage"].(map[string]any); ok {
			e.setUsage(map[string]any{
				"input_tokens":  numInt(u["prompt_tokens"]),
				"output_tokens": numInt(u["completion_tokens"]),
			})
		}
	}
}

func drainResponses(body io.Reader, e *streamEmitter) error {
	r := sse.NewReader(body)
	for {
		ev, err := r.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if ev == nil {
			continue
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			if data == "[DONE]" {
				return nil
			}
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			continue
		}
		switch asString(obj["type"]) {
		case "response.output_text.delta":
			if err := e.textDelta(asString(obj["delta"])); err != nil {
				return err
			}
		case "response.output_item.added":
			item, _ := obj["item"].(map[string]any)
			if item != nil && asString(item["type"]) == "function_call" {
				itemID := asString(item["id"])
				callID := asString(item["call_id"])
				if callID == "" {
					callID = itemID
				}
				if err := e.toolCallStart(itemID, callID, asString(item["name"])); err != nil {
					return err
				}
				if args := asString(item["arguments"]); args != "" {
					if err := e.toolCallArgsDelta(itemID, args); err != nil {
						return err
					}
				}
			}
		case "response.function_call_arguments.delta":
			itemID := asString(obj["item_id"])
			if err := e.toolCallArgsDelta(itemID, asString(obj["delta"])); err != nil {
				return err
			}
		case "response.completed":
			if rsp, ok := obj["response"].(map[string]any); ok {
				if u, ok := rsp["usage"].(map[string]any); ok {
					e.setUsage(map[string]any{
						"input_tokens":  numInt(u["input_tokens"]),
						"output_tokens": numInt(u["output_tokens"]),
					})
				}
			}
		}
	}
}

func drainAnthropic(body io.Reader, e *streamEmitter) error {
	r := sse.NewReader(body)
	for {
		ev, err := r.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if ev == nil {
			continue
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			continue
		}
		switch asString(obj["type"]) {
		case "content_block_delta":
			if d, ok := obj["delta"].(map[string]any); ok {
				if asString(d["type"]) == "text_delta" {
					if err := e.textDelta(asString(d["text"])); err != nil {
						return err
					}
				}
			}
		case "message_delta":
			if d, ok := obj["delta"].(map[string]any); ok {
				if sr := asString(d["stop_reason"]); sr != "" {
					e.setStopReason(sr)
				}
			}
			if u, ok := obj["usage"].(map[string]any); ok {
				e.setUsage(map[string]any{
					"input_tokens":  numInt(u["input_tokens"]),
					"output_tokens": numInt(u["output_tokens"]),
				})
			}
		case "message_stop":
			return nil
		}
	}
}
