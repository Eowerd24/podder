//go:build !production && !server

package main

import (
	"net/http/httptest"
	"testing"
)

func TestWebviewDevelopmentOrigin(t *testing.T) {
	t.Setenv("FRONTEND_DEVSERVER_URL", "")
	base := webviewOrigin()
	t.Setenv("FRONTEND_DEVSERVER_URL", "http://127.0.0.1:9245")
	if webviewOrigin() != base+":9245" {
		t.Fatal("development origin differs from Wails start URL")
	}
	r := httptest.NewRequest("POST", webviewOrigin()+"/wails/runtime", nil)
	r.Header.Set("Referer", webviewOrigin()+"/")
	if !trustedWebviewRequest(r, webviewOrigin()) {
		t.Fatal("native development fetch rejected")
	}
	r.Header.Set("Origin", "http://127.0.0.1:9245")
	if trustedWebviewRequest(r, webviewOrigin()) {
		t.Fatal("proxy upstream inherited native origin authority")
	}
}
