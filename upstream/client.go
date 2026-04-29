package upstream

import (
	"net/http"
	"net/url"
	"time"
)

// New returns an HTTP client configured for KIE.AI upstream.
// httpProxy may be empty.
func New(httpProxy string) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		DisableCompression:    false,
	}
	if httpProxy != "" {
		if u, err := url.Parse(httpProxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{
		Transport: tr,
		// No global timeout: streaming responses must stay open. The
		// transport's ResponseHeaderTimeout caps time-to-first-byte.
	}
}
