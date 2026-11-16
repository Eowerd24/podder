//go:build !production

package main

import (
	"net/url"
	"os"
)

func webviewDevelopmentPort() string {
	// Match Wails' development start URL: only the dev server's port is
	// copied onto the native application origin, never its hostname.
	parsed, err := url.Parse(os.Getenv("FRONTEND_DEVSERVER_URL"))
	if err != nil {
		return ""
	}
	return parsed.Port()
}
