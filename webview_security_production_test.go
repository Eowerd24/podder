//go:build production && !server

package main

import "testing"

func TestWebviewProductionIgnoresDevelopmentOrigin(t *testing.T) {
	t.Setenv("FRONTEND_DEVSERVER_URL", "")
	expected := webviewOrigin()
	t.Setenv("FRONTEND_DEVSERVER_URL", "http://attacker.invalid:9245")
	if webviewOrigin() != expected || webviewDevelopmentPort() != "" {
		t.Fatal("production trust changed through a development environment variable")
	}
}
