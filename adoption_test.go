package main

import (
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
	if _, exists := assessment.ProposedSpec.Env["PATH"]; exists {
		t.Errorf("expected default PATH to be filtered out")
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
