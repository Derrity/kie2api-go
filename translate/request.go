// Package translate converts between OpenAI Chat Completions, OpenAI Responses,
// and Anthropic Messages request/response shapes.
//
// The translators here aim for "good enough for chat + tools + streaming text".
// They do not attempt to perfectly round-trip every edge case; complex tool-use
// streams may degrade to non-streaming buffered responses on the cross-protocol
// paths.
package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ----- common helpers -----

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// flattenContent collapses an OpenAI Chat content (string or array of parts)
// into a plain string. Image parts are referenced by URL.
func flattenContent(content any) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	if arr, ok := content.([]any); ok {
		var b strings.Builder
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text", "input_text":
				b.WriteString(asString(m["text"]))
			case "image_url", "input_image":
				if iu, ok := m["image_url"].(map[string]any); ok {
					b.WriteString("[image: ")
					b.WriteString(asString(iu["url"]))
					b.WriteString("]")
				} else if u, ok := m["image_url"].(string); ok {
					b.WriteString("[image: ")
					b.WriteString(u)
					b.WriteString("]")
				}
			}
		}
		return b.String()
	}
	return asString(content)
}

// ----- OpenAI Chat <-> Anthropic Messages (request) -----

// OpenAIChatToAnthropic converts an OpenAI Chat Completions request body
// to an Anthropic Messages request body. The caller supplies the upstream
// Anthropic model id.
func OpenAIChatToAnthropic(req map[string]any, upstreamModel string) map[string]any {
	out := map[string]any{
		"model": upstreamModel,
	}
	var systemParts []string
	var msgs []map[string]any
	if raw, ok := req["messages"].([]any); ok {
		for _, m := range raw {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role := asString(mm["role"])
			text := flattenContent(mm["content"])
			switch role {
			case "system", "developer":
				if text != "" {
					systemParts = append(systemParts, text)
				}
			case "tool":
				// Encode tool result as user message containing tool_result
				toolUseID := asString(mm["tool_call_id"])
				msgs = append(msgs, map[string]any{
					"role": "user",
					"content": []any{map[string]any{
						"type":        "tool_result",
						"tool_use_id": toolUseID,
						"content":     text,
					}},
				})
			default:
				if role != "user" && role != "assistant" {
					role = "user"
				}
				if text != "" {
					msgs = append(msgs, map[string]any{
						"role":    role,
						"content": text,
					})
				}
			}
		}
	}
	if len(systemParts) > 0 {
		out["system"] = strings.Join(systemParts, "\n\n")
	}
	out["messages"] = msgs
	if v, ok := req["max_tokens"]; ok {
		out["max_tokens"] = v
	} else {
		out["max_tokens"] = 4096
	}
	if v, ok := req["temperature"]; ok {
		out["temperature"] = v
	}
	if v, ok := req["top_p"]; ok {
		out["top_p"] = v
	}
	if v, ok := req["stream"].(bool); ok {
		out["stream"] = v
	}
	// Tools translation (best-effort).
	if rawTools, ok := req["tools"].([]any); ok {
		var anth []map[string]any
		for _, t := range rawTools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tm["function"].(map[string]any)
			if fn == nil {
				continue
			}
			anth = append(anth, map[string]any{
				"name":         asString(fn["name"]),
				"description":  asString(fn["description"]),
				"input_schema": fn["parameters"],
			})
		}
		if len(anth) > 0 {
			out["tools"] = anth
		}
	}
	return out
}

// AnthropicToOpenAIChat converts an Anthropic Messages request to an OpenAI
// Chat Completions request shape (so we can forward it to a Chat-style upstream).
func AnthropicToOpenAIChat(req map[string]any, upstreamModel string) map[string]any {
	out := map[string]any{
		"model": upstreamModel,
	}
	var msgs []map[string]any
	if sys, ok := req["system"]; ok {
		s := flattenContent(sys)
		if s != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": s})
		}
	}
	if raw, ok := req["messages"].([]any); ok {
		for _, m := range raw {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role := asString(mm["role"])
			content := mm["content"]
			// content might be string or array of blocks
			if s, ok := content.(string); ok {
				msgs = append(msgs, map[string]any{"role": role, "content": s})
				continue
			}
			arr, _ := content.([]any)
			var b strings.Builder
			for _, block := range arr {
				bm, ok := block.(map[string]any)
				if !ok {
					continue
				}
				switch bm["type"] {
				case "text":
					b.WriteString(asString(bm["text"]))
				case "tool_result":
					b.WriteString(asString(bm["content"]))
				}
			}
			if b.Len() > 0 {
				msgs = append(msgs, map[string]any{"role": role, "content": b.String()})
			}
		}
	}
	out["messages"] = msgs
	if v, ok := req["temperature"]; ok {
		out["temperature"] = v
	}
	if v, ok := req["top_p"]; ok {
		out["top_p"] = v
	}
	if v, ok := req["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	if v, ok := req["stream"].(bool); ok {
		out["stream"] = v
	}
	if rawTools, ok := req["tools"].([]any); ok {
		var oai []map[string]any
		for _, t := range rawTools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			oai = append(oai, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        asString(tm["name"]),
					"description": asString(tm["description"]),
					"parameters":  tm["input_schema"],
				},
			})
		}
		if len(oai) > 0 {
			out["tools"] = oai
		}
	}
	return out
}

// ----- OpenAI Chat <-> OpenAI Responses (request) -----

