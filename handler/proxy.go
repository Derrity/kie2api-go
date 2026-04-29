package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Derrity/kie2api-go/config"
	"github.com/Derrity/kie2api-go/routing"
	"github.com/Derrity/kie2api-go/sse"
	"github.com/Derrity/kie2api-go/translate"
)

// ClientProto identifies the protocol the client used when calling us.
type ClientProto int

const (
	ClientOpenAIChat ClientProto = iota
	ClientOpenAIResponses
	ClientAnthropic
)

type Proxy struct {
	Store    *config.Store
	Upstream *http.Client
}

func debugUpstream() bool { return os.Getenv("KIE2API_DEBUG") != "" }

// ----- entry points -----

func (p *Proxy) HandleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	p.dispatch(w, r, ClientOpenAIChat)
}

func (p *Proxy) HandleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	p.dispatch(w, r, ClientOpenAIResponses)
}

func (p *Proxy) HandleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	p.dispatch(w, r, ClientAnthropic)
}

func (p *Proxy) HandleListModels(w http.ResponseWriter, r *http.Request) {
	cfg := p.Store.Get()
	now := time.Now().Unix()
	data := []map[string]any{}
	for _, id := range cfg.EnabledModels {
		if routing.Find(id) == nil {
			continue
		}
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  now,
			"owned_by": "kie.ai",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (p *Proxy) HandleCountTokens(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	// crude estimate: sum of message text lengths / 4
	var chars int
	if msgs, ok := req["messages"].([]any); ok {
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			chars += len(flattenText(mm["content"]))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": chars / 4})
}

func flattenText(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ----- dispatch -----

func (p *Proxy) dispatch(w http.ResponseWriter, r *http.Request, client ClientProto) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "failed to read body: "+err.Error())
		return
	}
	var req map[string]any
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	modelID := strings.TrimSpace(asString(req["model"]))
	if modelID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "missing required field: model")
		return
	}
	route := routing.Find(modelID)
	if route == nil {
		writeJSONError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("unknown model: %s", modelID))
		return
	}
	if !p.Store.IsModelEnabled(modelID) {
		writeJSONError(w, http.StatusForbidden, "model_disabled", fmt.Sprintf("model %s is not enabled in this kie2api instance", modelID))
		return
	}
	stream, _ := req["stream"].(bool)

	// Build upstream request body in the protocol expected by the upstream.
	var upstreamBody map[string]any
	switch route.Proto {
	case routing.ProtoOpenAIChat:
		upstreamBody = toUpstreamChat(client, req, route.UpstreamModel)
	case routing.ProtoOpenAIResponses:
		upstreamBody = toUpstreamResponses(client, req, route.UpstreamModel)
	case routing.ProtoAnthropic:
		upstreamBody = toUpstreamAnthropic(client, req, route.UpstreamModel)
	}
	if stream {
		upstreamBody["stream"] = true
	} else {
		// some upstreams default stream=true; force off when client didn't ask
		if _, ok := upstreamBody["stream"]; !ok {
			upstreamBody["stream"] = false
		}
	}

	// Dispatch.
	cfg := p.Store.Get()
	resp, err := p.callUpstream(r, cfg, route, upstreamBody)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	// Non-2xx: surface upstream body to client (best-effort same shape).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	if stream {
		p.handleStream(w, resp, client, route, modelID)
		return
	}
	p.handleNonStream(w, resp, client, route, modelID)
}

