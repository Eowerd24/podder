package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Session CLI is an opt-in namespace; ordinary Podman passthrough is unchanged.
func runSessionCLI(args []string, input io.Reader, output io.Writer, p *PodmanService) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: podder session list|recipes|starters|preview|launch|stop|export|import|review|build|launch-built; JSON input is read from stdin")
	}
	encode := func(v any) error { enc := json.NewEncoder(output); enc.SetIndent("", "  "); return enc.Encode(v) }
	encodeResult := func(state *SessionRuntimeState, success string) error {
		if err := encode(state); err != nil {
			return err
		}
		if state == nil || state.State != success {
			return fmt.Errorf("requested session operation did not complete successfully; inspect JSON state for recovery")
		}
		return nil
	}
	readSpec := func() (DisposableSessionSpec, error) {
		var spec DisposableSessionSpec
		decoder := json.NewDecoder(io.LimitReader(input, 1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&spec); err != nil {
			return spec, fmt.Errorf("invalid session JSON")
		}
		return spec, validateSessionSource(spec)
	}
	confirmed := false
	for _, arg := range args[1:] {
		if arg == "--confirm" {
			confirmed = true
		}
	}
	switch args[0] {
	case "list":
		states, err := p.ListDisposableSessions()
		if err != nil {
			return err
		}
		return encode(states)
	case "recipes":
		recipes, err := p.ListSessionRecipes()
		if err != nil {
			return err
		}
		return encode(recipes)
	case "starters":
		return encode(p.SessionStarterProfiles())
	case "preview":
		spec, err := readSpec()
		if err != nil {
			return err
		}
		preview, err := p.PreviewDisposableSession(spec)
		if err != nil {
			return err
		}
		return encode(preview)
	case "launch":
		if !confirmed {
			return fmt.Errorf("run preview, then explicitly pass --confirm to launch")
		}
		spec, err := readSpec()
		if err != nil {
			return err
		}
		state, err := p.LaunchDisposableSession(spec)
		if err != nil {
			return err
		}
		return encodeResult(state, "running")
	case "stop":
		if len(args) < 2 {
			return fmt.Errorf("session ID required")
		}
		if !confirmed {
			preview, err := p.PreviewSessionCleanup(args[1])
			if err != nil {
				return err
			}
			return encode(preview)
		}
		removeImage := false
		for _, arg := range args[2:] {
			if arg == "--remove-owned-image" {
				removeImage = true
			}
		}
		state, err := p.StopAndCleanupSession(args[1], true, removeImage)
		if err != nil {
			return err
		}
		return encodeResult(state, "stopped")
	case "export":
		if len(args) != 2 {
			return fmt.Errorf("recipe name required")
		}
		content, err := p.ExportSessionRecipe(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, content)
		return err
	case "import":
		data, err := io.ReadAll(io.LimitReader(input, 65537))
		if err != nil {
			return err
		}
		spec, err := p.ImportSessionRecipe(string(data))
		if err != nil {
			return err
		}
		if !confirmed {
			return encode(spec)
		}
		if err := p.SaveSessionRecipe(*spec); err != nil {
			return err
		}
		return encode(spec)
	case "review":
		spec, err := readSpec()
		if err != nil {
			return err
		}
		state, err := p.ReviewGitHubSession(spec)
		if err != nil {
			return err
		}
		return encodeResult(state, "draft")
	case "build":
		if len(args) < 2 {
			return fmt.Errorf("reviewed session ID required")
		}
		network := false
		for _, arg := range args[2:] {
			if arg == "--allow-network" {
				network = true
			}
		}
		state, err := p.StartReviewedSessionBuild(args[1], confirmed, network)
		if err != nil {
			return err
		}
		defer shutdownLauncherHelpers()
		for sessionBuildActive(state.ID) {
			time.Sleep(100 * time.Millisecond)
		}
		state, err = loadSessionState(state.ID)
		if err != nil {
			return err
		}
		return encodeResult(state, "built")
	case "launch-built":
		if len(args) < 2 || !confirmed {
			return fmt.Errorf("reviewed session ID and --confirm required; supply launch-time env/command JSON on stdin")
		}
		var material struct {
			SessionLaunchConsent
			Env     map[string]string `json:"env"`
			Command []string          `json:"command"`
		}
		if err := json.NewDecoder(io.LimitReader(input, 1024*1024)).Decode(&material); err != nil {
			return err
		}
		state, err := p.LaunchBuiltSessionWithConsent(args[1], material.Env, material.Command, material.SessionLaunchConsent)
		if err != nil {
			return err
		}
		return encodeResult(state, "running")
	default:
		return fmt.Errorf("unknown session action %q", strings.TrimSpace(args[0]))
	}
}
