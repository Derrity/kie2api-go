package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const DefaultUpstreamBase = "https://api.kie.ai"

type Config struct {
	KIEAPIKey     string   `json:"kie_api_key"`
	ProxyKey      string   `json:"proxy_key"`
	UpstreamBase  string   `json:"upstream_base"`
	EnabledModels []string `json:"enabled_models"`
	HTTPProxy     string   `json:"http_proxy,omitempty"`
}

type Store struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

// DefaultDir returns ~/.local/share/kie2api (or %APPDATA% on Windows-ish).
func DefaultDir() (string, error) {
	if d := os.Getenv("KIE2API_DATA_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "kie2api"), nil
}

func Load(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "config.json")}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// fresh init
		s.cfg = Config{
			UpstreamBase: DefaultUpstreamBase,
			ProxyKey:     "sk-" + randomHex(24),
		}
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err := json.Unmarshal(data, &s.cfg); err != nil {
		return nil, err
	}
	if s.cfg.UpstreamBase == "" {
		s.cfg.UpstreamBase = DefaultUpstreamBase
	}
	if s.cfg.ProxyKey == "" {
		s.cfg.ProxyKey = "sk-" + randomHex(24)
		_ = s.saveLocked()
	}
	return s, nil
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.cfg
	cp.EnabledModels = append([]string(nil), s.cfg.EnabledModels...)
	return cp
}

// Update mutates the config under lock; the callback receives a pointer to a copy.
func (s *Store) Update(fn func(c *Config)) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	if s.cfg.UpstreamBase == "" {
		s.cfg.UpstreamBase = DefaultUpstreamBase
	}
	if err := s.saveLocked(); err != nil {
		return Config{}, err
	}
	cp := s.cfg
	cp.EnabledModels = append([]string(nil), s.cfg.EnabledModels...)
	return cp, nil
}

func (s *Store) RegenerateProxyKey() (string, error) {
	cfg, err := s.Update(func(c *Config) {
		c.ProxyKey = "sk-" + randomHex(24)
	})
	if err != nil {
		return "", err
	}
	return cfg.ProxyKey, nil
}

func (s *Store) IsModelEnabled(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.cfg.EnabledModels {
		if m == id {
			return true
		}
	}
	return false
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// fall back to a poor but non-empty value; should never happen
		return "unsafe-fallback-key"
	}
	return hex.EncodeToString(b)
}
