package translate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// AnthropicResponseToOpenAIChat converts a non-streaming Anthropic Messages
// response into an OpenAI Chat Completions response body.
func AnthropicResponseToOpenAIChat(anth map[string]any, modelID string) map[string]any {
	id := asString(anth["id"])
	if id == "" {
		id = "chatcmpl-" + randID(12)
	}
	var text string
	var toolCalls []map[string]any
	if blocks, ok := anth["content"].([]any); ok {
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			switch bm["type"] {
			case "text":
				text += asString(bm["text"])
			case "tool_use":
				args, _ := json.Marshal(bm["input"])
				toolCalls = append(toolCalls, map[string]any{
					"id":   asString(bm["id"]),
					"type": "function",
					"function": map[string]any{
						"name":      asString(bm["name"]),
						"arguments": string(args),
					},
				})
			}
		}
	}
	msg := map[string]any{"role": "assistant", "content": text}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	finish := mapStopReason(asString(anth["stop_reason"]))
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
	}
	if usage, ok := anth["usage"].(map[string]any); ok {
		in, _ := usage["input_tokens"].(json.Number)
		out, _ := usage["output_tokens"].(json.Number)
		inI, _ := in.Int64()
		outI, _ := out.Int64()
		// fall back when not json.Number
		if inI == 0 {
			if v, ok := usage["input_tokens"].(float64); ok {
				inI = int64(v)
			}
		}
		if outI == 0 {
			if v, ok := usage["output_tokens"].(float64); ok {
				outI = int64(v)
			}
		}
		resp["usage"] = map[string]any{
			"prompt_tokens":     inI,
			"completion_tokens": outI,
			"total_tokens":      inI + outI,
		}
	}
	return resp
}

// OpenAIChatResponseToAnthropic converts a Chat Completions response body
// into an Anthropic Messages response body.
func OpenAIChatResponseToAnthropic(chat map[string]any, modelID string) map[string]any {
	id := asString(chat["id"])
	if id == "" {
		id = "msg_" + randID(16)
	}
	choices, _ := chat["choices"].([]any)
	var text string
	var content []map[string]any
	stopReason := "end_turn"
	if len(choices) > 0 {
		ch, _ := choices[0].(map[string]any)
		msg, _ := ch["message"].(map[string]any)
		if c, ok := msg["content"].(string); ok {
			text = c
		}
		if tcs, ok := msg["tool_calls"].([]any); ok && len(tcs) > 0 {
			for _, tc := range tcs {
				tcm, _ := tc.(map[string]any)
				fn, _ := tcm["function"].(map[string]any)
				var input any
				if argStr, ok := fn["arguments"].(string); ok && argStr != "" {
					_ = json.Unmarshal([]byte(argStr), &input)
				}
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    asString(tcm["id"]),
					"name":  asString(fn["name"]),
					"input": input,
				})
			}
			stopReason = "tool_use"
		}
		if fr := asString(ch["finish_reason"]); fr != "" {
			stopReason = mapFinishReasonToAnthropic(fr, stopReason)
		}
	}
	if text != "" {
		content = append([]map[string]any{{"type": "text", "text": text}}, content...)
	}
	resp := map[string]any{
		"id":          id,
		"type":        "message",
		"role":        "assistant",
		"model":       modelID,
		"content":     content,
		"stop_reason": stopReason,
	}
	if usage, ok := chat["usage"].(map[string]any); ok {
		resp["usage"] = map[string]any{
			"input_tokens":  numInt(usage["prompt_tokens"]),
			"output_tokens": numInt(usage["completion_tokens"]),
		}
	}
	return resp
}

// ResponsesResponseToOpenAIChat converts a buffered /api/v1/responses body
// to a Chat Completions response.
func ResponsesResponseToOpenAIChat(resp map[string]any, modelID string) map[string]any {
	id := asString(resp["id"])
	if id == "" {
		id = "chatcmpl-" + randID(12)
	}
	var text string
	var toolCalls []map[string]any
	if out, ok := resp["output"].([]any); ok {
		for _, item := range out {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch asString(im["type"]) {
			case "message":
				if cs, ok := im["content"].([]any); ok {
					for _, c := range cs {
						cm, ok := c.(map[string]any)
						if !ok {
							continue
						}
						if cm["type"] == "output_text" {
							text += asString(cm["text"])
						}
					}
				}
			case "function_call":
				callID := asString(im["call_id"])
				if callID == "" {
					callID = asString(im["id"])
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      asString(im["name"]),
						"arguments": asString(im["arguments"]),
					},
				})
			}
		}
	}
	msg := map[string]any{"role": "assistant", "content": text}
	finish := "stop"
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		finish = "tool_calls"
	}
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
	}
	if usage, ok := resp["usage"].(map[string]any); ok {
		out["usage"] = map[string]any{
			"prompt_tokens":     numInt(usage["input_tokens"]),
			"completion_tokens": numInt(usage["output_tokens"]),
			"total_tokens":      numInt(usage["total_tokens"]),
		}
	}
	return out
}

// OpenAIChatResponseToResponses converts a Chat Completions response into a
// /v1/responses-shaped response.
func OpenAIChatResponseToResponses(chat map[string]any, modelID string) map[string]any {
	choices, _ := chat["choices"].([]any)
	var text string
	if len(choices) > 0 {
		ch, _ := choices[0].(map[string]any)
		msg, _ := ch["message"].(map[string]any)
		text = asString(msg["content"])
	}
	id := asString(chat["id"])
	if id == "" {
		id = "resp_" + randID(16)
	}
	out := map[string]any{
		"id":     id,
		"object": "response",
		"model":  modelID,
		"output": []map[string]any{{
			"type":   "message",
			"role":   "assistant",
			"id":     "msg_" + randID(12),
			"status": "completed",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		}},
		"status": "completed",
	}
	if usage, ok := chat["usage"].(map[string]any); ok {
		out["usage"] = map[string]any{
			"input_tokens":  numInt(usage["prompt_tokens"]),
			"output_tokens": numInt(usage["completion_tokens"]),
			"total_tokens":  numInt(usage["total_tokens"]),
		}
	}
	return out
}

// AnthropicResponseToResponses chains Anthropic -> Chat -> Responses.
func AnthropicResponseToResponses(anth map[string]any, modelID string) map[string]any {
	chat := AnthropicResponseToOpenAIChat(anth, modelID)
	return OpenAIChatResponseToResponses(chat, modelID)
}

// ResponsesResponseToAnthropic chains Responses -> Chat -> Anthropic.
func ResponsesResponseToAnthropic(resp map[string]any, modelID string) map[string]any {
	chat := ResponsesResponseToOpenAIChat(resp, modelID)
	return OpenAIChatResponseToAnthropic(chat, modelID)
}

// ----- helpers -----

func mapStopReason(anth string) string {
	switch anth {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence":
		return "stop"
	default:
		if anth == "" {
			return "stop"
		}
		return anth
	}
}

func mapFinishReasonToAnthropic(fr, fallback string) string {
	switch fr {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return fallback
	}
}

func numInt(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		i, _ := t.Int64()
		return i
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	}
	return 0
}

func randID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
