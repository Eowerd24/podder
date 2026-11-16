package main

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseToolchainAndReachabilityPolicy(t *testing.T) {
	b, e := os.ReadFile("go.mod")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "\ngo 1.26.11\n") {
		t.Fatal("unpinned or unsupported Go directive")
	}
	files, e := filepath.Glob(".github/workflows/*.yml")
	if e != nil {
		t.Fatal(e)
	}
	checked := false
	frontendChecked := false
	for _, file := range files {
		data, _ := os.ReadFile(file)
		var workflow struct {
			Jobs map[string]struct {
				Steps []struct {
					Name     string
					With     map[string]any
					Run      string
					Continue bool `yaml:"continue-on-error"`
				}
			}
		}
		if e = yaml.Unmarshal(data, &workflow); e != nil {
			t.Fatal(e)
		}
		for _, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if v, ok := step.With["go-version"]; ok && v != "1.26.11" {
					t.Fatalf("floating or unsupported Go selector %v", v)
				}
				if step.Name == "Reachable vulnerability check" {
					checked = true
					if step.Continue || !strings.Contains(step.Run, "govulncheck@v1.6.0") || !strings.Contains(step.Run, "-mode=source -scan=symbol ./...") || strings.Contains(step.Run, "||") {
						t.Fatal("reachable findings would not gate CI")
					}
				}
				if step.Name == "Frontend vulnerability check" {
					frontendChecked = true
					if step.Continue || strings.TrimSpace(step.Run) != "npm audit --audit-level=low" {
						t.Fatal("frontend advisories would not gate CI")
					}
				}
			}
		}
	}
	if !checked {
		t.Fatal("missing reachable vulnerability gate")
	}
	if !frontendChecked {
		t.Fatal("missing frontend vulnerability gate")
	}
}
