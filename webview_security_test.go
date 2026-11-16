//go:build !server

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestWebviewRuntimeBoundary(t *testing.T) {
	t.Setenv("FRONTEND_DEVSERVER_URL", "")
	origin := webviewOrigin()
	for _, tc := range []struct {
		name, method, target, origin, referer, contentType, site, dest string
		status                                                         int
		chunk                                                          bool
	}{
		{name: "native WebKit JSON", method: "POST", referer: origin + "/", contentType: "application/json", status: 204},
		{name: "browser JSON", method: "POST", origin: origin, contentType: "application/json; charset=utf-8", site: "same-origin", dest: "empty", status: 204},
		{name: "native chunk", method: "POST", referer: origin + "/", contentType: "application/json", chunk: true, status: 204},
		{name: "foreign top level", method: "POST", origin: "https://attacker.invalid", contentType: "application/json", status: 403},
		{name: "foreign frame", method: "POST", origin: "http://127.0.0.1:43210", referer: "http://127.0.0.1:43210/", contentType: "application/json", status: 403},
		{name: "opaque frame with trusted referrer", method: "POST", origin: "null", referer: origin + "/", contentType: "application/json", status: 403},
		{name: "no browser origin evidence", method: "POST", contentType: "application/json", status: 403},
		{name: "foreign referrer", method: "POST", referer: "https://attacker.invalid/", contentType: "application/json", status: 403},
		{name: "origin suffix", method: "POST", origin: origin + ".attacker.invalid", contentType: "application/json", status: 403},
		{name: "referrer credentials", method: "POST", referer: "wails://attacker@localhost/", contentType: "application/json", status: 403},
		{name: "wrong asset origin", method: "POST", target: "http://attacker.invalid/wails/runtime", origin: origin, contentType: "application/json", status: 403},
		{name: "cross site metadata", method: "POST", origin: origin, site: "cross-site", contentType: "application/json", status: 403},
		{name: "same site is insufficient", method: "POST", origin: origin, site: "same-site", contentType: "application/json", status: 403},
		{name: "navigation POST", method: "POST", origin: origin, dest: "document", contentType: "application/json", status: 403},
		{name: "simple plain text POST", method: "POST", referer: origin + "/", contentType: "text/plain", status: 415},
		{name: "form POST", method: "POST", referer: origin + "/", contentType: "application/x-www-form-urlencoded", status: 415},
		{name: "missing content type", method: "POST", referer: origin + "/", status: 415},
		{name: "image query dispatch", method: "GET", target: origin + "/wails/runtime?object=0&method=0&args={}", status: 405},
		{name: "HEAD dispatch", method: "HEAD", origin: origin, status: 405},
		{name: "preflight", method: "OPTIONS", origin: origin, status: 405},
		{name: "POST query fallback", method: "POST", target: origin + "/wails/runtime?object=0&method=0", origin: origin, contentType: "application/json", status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if target == "" {
				target = origin + "/wails/runtime"
			}
			const body = `{"object":0,"method":0,"args":{"sentinel":"unchanged"}}`
			r := httptest.NewRequest(tc.method, target, strings.NewReader(body))
			for key, value := range map[string]string{"Origin": tc.origin, "Referer": tc.referer, "Content-Type": tc.contentType, "Sec-Fetch-Site": tc.site, "Sec-Fetch-Dest": tc.dest} {
				if value != "" {
					r.Header.Set(key, value)
				}
			}
			if tc.chunk {
				r.Header.Set("x-wails-chunk-id", "fixture")
				r.Header.Set("x-wails-chunk-index", "0")
				r.Header.Set("x-wails-chunk-total", "2")
			}
			if tc.name == "native WebKit JSON" || tc.chunk {
				// Wails preserves Host but makes the request URL relative.
				r.URL.Scheme, r.URL.Host = "", ""
			}
			calls := 0
			handler := webviewSecurityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				data, err := io.ReadAll(r.Body)
				if err != nil || string(data) != body {
					t.Fatal("authorized runtime body changed", err)
				}
				if tc.chunk && r.Header.Get("x-wails-chunk-id") != "fixture" {
					t.Fatal("chunk transport changed")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || (calls == 1) != (tc.status == 204) {
				t.Fatalf("status=%d dispatches=%d; want status=%d", w.Code, calls, tc.status)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("runtime response exposed cross-origin or cached")
			}
		})
	}
}

func TestWebviewRuntimeRejectsAmbiguousOrigin(t *testing.T) {
	t.Setenv("FRONTEND_DEVSERVER_URL", "")
	for _, header := range []string{"Origin", "Referer"} {
		r := httptest.NewRequest("POST", webviewOrigin()+"/wails/runtime", nil)
		r.Header.Set("Origin", webviewOrigin())
		r.Header.Set("Referer", webviewOrigin()+"/")
		r.Header.Add(header, "https://attacker.invalid")
		if trustedWebviewRequest(r, webviewOrigin()) {
			t.Fatalf("ambiguous %s accepted", header)
		}
	}
}

func TestWebviewAssetsPreventEmbeddedContent(t *testing.T) {
	called := false
	w := httptest.NewRecorder()
	webviewSecurityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte("static positive control"))
	})).ServeHTTP(w, httptest.NewRequest("GET", webviewOrigin()+"/", nil))
	if !called || w.Body.String() != "static positive control" {
		t.Fatal("static asset serving changed")
	}
	index, err := os.ReadFile("frontend/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, directive := range []string{"frame-src 'none'", "child-src 'none'", "form-action 'none'", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), directive) || !strings.Contains(string(index), directive) {
			t.Fatalf("missing browser containment directive: %s", directive)
		}
	}
	// frame-ancestors is ineffective in a meta element and must be a header.
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("Podder document may be framed")
	}
}
