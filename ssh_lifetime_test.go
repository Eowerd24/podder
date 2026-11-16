package main

import (
	"strings"
	"testing"
)

func TestSSHTunnelDoesNotForkAfterAuthentication(t *testing.T) {
	args := strings.Join(sshTunnelArgs(RemoteProfile{Host: "127.0.0.1", User: "fixture", Port: 2222}, 8080, "127.0.0.1", 80), " ")
	for _, required := range []string{"ForkAfterAuthentication=no", "StrictHostKeyChecking=yes", "BatchMode=yes", "-F", "127.0.0.1:8080:127.0.0.1:80"} {
		if !strings.Contains(args, required) {
			t.Fatal(required)
		}
	}
}
