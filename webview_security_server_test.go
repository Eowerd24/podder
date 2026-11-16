//go:build server

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestServerRuntimeKeepsUpstreamOriginPolicy(t *testing.T) {
	// Empty JSON stops at Wails' parser, before any service dispatch. Reaching
	// that parser proves the HTTP origin was accepted without invoking a method.
	transport := application.NewHTTPTransport()
	handler := webviewSecurityMiddleware(transport.Handler()(http.NotFoundHandler()))
	for _, tc := range []struct {
		name, method, origin, site string
		status                     int
	}{
		{"same HTTP origin", "POST", "http://localhost:8080", "same-origin", 422},
		{"foreign HTTP origin", "POST", "https://attacker.invalid", "cross-site", 403},
		{"opaque origin", "POST", "null", "", 403},
		{"cross-site without origin", "POST", "", "cross-site", 403},
		{"image GET", "GET", "http://localhost:8080", "", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://localhost:8080/wails/runtime", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}
