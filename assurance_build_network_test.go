package main

import (
	"strings"
	"testing"
)

func TestDeniedBuildNetworkNeverStartsBuilder(t *testing.T) {
	p, r, s := sessionFixture(t)
	s.GitHub = &GitHubSource{URL: "https://github.com/example/fixture", Commit: strings.Repeat("b", 40)}
	state, err := p.ReviewGitHubSession(s)
	if err != nil {
		t.Fatal(err)
	}
	before := len(r.calls)
	if started, err := p.StartReviewedSessionBuild(state.ID, true, false); err == nil || started != nil || !strings.Contains(err.Error(), "denied RUN networking is unsupported") {
		t.Fatalf("%+v %v", started, err)
	}
	for _, call := range r.calls[before:] {
		if strings.HasPrefix(call, "podman build ") {
			t.Fatal("denial executed builder")
		}
	}
	loaded, err := loadSessionState(state.ID)
	if err != nil || loaded.State != "draft" || loaded.BuildNetwork {
		t.Fatalf("denial changed journal: %+v %v", loaded, err)
	}
}
