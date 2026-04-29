package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Derrity/kie2api-go/config"
	"github.com/Derrity/kie2api-go/routing"
)

type Console struct {
	Store    *config.Store
	Upstream *http.Client
}

type publicConfig struct {
	HasKIEKey     bool     `json:"has_kie_key"`
	KIEKeyPreview string   `json:"kie_key_preview,omitempty"`
	ProxyKey      string   `json:"proxy_key"`
	UpstreamBase  string   `json:"upstream_base"`
	HTTPProxy     string   `json:"http_proxy,omitempty"`
	EnabledModels []string `json:"enabled_models"`
	AllModels     []model  `json:"all_models"`
}

type model struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Proto string `json:"proto"`
}

func (c *Console) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := c.Store.Get()
	all := make([]model, 0, len(routing.All))
	for _, r := range routing.All {
		all = append(all, model{ID: r.ID, Group: groupOf(r.ID), Proto: string(r.Proto)})
	}
	pc := publicConfig{
		HasKIEKey:     cfg.KIEAPIKey != "",
		KIEKeyPreview: maskKey(cfg.KIEAPIKey),
		ProxyKey:      cfg.ProxyKey,
		UpstreamBase:  cfg.UpstreamBase,
		HTTPProxy:     cfg.HTTPProxy,
		EnabledModels: cfg.EnabledModels,
		AllModels:     all,
	}
	writeJSON(w, http.StatusOK, pc)
}

type updateBody struct {
	KIEAPIKey     *string   `json:"kie_api_key"`
	UpstreamBase  *string   `json:"upstream_base"`
	HTTPProxy     *string   `json:"http_proxy"`
	EnabledModels *[]string `json:"enabled_models"`
}

func (c *Console) HandleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var body updateBody
	if err := decodeJSON(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	cfg, err := c.Store.Update(func(c *config.Config) {
		if body.KIEAPIKey != nil && strings.TrimSpace(*body.KIEAPIKey) != "" {
			c.KIEAPIKey = strings.TrimSpace(*body.KIEAPIKey)
		}
		if body.UpstreamBase != nil {
			c.UpstreamBase = strings.TrimSpace(*body.UpstreamBase)
		}
		if body.HTTPProxy != nil {
			c.HTTPProxy = strings.TrimSpace(*body.HTTPProxy)
		}
		if body.EnabledModels != nil {
			// validate each id
			seen := map[string]bool{}
			out := []string{}
			for _, id := range *body.EnabledModels {
				if routing.Find(id) != nil && !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
			c.EnabledModels = out
		}
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	_ = cfg
	c.HandleGetConfig(w, r)
}

func (c *Console) HandleRegenerateKey(w http.ResponseWriter, r *http.Request) {
	k, err := c.Store.RegenerateProxyKey()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxy_key": k})
}

// HandleTest pings KIE.AI with the configured key. We hit an arbitrary
// model's chat endpoint with stream=false and a 1-token cap to verify auth.
func (c *Console) HandleTest(w http.ResponseWriter, r *http.Request) {
	cfg := c.Store.Get()
	if cfg.KIEAPIKey == "" {
		writeJSONError(w, http.StatusBadRequest, "configuration_error", "kie_api_key is empty")
		return
	}
	body := map[string]any{
		"model":    "gpt-5-2",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   false,
	}
	buf, _ := json.Marshal(body)
	url := strings.TrimRight(cfg.UpstreamBase, "/") + "/gpt-5-2/v1/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.KIEAPIKey)
	cli := *c.Upstream
	cli.Timeout = 30 * time.Second
	resp, err := cli.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	writeJSON(w, http.StatusOK, map[string]any{
		"upstream_status": resp.StatusCode,
		"upstream_body":   string(respBody),
	})
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + strings.Repeat("*", len(k)-8) + k[len(k)-4:]
}

func groupOf(id string) string {
	switch {
	case strings.HasPrefix(id, "claude"):
		return "Claude"
	case strings.Contains(id, "codex"):
		return "GPT Codex"
	case strings.HasPrefix(id, "gpt"):
		return "GPT"
	case strings.HasPrefix(id, "gemini"):
		return "Gemini"
	}
	return "Other"
}
