package main

import (
	"strings"
	"testing"
)

func TestParseComposePorts(t *testing.T) {
	yamlContent := `
services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
      - "127.0.0.1:443:443/tcp"
      - "5353:5353/udp"
  redis:
    image: redis:alpine
    ports:
      - "6379:6379"
`

	ports, err := ParseComposePorts(yamlContent, "web")
	if err != nil {
		t.Fatalf("unexpected error parsing compose ports: %v", err)
	}

	if len(ports) != 3 {
		t.Fatalf("expected 3 ports for 'web', got %d", len(ports))
	}
	if ports[0].HostPort != 8080 || ports[0].ContainerPort != 80 {
		t.Errorf("unexpected port 0: %+v", ports[0])
	}
	if ports[1].HostIP != "127.0.0.1" || ports[1].HostPort != 443 {
		t.Errorf("unexpected port 1: %+v", ports[1])
	}
	if ports[2].Protocol != "udp" || ports[2].HostPort != 5353 {
		t.Errorf("unexpected port 2: %+v", ports[2])
	}

	// redis ports
	redisPorts, err := ParseComposePorts(yamlContent, "redis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(redisPorts) != 1 || redisPorts[0].HostPort != 6379 {
		t.Errorf("unexpected redis ports: %+v", redisPorts)
	}
}

func TestUpdateComposePorts(t *testing.T) {
	yamlContent := `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    restart: always
  redis:
    image: redis:alpine
`

	newPorts := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      3000,
			ContainerPort: 80,
			Protocol:      "tcp",
		},
		{
			HostIP:        "127.0.0.1",
			HostPort:      8443,
			ContainerPort: 443,
			Protocol:      "tcp",
		},
	}

	updated, err := UpdateComposePorts(yamlContent, "web", newPorts)
	if err != nil {
		t.Fatalf("failed to update compose ports: %v", err)
	}

	if strings.Contains(updated, "8080:80") {
		t.Errorf("expected old port 8080 to be replaced")
	}
	if !strings.Contains(updated, "127.0.0.1:3000:80/tcp") {
		t.Errorf("expected port 3000 in updated YAML: %s", updated)
	}
	if !strings.Contains(updated, "127.0.0.1:8443:443/tcp") {
		t.Errorf("expected port 8443 in updated YAML: %s", updated)
	}
	if !strings.Contains(updated, "restart: always") {
		t.Errorf("expected restart setting to be preserved")
	}
	if !strings.Contains(updated, "redis:") {
		t.Errorf("expected redis service to be preserved")
	}
}