func (p *Proxy) callUpstream(r *http.Request, cfg config.Config, route *routing.ModelRoute, body map[string]any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if debugUpstream() {
		log.Printf("[upstream %s %s] req body: %s", route.Proto, route.UpstreamModel, truncate(string(buf), 2000))
	}
	url := strings.TrimRight(cfg.UpstreamBase, "/") + route.UpstreamPath
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.KIEAPIKey)
	// Anthropic upstream additionally accepts x-api-key
	if route.Proto == routing.ProtoAnthropic {
		req.Header.Set("x-api-key", cfg.KIEAPIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	return p.Upstream.Do(req)
}

func copyHeaders(w http.ResponseWriter, h http.Header) {
	for k, vs := range h {
		// avoid leaking transfer-encoding/content-length when re-emitting JSON
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

// ----- request shaping -----

func toUpstreamChat(client ClientProto, req map[string]any, upstreamModel string) map[string]any {
	switch client {
	case ClientOpenAIChat:
		// minor cleanup: replace model id with upstream's expected value
		out := cloneMap(req)
		out["model"] = upstreamModel
		return out
	case ClientOpenAIResponses:
		return translate.ResponsesToOpenAIChat(req, upstreamModel)
	case ClientAnthropic:
		return translate.AnthropicToOpenAIChat(req, upstreamModel)
	}
	return req
}

func toUpstreamResponses(client ClientProto, req map[string]any, upstreamModel string) map[string]any {
	switch client {
	case ClientOpenAIChat:
		return translate.OpenAIChatToResponses(req, upstreamModel)
	case ClientOpenAIResponses:
		out := cloneMap(req)
		out["model"] = upstreamModel
		// Normalize input: KIE Responses upstream rejects string content; coerce
		// to [{type:input_text,text:...}] form.
		if inp, ok := out["input"].([]any); ok {
			fixed := make([]any, 0, len(inp))
			for _, item := range inp {
				m, ok := item.(map[string]any)
				if !ok {
					fixed = append(fixed, item)
					continue
				}
				if s, ok := m["content"].(string); ok {
					m["content"] = []any{map[string]any{"type": "input_text", "text": s}}
				}
				fixed = append(fixed, m)
			}
			out["input"] = fixed
		} else if s, ok := out["input"].(string); ok {
			out["input"] = []any{map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": s}},
			}}
		}
		return out
	case ClientAnthropic:
		return translate.AnthropicToResponses(req, upstreamModel)
	}
	return req
}

func toUpstreamAnthropic(client ClientProto, req map[string]any, upstreamModel string) map[string]any {
	switch client {
	case ClientOpenAIChat:
		return translate.OpenAIChatToAnthropic(req, upstreamModel)
	case ClientOpenAIResponses:
		// Responses -> Anthropic via chat
		return translate.ResponsesToAnthropic(req, upstreamModel)
	case ClientAnthropic:
		out := cloneMap(req)
		out["model"] = upstreamModel
		return out
	}
	return req
}

// ----- response handling -----

func (p *Proxy) handleNonStream(w http.ResponseWriter, resp *http.Response, client ClientProto, route *routing.ModelRoute, modelID string) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	if debugUpstream() {
		log.Printf("[upstream %s %s] resp body: %s", route.Proto, modelID, truncate(string(body), 2000))
	}
	var upstream map[string]any
	if err := json.Unmarshal(body, &upstream); err != nil {
		// not JSON: pass through
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	// KIE wraps some upstream errors as HTTP 200 with {code: <num>, msg: "..."}.
	// Detect and surface as a proper error to the client.
	if code, ok := upstream["code"]; ok {
		if n := numInt(code); n != 0 && n != 200 {
			msg := asString(upstream["msg"])
			if msg == "" {
				msg = fmt.Sprintf("upstream returned code=%d", n)
			}
			status := http.StatusBadGateway
			if n >= 400 && n < 600 {
				status = n
			}
			writeJSONError(w, status, "upstream_error", msg)
			return
		}
	}
	out := translateResponse(client, route.Proto, upstream, modelID)
	writeJSON(w, http.StatusOK, out)
}

func translateResponse(client ClientProto, upstreamProto routing.UpstreamProto, body map[string]any, modelID string) map[string]any {
	switch client {
	case ClientOpenAIChat:
		switch upstreamProto {
		case routing.ProtoOpenAIChat:
			body["model"] = modelID
			return body
		case routing.ProtoAnthropic:
			return translate.AnthropicResponseToOpenAIChat(body, modelID)
		case routing.ProtoOpenAIResponses:
			return translate.ResponsesResponseToOpenAIChat(body, modelID)
		}
	case ClientAnthropic:
		switch upstreamProto {
		case routing.ProtoAnthropic:
			body["model"] = modelID
			return body
		case routing.ProtoOpenAIChat:
			return translate.OpenAIChatResponseToAnthropic(body, modelID)
		case routing.ProtoOpenAIResponses:
			return translate.ResponsesResponseToAnthropic(body, modelID)
		}
	case ClientOpenAIResponses:
		switch upstreamProto {
		case routing.ProtoOpenAIResponses:
			body["model"] = modelID
			return body
		case routing.ProtoOpenAIChat:
			return translate.OpenAIChatResponseToResponses(body, modelID)
		case routing.ProtoAnthropic:
			return translate.AnthropicResponseToResponses(body, modelID)
		}
	}
	return body
}

func (p *Proxy) handleStream(w http.ResponseWriter, resp *http.Response, client ClientProto, route *routing.ModelRoute, modelID string) {
	upstreamFmt := upstreamStreamFmt(route.Proto)
	clientFmt := clientStreamFmt(client)
	if upstreamFmt == clientFmt {
		// pure passthrough
		_ = sse.Passthrough(w, resp.Body)
		return
	}
	writer := sse.NewWriter(w)
	if err := translate.TranslateStream(resp.Body, upstreamFmt, clientFmt, modelID, writer); err != nil && err != io.EOF {
		// best-effort: nothing else we can do mid-stream
		return
	}
}

func upstreamStreamFmt(p routing.UpstreamProto) translate.StreamFormat {
	switch p {
	case routing.ProtoOpenAIChat:
		return translate.FmtOpenAIChat
	case routing.ProtoOpenAIResponses:
		return translate.FmtOpenAIResponses
	case routing.ProtoAnthropic:
		return translate.FmtAnthropic
	}
	return translate.FmtOpenAIChat
}

func clientStreamFmt(c ClientProto) translate.StreamFormat {
	switch c {
	case ClientOpenAIChat:
		return translate.FmtOpenAIChat
	case ClientOpenAIResponses:
		return translate.FmtOpenAIResponses
	case ClientAnthropic:
		return translate.FmtAnthropic
	}
	return translate.FmtOpenAIChat
}

// ----- misc -----

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func numInt(v any) int {
	switch t := v.(type) {
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

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
