package main

import (
	"strings"
	"testing"
)

func TestParseQuadletContent(t *testing.T) {
	content := `
[Unit]
Description=Caddy Web Server
After=network-online.target

[Container]
Image=docker.io/library/caddy:alpine
PublishPort=80:80
PublishPort=127.0.0.1:443:443/tcp
PublishPort=5353:5353/udp

[Service]
Restart=always
`

	ports := ParseQuadletContent(content)
	if len(ports) != 3 {
		t.Fatalf("expected 3 port mappings, got %d", len(ports))
	}

	p1 := ports[0]
	if p1.HostPort != 80 || p1.ContainerPort != 80 || p1.HostIP != "0.0.0.0" || p1.Protocol != "tcp" {
		t.Errorf("unexpected port 1: %+v", p1)
	}

	p2 := ports[1]
	if p2.HostPort != 443 || p2.ContainerPort != 443 || p2.HostIP != "127.0.0.1" || p2.Protocol != "tcp" {
		t.Errorf("unexpected port 2: %+v", p2)
	}

	p3 := ports[2]
	if p3.HostPort != 5353 || p3.ContainerPort != 5353 || p3.Protocol != "udp" {
		t.Errorf("unexpected port 3: %+v", p3)
	}
}

func TestUpdateQuadletContent(t *testing.T) {
	orig := `
[Unit]
Description=Vaultwarden Password Manager

[Container]
Image=docker.io/vaultwarden/server:latest
PublishPort=8080:80/tcp
Environment=WEBSOCKET_ENABLED=true

[Service]
Restart=always
`

	newPorts := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      9090,
			ContainerPort: 80,
			Protocol:      "tcp",
		},
		{
			HostIP:        "127.0.0.1",
			HostPort:      3012,
			ContainerPort: 3012,
			Protocol:      "tcp",
		},
	}

	updated := UpdateQuadletContent(orig, newPorts)

	if strings.Contains(updated, "8080:80") {
		t.Errorf("expected old port 8080 to be removed")
	}
	if !strings.Contains(updated, "PublishPort=127.0.0.1:9090:80/tcp") {
		t.Errorf("expected new port 9090 to be present in updated content: %s", updated)
	}
	if !strings.Contains(updated, "PublishPort=127.0.0.1:3012:3012/tcp") {
		t.Errorf("expected new port 3012 to be present in updated content: %s", updated)
	}
	if !strings.Contains(updated, "Environment=WEBSOCKET_ENABLED=true") {
		t.Errorf("expected other container options to be preserved")
	}
	if !strings.Contains(updated, "[Service]") {
		t.Errorf("expected other sections to be preserved")
	}
}
