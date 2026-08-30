package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseInspectToAssessment_CleanAdHoc(t *testing.T) {
	rawJSON := `[
  {
    "Id": "abc1234567890abcdef",
    "Name": "/my-ad-hoc-web",
    "Image": "docker.io/library/nginx:alpine",
    "Config": {
      "Image": "docker.io/library/nginx:alpine",
      "Cmd": ["nginx", "-g", "daemon off;"],
      "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "CUSTOM_VAR=hello123"],
      "Labels": {}
    },
    "HostConfig": {
      "PortBindings": {
        "80/tcp": [
          {
            "HostIp": "127.0.0.1",
            "HostPort": "8080"
          }
        ]
      },
      "Privileged": false,
      "NetworkMode": "bridge"
    },
    "Mounts": [
      {
        "Type": "bind",
        "Source": "/host/data",
        "Destination": "/usr/share/nginx/html",
        "RW": true
      }
    ],
    "Pod": ""
  }
]`

	assessment, err := ParseInspectToAssessment([]byte(rawJSON))
	if err != nil {
		t.Fatalf("unexpected error parsing inspect JSON: %v", err)
	}

	if !assessment.CanAdopt {
		t.Errorf("expected clean ad-hoc container to be adoptable")
	}
	if assessment.ContainerName != "my-ad-hoc-web" {
		t.Errorf("expected name 'my-ad-hoc-web', got '%s'", assessment.ContainerName)
	}
	if len(assessment.ProposedSpec.PortMappings) != 1 {
		t.Fatalf("expected 1 port mapping, got %d", len(assessment.ProposedSpec.PortMappings))
	}
	pm := assessment.ProposedSpec.PortMappings[0]
	if pm.HostPort != 8080 || pm.ContainerPort != 80 || pm.HostIP != "127.0.0.1" {
		t.Errorf("unexpected port mapping: %+v", pm)
	}
	if len(assessment.ProposedSpec.Binds) != 1 {
		t.Fatalf("expected 1 bind mount, got %d", len(assessment.ProposedSpec.Binds))
	}
	if assessment.ProposedSpec.Binds[0].HostPath != "/host/data" {
		t.Errorf("unexpected bind host path: %s", assessment.ProposedSpec.Binds[0].HostPath)
	}
	if assessment.ProposedSpec.Env["CUSTOM_VAR"] != "hello123" {
		t.Errorf("expected CUSTOM_VAR in env, got: %+v", assessment.ProposedSpec.Env)
	}
	if assessment.ProposedSpec.Env["PATH"] == "" {
		t.Errorf("expected PATH to be preserved exactly; adoption must not guess which environment values are disposable")
	}
}

