package main

import (
	"strings"
	"testing"
)

func TestGenerateComposeSnippet(t *testing.T) {
	ports := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      8080,
			ContainerPort: 80,
			Protocol:      "tcp",
		},
		{
			HostIP:        "0.0.0.0",
			HostPort:      5353,
			ContainerPort: 5353,
			Protocol:      "udp",
		},
	}

	snippet := GenerateComposeSnippet("web-service", ports)
	if !strings.Contains(snippet, "web-service:") {
		t.Errorf("expected service name in snippet, got: %s", snippet)
	}
	if !strings.Contains(snippet, `"127.0.0.1:8080:80/tcp"`) {
		t.Errorf("expected loopback port in snippet, got: %s", snippet)
	}
	if !strings.Contains(snippet, `"5353:5353/udp"`) {
		t.Errorf("expected wildcard port in snippet, got: %s", snippet)
	}
}

func TestGenerateQuadletSnippet(t *testing.T) {
	ports := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      3000,
			ContainerPort: 3000,
			Protocol:      "tcp",
		},
	}

	snippet := GenerateQuadletSnippet(ports)
	if !strings.Contains(snippet, "[Container]") {
		t.Errorf("expected [Container] section, got: %s", snippet)
	}
	if !strings.Contains(snippet, "PublishPort=127.0.0.1:3000:3000/tcp") {
		t.Errorf("expected PublishPort line, got: %s", snippet)
	}
}
