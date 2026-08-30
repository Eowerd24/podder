package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// CommandArgv is a structured argv representation of a container command. It
// marshals as a JSON array (the canonical, lossless representation) but can
// unmarshal from either a JSON array or a legacy plain string, so that
// ContainerSpec files written before this hardening pass (which stored
// Command as a single shell-style string and replayed it with
// strings.Fields, silently losing quoting) keep loading. Re-saving a spec
// loaded from the legacy form upgrades it to the array form on disk.
type CommandArgv []string

// UnmarshalJSON accepts a JSON array of strings (current format) or a JSON
// string (legacy format), tokenizing the legacy string with
// SplitShellCommand as a tested migration rather than silently changing
// command semantics.
func (c *CommandArgv) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*c = nil
		return nil
	}

	if trimmed[0] == '[' {
		var arr []string
		if err := json.Unmarshal(data, &arr); err != nil {
			return fmt.Errorf("command array must contain only strings: %w", err)
		}
		*c = arr
		return nil
	}

	var legacy string
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("command field must be a JSON array of strings or a legacy shell string: %w", err)
	}
	if legacy == "" {
		*c = nil
		return nil
	}

	argv, err := SplitShellCommand(legacy)
	if err != nil {
		return fmt.Errorf("failed to migrate legacy command string %q: %w", legacy, err)
	}
	*c = argv
	return nil
}

// MarshalJSON always emits the canonical array form.
func (c CommandArgv) MarshalJSON() ([]byte, error) {
	if c == nil {
		return []byte("null"), nil
	}
	return json.Marshal([]string(c))
}

// SplitShellCommand tokenizes a legacy shell-style command string into argv,
// honoring single quotes (literal), double quotes (backslash-escapable), and
// backslash escapes outside quotes. It exists solely to migrate specs saved
// by the pre-hardening prototype, which stored Command as one string and
// replayed it with strings.Fields (losing any quoting). Unlike
// strings.Fields, this preserves quoted arguments containing spaces:
// `sh -c "echo hello world"` becomes ["sh", "-c", "echo hello world"], not
// ["sh", "-c", "echo", "hello", "world"].
func SplitShellCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	hasCur := false
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if hasCur {
			args = append(args, cur.String())
			cur.Reset()
			hasCur = false
		}
	}

	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			hasCur = true
			escaped = false
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
				hasCur = true
			}
		case inDouble:
			switch r {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
				hasCur = true
			default:
				cur.WriteRune(r)
				hasCur = true
			}
		default:
			switch r {
			case '\'':
				inSingle = true
				hasCur = true
			case '"':
				inDouble = true
				hasCur = true
			case '\\':
				escaped = true
				hasCur = true
			case ' ', '\t', '\n':
				flush()
			default:
				cur.WriteRune(r)
				hasCur = true
			}
		}
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote in command string: %q", s)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash in command string: %q", s)
	}
	flush()
	return args, nil
}

// FormatEntrypointArg renders an entrypoint argv for Podman's --entrypoint
// flag: a single-element entrypoint is passed as a plain string, and a
// multi-element entrypoint is passed as a JSON array, matching the two forms
// `podman run --entrypoint` accepts.
func FormatEntrypointArg(entrypoint []string) (string, error) {
	switch len(entrypoint) {
	case 0:
		return "", fmt.Errorf("entrypoint must not be empty")
	case 1:
		return entrypoint[0], nil
	default:
		data, err := json.Marshal(entrypoint)
		if err != nil {
			return "", fmt.Errorf("failed to encode entrypoint: %w", err)
		}
		return string(data), nil
	}
}
