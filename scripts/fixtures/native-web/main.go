// A local, dependency-free HTTP image fixture for the opt-in native launcher test.
package main

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"time"
)

func main() {
	started := time.Now()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		status := http.StatusOK
		if r.URL.Path == "/unready" || r.URL.Path == "/slow" && time.Since(started) < 2*time.Second {
			status = http.StatusServiceUnavailable
		}
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, status, r.UserAgent())
		w.WriteHeader(status)
		fmt.Fprintf(w, "<!doctype html><title>Podder native web acceptance</title><h1>Podder native web acceptance</h1><p>Selected path: %s</p>", html.EscapeString(r.URL.Path))
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
