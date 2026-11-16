package main

import (
	"mime"
	"net/http"
	"net/url"
	"runtime"
	"strings"
)

const webviewContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'; child-src 'none'; frame-ancestors 'none'"

func webviewOrigin() string {
	origin := "wails://localhost"
	if runtime.GOOS == "windows" {
		origin = "http://wails.localhost"
	}
	if port := webviewDevelopmentPort(); port != "" {
		origin += ":" + port
	}
	return origin
}

// Wails' desktop transport does not authorize callers of /wails/runtime.
// Window IDs are added to every native resource request, including requests
// from foreign documents, so they cannot authenticate the calling document.
// Bind the transport to Podder's document origin before Wails can dispatch it.
func webviewSecurityMiddleware(next http.Handler) http.Handler {
	trustedOrigin := webviewOrigin()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", webviewContentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path != "/wails/runtime" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if nativeWebviewTransport && !trustedWebviewRequest(r, trustedOrigin) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}
		// The current runtime sends JSON POST bodies, including chunked bodies.
		// Do not expose Wails' legacy query-string dispatch fallback to images,
		// forms, or navigation requests.
		if r.URL.RawQuery != "" {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func trustedWebviewRequest(r *http.Request, trustedOrigin string) bool {
	expected, err := url.Parse(trustedOrigin)
	// Wails strips the scheme and host from URL before invoking middleware,
	// while preserving the original request Host. Check any absolute URL too
	// so this boundary also works when used with an ordinary HTTP handler.
	if err != nil || r.Host != expected.Host ||
		(r.URL.Scheme != "" && r.URL.Scheme != expected.Scheme) ||
		(r.URL.Host != "" && r.URL.Host != expected.Host) {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" && dest != "empty" {
		return false
	}
	if len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("Referer")) > 1 {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		// An opaque/null origin must never inherit authority from its referrer.
		return origin == trustedOrigin
	}
	// WebKitGTK's custom scheme omits Origin and Fetch Metadata on legitimate
	// same-origin fetches. Its browser-generated Referer carries the origin.
	referrer, err := url.Parse(r.Referer())
	return err == nil && referrer.User == nil && referrer.Opaque == "" &&
		strings.EqualFold(referrer.Scheme+"://"+referrer.Host, trustedOrigin)
}