// OpenAIChatToResponses converts a Chat Completions request to a Responses
// request shape (model + input array + reasoning + tools).
//
// Chat-style assistant messages with `tool_calls` and `role:tool` results are
// translated to Responses API native items (`function_call` and
// `function_call_output`) so multi-turn tool use round-trips correctly.
func OpenAIChatToResponses(req map[string]any, upstreamModel string) map[string]any {
	out := map[string]any{
		"model": upstreamModel,
	}
	var input []map[string]any
	if raw, ok := req["messages"].([]any); ok {
		for _, m := range raw {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role := asString(mm["role"])
			if role == "" {
				role = "user"
			}

			// Tool result message → function_call_output item.
			if role == "tool" {
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": asString(mm["tool_call_id"]),
					"output":  flattenContent(mm["content"]),
				})
				continue
			}

			// Assistant message that ONLY contains tool_calls (no text content) →
			// emit one function_call item per tool_call. Mixed content+tool_calls
			// falls through and we emit both: a message AND function_call items.
			toolCalls, _ := mm["tool_calls"].([]any)

			// Build content parts for any text/image content.
			parts := []map[string]any{}
			content := mm["content"]
			if s, ok := content.(string); ok && s != "" {
				if role == "assistant" {
					parts = append(parts, map[string]any{"type": "output_text", "text": s})
				} else {
					parts = append(parts, map[string]any{"type": "input_text", "text": s})
				}
			} else if arr, ok := content.([]any); ok {
				for _, item := range arr {
					itm, ok := item.(map[string]any)
					if !ok {
						continue
					}
					switch itm["type"] {
					case "text", "input_text", "output_text":
						txt := asString(itm["text"])
						if txt == "" {
							continue
						}
						if role == "assistant" {
							parts = append(parts, map[string]any{"type": "output_text", "text": txt})
						} else {
							parts = append(parts, map[string]any{"type": "input_text", "text": txt})
						}
					case "image_url", "input_image":
						url := ""
						if iu, ok := itm["image_url"].(map[string]any); ok {
							url = asString(iu["url"])
						} else if u, ok := itm["image_url"].(string); ok {
							url = u
						}
						if url != "" {
							parts = append(parts, map[string]any{"type": "input_image", "image_url": url})
						}
					}
				}
			}
			if len(parts) > 0 {
				input = append(input, map[string]any{"role": role, "content": parts})
			} else if len(toolCalls) == 0 {
				// Fall back to a flattened text representation if we can't classify
				// the content but the message has SOMETHING in it.
				if flat := flattenContent(content); flat != "" {
					p := map[string]any{"type": "input_text", "text": flat}
					if role == "assistant" {
						p["type"] = "output_text"
					}
					input = append(input, map[string]any{"role": role, "content": []map[string]any{p}})
				}
			}

			// Emit function_call items for each tool call on this assistant turn.
			for _, tc := range toolCalls {
				tcm, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := tcm["function"].(map[string]any)
				if fn == nil {
					continue
				}
				args := asString(fn["arguments"])
				if args == "" {
					args = "{}"
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   asString(tcm["id"]),
					"name":      asString(fn["name"]),
					"arguments": args,
				})
			}
		}
	}
	out["input"] = input
	if v, ok := req["stream"].(bool); ok {
		out["stream"] = v
	}
	if effort, ok := req["reasoning_effort"]; ok {
		out["reasoning"] = map[string]any{"effort": asString(effort)}
	}
	if rawTools, ok := req["tools"].([]any); ok {
		var rt []map[string]any
		for _, t := range rawTools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tm["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name := asString(fn["name"])
			if name == "googleSearch" || name == "web_search" {
				rt = append(rt, map[string]any{"type": "web_search"})
				continue
			}
			rt = append(rt, map[string]any{
				"type":        "function",
				"name":        name,
				"description": asString(fn["description"]),
				"parameters":  fn["parameters"],
			})
		}
		if len(rt) > 0 {
			out["tools"] = rt
		}
	}
	return out
}

// AnthropicToResponses converts an Anthropic Messages request to a Responses request.
func AnthropicToResponses(req map[string]any, upstreamModel string) map[string]any {
	chat := AnthropicToOpenAIChat(req, upstreamModel)
	return OpenAIChatToResponses(chat, upstreamModel)
}

// ResponsesToOpenAIChat converts a Responses request body shape into a Chat
// Completions body (used when a /v1/responses-style client targets a Chat upstream).
func ResponsesToOpenAIChat(req map[string]any, upstreamModel string) map[string]any {
	out := map[string]any{"model": upstreamModel}
	var msgs []map[string]any
	switch input := req["input"].(type) {
	case string:
		msgs = append(msgs, map[string]any{"role": "user", "content": input})
	case []any:
		for _, m := range input {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role := asString(mm["role"])
			if role == "" {
				role = "user"
			}
			parts, _ := mm["content"].([]any)
			var b strings.Builder
			for _, p := range parts {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				switch pm["type"] {
				case "input_text", "text":
					b.WriteString(asString(pm["text"]))
				case "input_image":
					if u := asString(pm["image_url"]); u != "" {
						b.WriteString("[image: " + u + "]")
					}
				}
			}
			msgs = append(msgs, map[string]any{"role": role, "content": b.String()})
		}
	}
	out["messages"] = msgs
	if v, ok := req["stream"].(bool); ok {
		out["stream"] = v
	}
	if rs, ok := req["reasoning"].(map[string]any); ok {
		if e := asString(rs["effort"]); e != "" {
			out["reasoning_effort"] = e
		}
	}
	return out
}

// ResponsesToAnthropic uses the chat representation as an intermediate.
func ResponsesToAnthropic(req map[string]any, upstreamModel string) map[string]any {
	chat := ResponsesToOpenAIChat(req, upstreamModel)
	return OpenAIChatToAnthropic(chat, upstreamModel)
}

// ErrUntranslatable is returned when a request shape cannot be translated.
var ErrUntranslatable = fmt.Errorf("request cannot be translated to target protocol")
