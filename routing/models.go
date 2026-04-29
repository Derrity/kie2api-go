package routing

import "strings"

// UpstreamProto identifies which KIE upstream protocol shape a model speaks.
type UpstreamProto string

const (
	ProtoOpenAIChat      UpstreamProto = "openai_chat"
	ProtoOpenAIResponses UpstreamProto = "openai_responses"
	ProtoAnthropic       UpstreamProto = "anthropic"
)

// ModelRoute describes how to reach a model on KIE.AI.
//
//	ID            : the canonical model id we expose to clients.
//	UpstreamPath  : path under cfg.UpstreamBase to POST to.
//	UpstreamModel : value to put in the upstream `model` field
//	                (empty = the path already implies model, no `model` field needed
//	                for some chat-completions style endpoints; we still send it for safety).
//	Proto         : how the upstream expects requests / emits responses.
type ModelRoute struct {
	ID            string
	UpstreamPath  string
	UpstreamModel string
	Proto         UpstreamProto
}

// All is the master list of supported KIE.AI chat models.
// Adding a row here makes a model selectable in the Web Console.
var All = []ModelRoute{
	// --- GPT (OpenAI Chat Completions style) ---
	{ID: "gpt-5-2", UpstreamPath: "/gpt-5-2/v1/chat/completions", UpstreamModel: "gpt-5-2", Proto: ProtoOpenAIChat},

	// --- GPT (OpenAI Responses style) ---
	// All Responses-style GPT/Codex models share the unified /codex/v1/responses endpoint.
	{ID: "gpt-5-4", UpstreamPath: "/codex/v1/responses", UpstreamModel: "gpt-5-4", Proto: ProtoOpenAIResponses},
	{ID: "gpt-5-5", UpstreamPath: "/codex/v1/responses", UpstreamModel: "gpt-5-5", Proto: ProtoOpenAIResponses},

	// --- GPT Codex family (Responses) ---
	{ID: "gpt-5-codex", UpstreamPath: "/codex/v1/responses", UpstreamModel: "gpt-5-codex", Proto: ProtoOpenAIResponses},
	{ID: "gpt-5.1-codex", UpstreamPath: "/codex/v1/responses", UpstreamModel: "gpt-5.1-codex", Proto: ProtoOpenAIResponses},
	{ID: "gpt-5.2-codex", UpstreamPath: "/codex/v1/responses", UpstreamModel: "gpt-5.2-codex", Proto: ProtoOpenAIResponses},
	{ID: "gpt-5.3-codex", UpstreamPath: "/codex/v1/responses", UpstreamModel: "gpt-5.3-codex", Proto: ProtoOpenAIResponses},
	{ID: "gpt-5.4-codex", UpstreamPath: "/codex/v1/responses", UpstreamModel: "gpt-5.4-codex", Proto: ProtoOpenAIResponses},

	// --- Claude (Anthropic Messages) ---
	{ID: "claude-haiku-4-5", UpstreamPath: "/claude/v1/messages", UpstreamModel: "claude-haiku-4-5", Proto: ProtoAnthropic},
	{ID: "claude-opus-4-5", UpstreamPath: "/claude/v1/messages", UpstreamModel: "claude-opus-4-5", Proto: ProtoAnthropic},
	{ID: "claude-opus-4-6", UpstreamPath: "/claude/v1/messages", UpstreamModel: "claude-opus-4-6", Proto: ProtoAnthropic},
	{ID: "claude-sonnet-4-5", UpstreamPath: "/claude/v1/messages", UpstreamModel: "claude-sonnet-4-5", Proto: ProtoAnthropic},
	{ID: "claude-sonnet-4-6", UpstreamPath: "/claude/v1/messages", UpstreamModel: "claude-sonnet-4-6", Proto: ProtoAnthropic},

	// --- Gemini (OpenAI Chat shape) ---
	{ID: "gemini-2-5-pro", UpstreamPath: "/gemini-2-5-pro/v1/chat/completions", UpstreamModel: "gemini-2-5-pro", Proto: ProtoOpenAIChat},
	{ID: "gemini-3-pro", UpstreamPath: "/gemini-3-pro/v1/chat/completions", UpstreamModel: "gemini-3-pro", Proto: ProtoOpenAIChat},
	{ID: "gemini-3-1-pro", UpstreamPath: "/gemini-3-1-pro/v1/chat/completions", UpstreamModel: "gemini-3-1-pro", Proto: ProtoOpenAIChat},
	{ID: "gemini-2-5-flash", UpstreamPath: "/gemini-2-5-flash/v1/chat/completions", UpstreamModel: "gemini-2-5-flash", Proto: ProtoOpenAIChat},
	{ID: "gemini-3-flash", UpstreamPath: "/gemini-3-flash/v1/chat/completions", UpstreamModel: "gemini-3-flash", Proto: ProtoOpenAIChat},
}

// Find returns the route for an id (or any of its known aliases), or nil.
func Find(id string) *ModelRoute {
	id = canonical(id)
	for i := range All {
		if All[i].ID == id {
			return &All[i]
		}
	}
	return nil
}

// canonical maps common alias spellings to our canonical model id.
// KIE uses dashes everywhere (e.g. gpt-5-5, claude-sonnet-4-6), but many
// clients use the marketing-style dots (gpt-5.5, claude-sonnet-4.6).
// We accept either form.
func canonical(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return id
	}
	if alias, ok := aliasMap[id]; ok {
		return alias
	}
	// Auto-derive: replace dots with dashes within the version segments.
	// e.g. "gpt-5.4" -> "gpt-5-4", "claude-opus-4.6" -> "claude-opus-4-6",
	//      "gemini-3.1-pro" -> "gemini-3-1-pro", "gpt-5.3-codex" -> "gpt-5.3-codex" -> "gpt-5-3-codex"
	dashed := strings.ReplaceAll(id, ".", "-")
	for i := range All {
		if All[i].ID == dashed {
			return dashed
		}
	}
	return id
}

// aliasMap holds explicit aliases that don't follow the dot-to-dash rule.
// Populate as new client-side spellings are reported.
var aliasMap = map[string]string{}

// IDs returns all model ids in declaration order.
func IDs() []string {
	out := make([]string, 0, len(All))
	for _, r := range All {
		out = append(out, r.ID)
	}
	return out
}
