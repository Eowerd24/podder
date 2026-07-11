package main

import (
	"encoding/json"
	"testing"
)

func TestContainerJSONParsing(t *testing.T) {
	jsonInput := `[
		{
			"Id": "f0d3a6ae9da9ce991ec825e4c81efcd3a3f6f5a02abbdffc0cc3914a4bbe7899",
			"Names": ["test-alpine"],
			"Image": "docker.io/library/alpine:latest",
			"ImageID": "d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b",
			"State": "running",
			"Status": "Up Less than a second",
			"Created": 1783792600,
			"ExitCode": 0,
			"Command": ["sleep", "1000"],
			"AutoRemove": false
		}
	]`

	var containers []Container
	err := json.Unmarshal([]byte(jsonInput), &containers)
	if err != nil {
		t.Fatalf("Failed to parse container JSON: %v", err)
	}

	if len(containers) != 1 {
		t.Fatalf("Expected 1 container, got %d", len(containers))
	}

	c := containers[0]
	if c.Id != "f0d3a6ae9da9ce991ec825e4c81efcd3a3f6f5a02abbdffc0cc3914a4bbe7899" {
		t.Errorf("Expected ID f0d3a6ae9da9ce991ec825e4c81efcd3a3f6f5a02abbdffc0cc3914a4bbe7899, got %s", c.Id)
	}
	if len(c.Names) == 0 || c.Names[0] != "test-alpine" {
		t.Errorf("Expected name 'test-alpine', got %v", c.Names)
	}
	if c.State != "running" {
		t.Errorf("Expected state 'running', got %s", c.State)
	}
}

func TestImageJSONParsing(t *testing.T) {
	jsonInput := `[
		{
			"Id": "d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b",
			"Names": ["docker.io/library/alpine:latest"],
			"Size": 8709729,
			"CreatedAt": "2026-06-16T00:01:29Z",
			"Containers": 0
		}
	]`

	var images []Image
	err := json.Unmarshal([]byte(jsonInput), &images)
	if err != nil {
		t.Fatalf("Failed to parse image JSON: %v", err)
	}

	if len(images) != 1 {
		t.Fatalf("Expected 1 image, got %d", len(images))
	}

	img := images[0]
	if img.Id != "d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b" {
		t.Errorf("Expected ID d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b, got %s", img.Id)
	}
	if len(img.Names) == 0 || img.Names[0] != "docker.io/library/alpine:latest" {
		t.Errorf("Expected name 'docker.io/library/alpine:latest', got %v", img.Names)
	}
	if img.Size != 8709729 {
		t.Errorf("Expected size 8709729, got %d", img.Size)
	}
}

func TestSystemInfoJSONParsing(t *testing.T) {
	jsonInput := `{
		"host": {
			"os": "linux",
			"kernel": "6.8.0-134-generic",
			"cpus": 2,
			"memTotal": 2063216640,
			"memFree": 354811904,
			"uptime": "9h 25m 20.00s",
			"distribution": {
				"distribution": "ubuntu",
				"version": "24.04"
			}
		},
		"store": {
			"containerStore": {
				"number": 5,
				"running": 2,
				"stopped": 3
			},
			"imageStore": {
				"number": 8
			}
		},
		"version": {
			"Version": "4.9.3"
		}
	}`

	var raw map[string]interface{}
	err := json.Unmarshal([]byte(jsonInput), &raw)
	if err != nil {
		t.Fatalf("Failed to parse raw system info: %v", err)
	}

	info := &SystemInfo{}

	if host, ok := raw["host"].(map[string]interface{}); ok {
		info.OS, _ = host["os"].(string)
		info.Kernel, _ = host["kernel"].(string)
		if cpusVal, ok := host["cpus"].(float64); ok {
			info.CPUs = int(cpusVal)
		}
		if memTotalVal, ok := host["memTotal"].(float64); ok {
			info.MemTotal = int64(memTotalVal)
		}
		if memFreeVal, ok := host["memFree"].(float64); ok {
			info.MemFree = int64(memFreeVal)
		}
		info.Uptime, _ = host["uptime"].(string)
		if dist, ok := host["distribution"].(map[string]interface{}); ok {
			distName, _ := dist["distribution"].(string)
			distVer, _ := dist["version"].(string)
			info.Distribution = distName + " " + distVer
		}
	}

	if store, ok := raw["store"].(map[string]interface{}); ok {
		if cStore, ok := store["containerStore"].(map[string]interface{}); ok {
			if num, ok := cStore["number"].(float64); ok {
				info.TotalContainers = int(num)
			}
			if run, ok := cStore["running"].(float64); ok {
				info.RunningContainers = int(run)
			}
			if stopped, ok := cStore["stopped"].(float64); ok {
				info.StoppedContainers = int(stopped)
			}
		}
		if iStore, ok := store["imageStore"].(map[string]interface{}); ok {
			if num, ok := iStore["number"].(float64); ok {
				info.TotalImages = int(num)
			}
		}
	}

	if version, ok := raw["version"].(map[string]interface{}); ok {
		info.PodmanVersion, _ = version["Version"].(string)
	}

	if info.OS != "linux" {
		t.Errorf("Expected OS 'linux', got %s", info.OS)
	}
	if info.CPUs != 2 {
		t.Errorf("Expected CPUs 2, got %d", info.CPUs)
	}
	if info.Distribution != "ubuntu 24.04" {
		t.Errorf("Expected Distribution 'ubuntu 24.04', got %s", info.Distribution)
	}
	if info.TotalContainers != 5 {
		t.Errorf("Expected TotalContainers 5, got %d", info.TotalContainers)
	}
	if info.RunningContainers != 2 {
		t.Errorf("Expected RunningContainers 2, got %d", info.RunningContainers)
	}
	if info.TotalImages != 8 {
		t.Errorf("Expected TotalImages 8, got %d", info.TotalImages)
	}
	if info.PodmanVersion != "4.9.3" {
		t.Errorf("Expected PodmanVersion '4.9.3', got %s", info.PodmanVersion)
	}
}
