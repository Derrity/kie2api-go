package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/Derrity/kie2api-go/config"
	"github.com/Derrity/kie2api-go/handler"
	"github.com/Derrity/kie2api-go/upstream"
	"github.com/Derrity/kie2api-go/web"
)

func main() {
	var (
		webPort   = flag.Int("web-port", 3001, "Web console port")
		proxyPort = flag.Int("proxy-port", 4142, "Proxy API port")
		dataDir   = flag.String("data-dir", "", "Data directory (default: ~/.local/share/kie2api)")
		verbose   = flag.Bool("verbose", false, "Verbose logging")
	)
	flag.Parse()

	dir := *dataDir
	if dir == "" {
		d, err := config.DefaultDir()
		if err != nil {
			log.Fatalf("resolve data dir: %v", err)
		}
		dir = d
	}
	store, err := config.Load(dir)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	cli := upstream.New(store.Get().HTTPProxy)

	console := &handler.Console{Store: store, Upstream: cli}
	proxy := &handler.Proxy{Store: store, Upstream: cli}

	// --- Web Console (port 3001) ---
	webMux := http.NewServeMux()
	webMux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			console.HandleGetConfig(w, r)
		case http.MethodPut, http.MethodPost:
			console.HandleUpdateConfig(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	webMux.HandleFunc("/api/proxy-key/regenerate", console.HandleRegenerateKey)
	webMux.HandleFunc("/api/test", console.HandleTest)
	webMux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"proxy_port":%d}`, *proxyPort)
	})
	indexFS, _ := fs.Sub(web.FS, ".")
	webMux.Handle("/", http.FileServer(http.FS(indexFS)))

	// --- Proxy API (port 4142) ---
	proxyMux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc { return handler.AuthProxy(store, h) }

	registerOpenAI := func(path string, h http.HandlerFunc) {
		proxyMux.HandleFunc(path, h)
		// also support without /v1 prefix for OpenAI-style aliases
	}
	registerOpenAI("/v1/chat/completions", auth(proxy.HandleOpenAIChat))
	registerOpenAI("/chat/completions", auth(proxy.HandleOpenAIChat))
	registerOpenAI("/v1/responses", auth(proxy.HandleOpenAIResponses))
	registerOpenAI("/responses", auth(proxy.HandleOpenAIResponses))
	registerOpenAI("/v1/messages", auth(proxy.HandleAnthropicMessages))
	registerOpenAI("/v1/messages/count_tokens", auth(proxy.HandleCountTokens))
	registerOpenAI("/v1/models", auth(proxy.HandleListModels))
	registerOpenAI("/models", auth(proxy.HandleListModels))
	proxyMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if *verbose {
		log.Printf("data dir: %s", dir)
		log.Printf("loaded config: enabled=%d models, kie_key_set=%t", len(store.Get().EnabledModels), store.Get().KIEAPIKey != "")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		addr := fmt.Sprintf(":%d", *webPort)
		log.Printf("[web]   http://localhost%s", addr)
		if err := http.ListenAndServe(addr, webMux); err != nil {
			log.Printf("web server: %v", err)
			os.Exit(1)
		}
	}()
	go func() {
		defer wg.Done()
		addr := fmt.Sprintf(":%d", *proxyPort)
		log.Printf("[proxy] http://localhost%s", addr)
		if err := http.ListenAndServe(addr, proxyMux); err != nil {
			log.Printf("proxy server: %v", err)
			os.Exit(1)
		}
	}()
	wg.Wait()
}
