package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSplitShellCommand(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"simple", "nginx -g daemon", []string{"nginx", "-g", "daemon"}, false},
		{"double-quoted-phrase", `sh -c "echo hello world"`, []string{"sh", "-c", "echo hello world"}, false},
		{"single-quoted-phrase", `sh -c 'echo hello world'`, []string{"sh", "-c", "echo hello world"}, false},
		{"escaped-space", `a\ b c`, []string{"a b", "c"}, false},
		{"empty-quoted-arg", `cmd ""`, []string{"cmd", ""}, false},
		{"nested-quote-in-double", `sh -c "echo it's fine"`, []string{"sh", "-c", "echo it's fine"}, false},
		{"empty-string", "", nil, false},
		{"unterminated-double", `sh -c "echo`, nil, true},
		{"unterminated-single", `sh -c 'echo`, nil, true},
		{"trailing-backslash", `sh\`, nil, true},
		{"multiple-spaces", "a   b", []string{"a", "b"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitShellCommand(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got argv %v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SplitShellCommand(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCommandArgvUnmarshalMigratesLegacyString(t *testing.T) {
	var spec ContainerSpec
	raw := []byte(`{"name":"x","image":"y","command":"sh -c \"echo hello world\""}`)
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	want := CommandArgv{"sh", "-c", "echo hello world"}
	if !reflect.DeepEqual(spec.Command, want) {
		t.Fatalf("legacy command string migrated to %v, want %v", spec.Command, want)
	}

	// Re-marshaling must upgrade to the canonical array form.
	out, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	var roundTrip map[string]interface{}
	if err := json.Unmarshal(out, &roundTrip); err != nil {
		t.Fatalf("unexpected error re-parsing marshaled spec: %v", err)
	}
	if _, ok := roundTrip["command"].([]interface{}); !ok {
		t.Fatalf("expected command to marshal as a JSON array, got: %s", out)
	}
}

func TestCommandArgvUnmarshalAcceptsArray(t *testing.T) {
	var spec ContainerSpec
	raw := []byte(`{"name":"x","image":"y","command":["sh","-c","echo hello world"]}`)
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	want := CommandArgv{"sh", "-c", "echo hello world"}
	if !reflect.DeepEqual(spec.Command, want) {
		t.Fatalf("got %v, want %v", spec.Command, want)
	}
}

func TestCommandArgvUnmarshalRejectsInvalidLegacyString(t *testing.T) {
	var spec ContainerSpec
	raw := []byte(`{"name":"x","image":"y","command":"sh -c \"unterminated"}`)
	if err := json.Unmarshal(raw, &spec); err == nil {
		t.Fatalf("expected error migrating an unterminated legacy command string")
	}
}

func TestFormatEntrypointArg(t *testing.T) {
	got, err := FormatEntrypointArg([]string{"/bin/sh"})
	if err != nil || got != "/bin/sh" {
		t.Fatalf("single entrypoint: got %q, err %v", got, err)
	}

	got, err = FormatEntrypointArg([]string{"/bin/sh", "-c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `["/bin/sh","-c"]` {
		t.Fatalf("multi-element entrypoint got %q", got)
	}

	if _, err := FormatEntrypointArg(nil); err == nil {
		t.Fatalf("expected error for empty entrypoint")
	}
}