func TestParseInspectToAssessment_ComposeBlocked(t *testing.T) {
	rawJSON := `[
  {
    "Id": "compose123",
    "Name": "/compose-stack_web_1",
    "Config": {
      "Image": "nginx:alpine",
      "Labels": {
        "com.docker.compose.project": "my-stack",
        "com.docker.compose.service": "web"
      }
    },
    "HostConfig": {},
    "Mounts": [],
    "Pod": ""
  }
]`

	assessment, err := ParseInspectToAssessment([]byte(rawJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if assessment.CanAdopt {
		t.Errorf("expected compose container adoption to be blocked")
	}
	if len(assessment.Blockers) == 0 {
		t.Errorf("expected blockers list to contain compose warning")
	}
}

func TestParseInspectToAssessment_PodBlocked(t *testing.T) {
	rawJSON := `[
  {
    "Id": "podmember123",
    "Name": "/pod-service",
    "Config": {
      "Image": "redis:alpine",
      "Labels": {}
    },
    "HostConfig": {},
    "Mounts": [],
    "Pod": "pod-id-789"
  }
]`

	assessment, err := ParseInspectToAssessment([]byte(rawJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if assessment.CanAdopt {
		t.Errorf("expected pod member adoption to be blocked")
	}
}

// baseInspectJSON returns a minimal, otherwise-clean inspect document with a
// single field patched in via the supplied hostConfigExtra/configExtra/topExtra
// JSON fragments, to build focused representability-blocker fixtures.
func baseInspectJSON(topExtra, configExtra, hostConfigExtra, mountsExtra, networksExtra string) string {
	return `[
  {
    "Id": "abc1234567890abcdef",
    "Name": "/fixture",
    "Image": "docker.io/library/nginx:alpine",
    ` + topExtra + `
    "Config": {
      "Image": "docker.io/library/nginx:alpine",
      "Cmd": ["nginx"],
      "Env": [],
      "Labels": {}` + configExtra + `
    },
    "HostConfig": {
      "PortBindings": {},
      "Privileged": false,
      "NetworkMode": "bridge"` + hostConfigExtra + `
    },
    "Mounts": [` + mountsExtra + `],
    "NetworkSettings": { "Networks": {` + networksExtra + `} },
    "Pod": ""
  }
]`
}

func TestAdoptionBlockers(t *testing.T) {
	cases := []struct {
		name            string
		topExtra        string
		configExtra     string
		hostConfigExtra string
		mountsExtra     string
		networksExtra   string
		wantBlockPhrase string
	}{
		{
			name:            "named-volume",
			mountsExtra:     `{"Type":"volume","Name":"mydata","Source":"/var/lib/containers/storage/volumes/mydata/_data","Destination":"/data","RW":true}`,
			wantBlockPhrase: "named volume",
		},
		{
			name:            "anonymous-volume",
			mountsExtra:     `{"Type":"volume","Name":"","Source":"/var/lib/containers/storage/volumes/anon/_data","Destination":"/data","RW":true}`,
			wantBlockPhrase: "anonymous volume",
		},
		{
			name:            "privileged",
			hostConfigExtra: `,"Privileged":true`,
			wantBlockPhrase: "privileged",
		},
		{
			name:            "host-networking",
			hostConfigExtra: `,"NetworkMode":"host"`,
			wantBlockPhrase: "host networking",
		},
		{
			name:            "custom-restart-policy",
			hostConfigExtra: `,"RestartPolicy":{"Name":"always"}`,
			wantBlockPhrase: "restart policy",
		},
		{
			name:            "additional-network",
			networksExtra:   `"custom-net": {"Aliases": []}`,
			wantBlockPhrase: "network",
		},
		{
			name:            "network-alias",
			networksExtra:   `"podman": {"Aliases": ["myalias"]}`,
			wantBlockPhrase: "aliases",
		},
		{
			name:            "static-ip",
			networksExtra:   `"podman": {"IPAMConfig": {"IPv4Address": "10.0.0.5"}}`,
			wantBlockPhrase: "static container IP",
		},
		{
			name:            "device-mapping",
			hostConfigExtra: `,"Devices":[{"PathOnHost":"/dev/nvidia0"}]`,
			wantBlockPhrase: "device mappings",
		},
		{
			name:            "capabilities",
			hostConfigExtra: `,"CapAdd":["SYS_ADMIN"]`,
			wantBlockPhrase: "capabilities",
		},
		{
			name:            "security-option",
			hostConfigExtra: `,"SecurityOpt":["label=type:spc_t"]`,
			wantBlockPhrase: "security option",
		},
		{
			name:            "user",
			configExtra:     `,"User":"1000:1000"`,
			wantBlockPhrase: "custom user",
		},
		{
			name:            "workdir",
			configExtra:     `,"WorkingDir":"/srv/app"`,
			wantBlockPhrase: "working directory",
		},
		{
			name:            "healthcheck",
			configExtra:     `,"Healthcheck":{"Test":["CMD","curl","-f","http://localhost"]}`,
			wantBlockPhrase: "healthcheck",
		},
		{
			name:            "memory-limit",
			hostConfigExtra: `,"Memory":536870912`,
			wantBlockPhrase: "memory limit",
		},
		{
			name:            "cpu-limit",
			hostConfigExtra: `,"NanoCpus":2000000000`,
			wantBlockPhrase: "CPU limits",
		},
		{
			name:            "pid-namespace",
			hostConfigExtra: `,"PidMode":"host"`,
			wantBlockPhrase: "PID namespace",
		},
		{
			name:            "dns",
			hostConfigExtra: `,"Dns":["1.1.1.1"]`,
			wantBlockPhrase: "DNS",
		},
		{
			name:            "extra-hosts",
			hostConfigExtra: `,"ExtraHosts":["foo:127.0.0.1"]`,
			wantBlockPhrase: "extra /etc/hosts",
		},
		{
			name:            "tmpfs",
			mountsExtra:     `{"Type":"tmpfs","Destination":"/tmp/scratch"}`,
			wantBlockPhrase: "tmpfs",
		},
		{
			name:            "ulimits",
			hostConfigExtra: `,"Ulimits":[{"Name":"nofile"}]`,
			wantBlockPhrase: "ulimits",
		},
		{
			name:            "init",
			hostConfigExtra: `,"Init":true`,
			wantBlockPhrase: "--init",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := baseInspectJSON(tc.topExtra, tc.configExtra, tc.hostConfigExtra, tc.mountsExtra, tc.networksExtra)
			assessment, err := ParseInspectToAssessment([]byte(raw))
			if err != nil {
				t.Fatalf("unexpected parse error: %v\njson: %s", err, raw)
			}
			if assessment.CanAdopt {
				t.Fatalf("expected adoption to be blocked for %s, blockers: %v", tc.name, assessment.Blockers)
			}
			found := false
			for _, b := range assessment.Blockers {
				if strings.Contains(b, tc.wantBlockPhrase) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a blocker mentioning %q, got: %v", tc.wantBlockPhrase, assessment.Blockers)
			}
		})
	}
}

func TestAdoptionSupportsCustomEntrypoint(t *testing.T) {
	raw := baseInspectJSON("", `,"Entrypoint":["/custom-entrypoint.sh"]`, "", "", "")
	assessment, err := ParseInspectToAssessment([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !assessment.CanAdopt {
		t.Fatalf("expected a custom entrypoint to be representable (Podder supports Entrypoint), blockers: %v", assessment.Blockers)
	}
	if len(assessment.ProposedSpec.Entrypoint) != 1 || assessment.ProposedSpec.Entrypoint[0] != "/custom-entrypoint.sh" {
		t.Errorf("expected entrypoint captured verbatim, got: %v", assessment.ProposedSpec.Entrypoint)
	}
}

func TestAdoptContainer_SuccessfulTransaction(t *testing.T) {
	withTestHome(t)
	withFastPolling(t)

	sim := newMutationSim()
	sim.addContainer("legacy-web", "nginx:alpine", "running", []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}, false, "")
	svc := &PodmanService{runner: &adoptionInspectRouter{sim: sim, image: "nginx:alpine", ports: []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}}}

	result, err := svc.AdoptContainer("legacy-web", "legacy-web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful adoption, message: %s", result.Message)
	}

	spec, err := svc.GetSpec("legacy-web")
	if err != nil {
		t.Fatalf("expected committed spec after successful adoption: %v", err)
	}
	if !spec.Managed {
		t.Errorf("expected committed spec to be Managed")
	}

	c := sim.containers["legacy-web"]
	if c == nil || !c.managed {
		t.Fatalf("expected replacement container to exist and carry managed labels, got: %+v", c)
	}
}

func TestAdoptContainer_FailedTransactionLeavesNoTrace(t *testing.T) {
	withTestHome(t)
	withFastPolling(t)

	sim := newMutationSim()
	sim.addContainer("legacy-db", "postgres:16", "running", []PortMapping{{HostIP: "127.0.0.1", HostPort: 5432, ContainerPort: 5432, Protocol: "tcp"}}, false, "")
	router := &adoptionInspectRouter{sim: sim, image: "postgres:16", ports: []PortMapping{{HostIP: "127.0.0.1", HostPort: 5432, ContainerPort: 5432, Protocol: "tcp"}}, failCreate: true}
	svc := &PodmanService{runner: router}

	result, err := svc.AdoptContainer("legacy-db", "")
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false when the recreation step fails")
	}
	if _, err := svc.GetSpec("legacy-db"); err == nil {
		t.Errorf("expected no committed spec after a failed adoption transaction")
	}

	c := sim.containers["legacy-db"]
	if c == nil {
		t.Fatalf("expected the original container to still exist under its original name")
	}
	if c.state != "running" {
		t.Errorf("expected original container's running state preserved, got %s", c.state)
	}
}

// adoptionInspectRouter wraps mutationSim and answers `podman inspect
// --format json <id>` with a minimal clean-adoptable fixture for whatever
// container is being inspected, since AdoptContainer's preflight uses
// `inspect` rather than `ps` to build its assessment.
type adoptionInspectRouter struct {
	sim        *mutationSim
	image      string
	ports      []PortMapping
	failCreate bool
}

func (r *adoptionInspectRouter) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "inspect" {
		id := args[len(args)-1]
		r.sim.mu.Lock()
		c, ok := r.sim.containers[id]
		r.sim.mu.Unlock()
		if !ok {
			return "[]", "", nil
		}
		portsJSON := `{}`
		if len(r.ports) > 0 {
			p := r.ports[0]
			portsJSON = fmt.Sprintf(`{"%d/%s": [{"HostIp":"%s","HostPort":"%d"}]}`, p.ContainerPort, p.Protocol, p.HostIP, p.HostPort)
		}
		return fmt.Sprintf(`[{"Id":"%s","Name":"/%s","Image":"%s","Config":{"Image":"%s","Cmd":[],"Env":[],"Labels":{}},"HostConfig":{"PortBindings":%s,"Privileged":false,"NetworkMode":"bridge"},"Mounts":[],"Pod":""}]`,
			c.id, id, c.imageID, r.image, portsJSON), "", nil
	}
	if r.failCreate && name == "podman" && len(args) > 0 && (args[0] == "run" || args[0] == "create") {
		return "", "simulated create failure", fmt.Errorf("simulated create failure")
	}
	return r.sim.Run(name, args...)
}
