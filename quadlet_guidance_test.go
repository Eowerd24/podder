package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuadletGuidanceRejectsShellMetacharacters(t *testing.T) {
	withTestHome(t)
	for _, unit := range []string{"a.service; touch /tmp/not-executed", "a.service $(id)", "a.service\nfalse", "--help", "valid@instance.service"} {
		r := newFakeCommandRunner()
		raw, _ := json.Marshal([]rawContainer{{Id: strings.Repeat("a", 64), Names: []string{"fixture"}, Labels: map[string]string{"PODMAN_SYSTEMD_UNIT": unit}}})
		r.On("podman ps", func(string, []string) (string, string, error) { return string(raw), "", nil })
		p := &PodmanService{runner: r}
		x, e := p.MutateContainerPorts(PortMutationRequest{ContainerID: strings.Repeat("a", 64)})
		if e != nil {
			t.Fatal(e)
		}
		if unit == "valid@instance.service" {
			if !strings.Contains(x.Guidance, "restart -- 'valid@instance.service'") {
				t.Fatal(x.Guidance)
			}
		} else {
			if strings.Contains(x.Guidance, "systemctl") || strings.Contains(x.Guidance, unit) {
				t.Fatal("untrusted command generated", x.Guidance)
			}
		}
	}
}
