package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPortEditorRevisionBindsTarget(t *testing.T) {
	a := Container{Id: "a", Names: []string{"A"}, ImageID: "image", State: "running", Labels: map[string]string{"io.podder.service": "A"}}
	rev := portEditorRevision(&a)
	a.Status = "Up 20 seconds"
	if portEditorRevision(&a) != rev {
		t.Fatal("display age changed revision")
	}
	a.Id = "b"
	if portEditorRevision(&a) == rev {
		t.Fatal("target not bound")
	}
	a.Id = "a"
	a.Labels["io.podder.service"] = "B"
	if portEditorRevision(&a) == rev {
		t.Fatal("ownership not bound")
	}
}

func TestWrongTargetPayloadIsRejected(t *testing.T) {
	withTestHome(t)
	// Model a stale editor for service A that dispatches against service B's
	// container. Keep the regression independent of local browser captures.
	req := PortMutationRequest{
		ContainerID: strings.Repeat("b", 64),
		ServiceName: "assurance-race-A",
		NewPorts: []PortMapping{{
			HostIP: "127.0.0.1", HostPort: 35599,
			ContainerPort: 8080, Protocol: "tcp",
		}},
	}
	c := rawContainer{Id: req.ContainerID, Names: []string{"assurance-race-B"}, ImageID: "fixture", State: "running", Labels: map[string]string{"io.podder.managed": "true", "io.podder.service": "assurance-race-B", "io.podder.schema-version": "1"}}
	listing, _ := json.Marshal([]rawContainer{c})
	r := newFakeCommandRunner()
	r.On("podman ps", func(string, []string) (string, string, error) { return string(listing), "", nil })
	p := &PodmanService{runner: r}
	if _, err := p.MutateContainerPorts(req); err == nil || !strings.Contains(err.Error(), "target/service expectation") {
		t.Fatalf("wrong target accepted: %v", err)
	}
	for _, revision := range []string{"", "stale"} {
		req.ServiceName = "assurance-race-B"
		req.Revision = revision
		if _, err := p.MutateContainerPorts(req); err == nil || !strings.Contains(err.Error(), "revision") {
			t.Fatalf("revision accepted: %v", err)
		}
	}
	req.ContainerID = req.ContainerID[:12]
	req.ServiceName = ""
	req.Revision = ""
	if _, err := p.MutateContainerPorts(req); err == nil {
		t.Fatal("prefix accepted")
	}
}
