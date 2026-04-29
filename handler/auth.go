package handler

import (
	"net/http"
	"strings"

	"github.com/Derrity/kie2api-go/config"
)

// AuthProxy validates the proxy key from either Authorization: Bearer or x-api-key.
func AuthProxy(store *config.Store, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := store.Get()
		if cfg.ProxyKey == "" {
			writeJSONError(w, http.StatusServiceUnavailable, "configuration_error", "proxy key is not initialised")
			return
		}
		got := extractKey(r)
		if got == "" {
			writeJSONError(w, http.StatusUnauthorized, "authentication_error", "missing API key")
			return
		}
		if got != cfg.ProxyKey {
			writeJSONError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
			return
		}
		if cfg.KIEAPIKey == "" {
			writeJSONError(w, http.StatusServiceUnavailable, "configuration_error", "KIE_API_KEY not configured; open the web console to set it")
			return
		}
		next(w, r)
	}
}

func extractKey(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}
