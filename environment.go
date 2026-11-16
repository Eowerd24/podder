package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const privateEnvironmentPlaceholder = "<private-environment-file>"

// JSON is a presentation/persistence boundary, never a secret transport. The
// private runtime material is stored separately; exported specs require re-entry.
func (s ContainerSpec) MarshalJSON() ([]byte, error) {
	type plain ContainerSpec
	copy := plain(s)
	if len(s.Env) > 0 {
		copy.Env = map[string]string{}
		for key := range s.Env {
			copy.Env[key] = "••••••••"
		}
	}
	return json.Marshal(copy)
}
func environmentRoot() (*os.Root, error) {
	return privateRoot(filepath.Join(sessionRuntimeDir(), "environment"))
}

func storedEnvironmentSpec(s ContainerSpec) (ContainerSpec, error) {
	if len(s.Env) == 0 {
		return s, nil
	}
	root, err := environmentRoot()
	if err != nil {
		return s, err
	}
	defer root.Close()
	// Reuse only an identical private receipt. Replacements never overwrite an old
	// environment needed by a rollback or an unfinished journal.
	if s.EnvironmentID != "" {
		old, e := readEnvironment(root, s.EnvironmentID)
		if e != nil || !reflect.DeepEqual(old, s.Env) {
			s.EnvironmentID = ""
		}
	}
	if s.EnvironmentID == "" {
		s.EnvironmentID = newSessionID()
		if err := writeRootJSON(root, s.EnvironmentID+".json", s.Env); err != nil {
			return s, err
		}
	}
	s.RequiredEnv = nil
	for k := range s.Env {
		s.RequiredEnv = append(s.RequiredEnv, k)
	}
	sort.Strings(s.RequiredEnv)
	s.Env = nil
	return s, nil
}
func readEnvironment(root *os.Root, id string) (map[string]string, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid private environment identity")
	}
	info, err := root.Lstat(id + ".json")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("insecure private environment material")
	}
	env := map[string]string{}
	err = readRootJSON(root, id+".json", &env)
	return env, err
}
func specEnvironment(s ContainerSpec) (map[string]string, error) {
	if len(s.Env) > 0 {
		return s.Env, nil
	}
	if s.EnvironmentID == "" && len(s.RequiredEnv) == 0 {
		return nil, nil
	}
	root, err := environmentRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	env, err := readEnvironment(root, s.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("private environment unavailable; re-enter service environment before replay")
	}
	for _, k := range s.RequiredEnv {
		if _, ok := env[k]; !ok {
			return nil, fmt.Errorf("private environment incomplete; re-entry required")
		}
	}
	return env, nil
}

// Files exist only for the synchronous CLI call. On abrupt parent death a
// private runtime file may remain until runtime-dir cleanup; never unlink while
// a surviving OCI launch may still need it.
func prepareEnvironmentArgs(args []string, env map[string]string) ([]string, func(), error) {
	cleanup := func() {}
	out := append([]string{}, args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--" {
			break
		}
		if out[i] != "--env-file" || out[i+1] != privateEnvironmentPlaceholder {
			continue
		}
		root, err := environmentRoot()
		if err != nil {
			return nil, cleanup, err
		}
		id := "launch-" + newSessionID() + ".env"
		f, err := root.OpenFile(id, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			root.Close()
			return nil, cleanup, err
		}
		cleanup = func() { root.Remove(id); root.Close() }
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.ContainsAny(k, "=\r\n\x00") || strings.ContainsAny(env[k], "\r\n\x00") {
				f.Close()
				cleanup()
				return nil, func() {}, fmt.Errorf("environment cannot be represented in a line-based env file")
			}
			if _, err = fmt.Fprintf(f, "%s=%s\n", k, env[k]); err != nil {
				break
			}
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("could not write private environment")
		}
		out[i+1] = filepath.Join(root.Name(), id)
		return out, cleanup, nil
	}
	return out, cleanup, nil
}
func (p *PodmanService) runSpecCommand(s ContainerSpec, args ...string) (string, string, error) {
	env, err := specEnvironment(s)
	if err != nil {
		return "", "", err
	}
	pinned, closePinned, err := pinApprovedContainerBinds(s.Binds)
	if err != nil {
		return "", "", err
	}
	defer closePinned()
	executionBinds, plan, err := p.preparePinnedMountPlan("command-"+newSessionID(), pinned)
	if err != nil {
		return "", "", err
	}
	defer cleanupPinnedMountPlan(plan)
	if p.sessionMountsPinnedHook != nil {
		p.sessionMountsPinnedHook()
	}
	executionSpec := s
	executionSpec.Binds = executionBinds
	var canonical []string
	if len(args) > 0 && args[0] == "create" {
		canonical, err = BuildCreateArgsFromSpec(executionSpec)
	} else {
		canonical, err = BuildRunArgsFromSpec(executionSpec)
	}
	if err != nil {
		return "", "", err
	}
	actual, cleanup, err := prepareEnvironmentArgs(canonical, env)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	if len(plan.pairs) != 0 {
		actual = pinnedPodmanOuterArgs(plan, actual)
	}
	var out, stderr string
	if len(plan.files) != 0 {
		out, stderr, err = p.runCommandWithFiles(plan.files, actual...)
	} else {
		out, stderr, err = p.runCommand(actual...)
	}
	if validateFullContainerID(strings.TrimSpace(out)) == nil {
		return out, redactEnvironment(stderr, env), redactEnvironmentError(err, env)
	}
	return redactEnvironment(out, env), redactEnvironment(stderr, env), redactEnvironmentError(err, env)
}
func (p *PodmanService) runSessionWithEnvironment(ctx context.Context, env map[string]string, args ...string) (string, string, error) {
	actual, cleanup, err := prepareEnvironmentArgs(args, env)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	out, stderr, err := p.runSessionCommand(ctx, "podman", actual...)
	if validateFullContainerID(strings.TrimSpace(out)) == nil {
		return out, redactEnvironment(stderr, env), redactEnvironmentError(err, env)
	}
	return redactEnvironment(out, env), redactEnvironment(stderr, env), redactEnvironmentError(err, env)
}

const pinnedMountNamespaceScript = `
count=$1
prefix_count=$2
barrier_ready=$3
barrier_continue=$4
shift 4
sources=()
destinations=()
i=0
while [ "$i" -lt "$count" ]; do
	source=$1
	target=$2
	destination=$3
	shift 3
	[ -e "$target" ]
	target_object=$(readlink -f -- "$target")
	[ -n "$target_object" ]
	mount --bind "$source" "$target_object"
	sources+=("$source")
	destinations+=("$destination")
	i=$((i + 1))
done
if [ -n "$barrier_ready" ]; then
	[ -n "$barrier_continue" ]
	: >"$barrier_ready"
	while [ ! -e "$barrier_continue" ]; do sleep 0.01; done
fi
[ "$1" = "--" ]
shift
args=("$@")
verb=${args[$prefix_count]}
prefix=("${args[@]:0:$prefix_count}")
verify_mounts() {
	pid=$(podman "${prefix[@]}" inspect --format '{{.State.Pid}}' -- "$id")
	[[ "$pid" =~ ^[1-9][0-9]*$ ]]
	i=0
	while [ "$i" -lt "$count" ]; do
		source_identity=$(stat -Lc '%d:%i' -- "${sources[$i]}")
		mounted_identity=$(stat -Lc '%d:%i' -- "/proc/$pid/root${destinations[$i]}")
		[ "$source_identity" = "$mounted_identity" ]
		i=$((i + 1))
	done
}
if [ "$verb" = "start" ]; then
	id=${args[${#args[@]}-1]}
	[[ "$id" =~ ^[0-9a-f]{64}$ ]]
	cleanup_start() { podman "${prefix[@]}" container cleanup -- "$id" >/dev/null 2>&1 || true; }
	trap cleanup_start EXIT
	podman "${prefix[@]}" init -- "$id" >/dev/null
	verify_mounts
	podman "${prefix[@]}" start -- "$id" >/dev/null
	trap - EXIT
	exit 0
fi
[ "$verb" = "run" ] || exec podman "${args[@]}"
create_args=("${args[@]:0:$prefix_count}" create)
detached=0
i=$((prefix_count + 1))
while [ "$i" -lt "${#args[@]}" ]; do
	if [ "${args[$i]}" = "--" ]; then
		create_args+=("${args[@]:$i}")
		break
	fi
	if [ "$detached" -eq 0 ] && [ "${args[$i]}" = "-d" ]; then
		detached=1
	else
		create_args+=("${args[$i]}")
	fi
	i=$((i + 1))
done
[ "$detached" -eq 1 ]
args=("${create_args[@]}")
id=$(podman "${args[@]}")
[[ "$id" =~ ^[0-9a-f]{64}$ ]]
cleanup() { podman "${prefix[@]}" rm -f -- "$id" >/dev/null 2>&1 || true; }
trap cleanup EXIT
podman "${prefix[@]}" init -- "$id" >/dev/null
verify_mounts
podman "${prefix[@]}" start -- "$id" >/dev/null
trap - EXIT
printf '%s\n' "$id"
`

func pinnedPodmanOuterArgs(plan pinnedMountPlan, actual []string) []string {
	outer := []string{"unshare", "bash", "-ceu", pinnedMountNamespaceScript, "podder-pinned-mount", fmt.Sprint(len(plan.pairs)), fmt.Sprint(len(plan.innerPrefix)), plan.barrierReady, plan.barrierContinue}
	for _, pair := range plan.pairs {
		outer = append(outer, pair[0], pair[1], pair[2])
	}
	outer = append(outer, "--")
	outer = append(outer, plan.innerPrefix...)
	return append(outer, actual...)
}

func (p *PodmanService) runSessionWithPinnedEnvironment(ctx context.Context, env map[string]string, plan pinnedMountPlan, args ...string) (string, string, error) {
	if len(plan.pairs) == 0 {
		return p.runSessionWithEnvironment(ctx, env, args...)
	}
	actual, cleanup, err := prepareEnvironmentArgs(args, env)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	outer := pinnedPodmanOuterArgs(plan, actual)
	out, stderr, err := p.runSessionCommandWithFiles(ctx, "podman", plan.files, outer...)
	if p.sessionRunResultHook != nil {
		p.sessionRunResultHook(redactEnvironment(stderr, env), redactEnvironmentError(err, env))
	}
	if validateFullContainerID(strings.TrimSpace(out)) == nil {
		return out, redactEnvironment(stderr, env), redactEnvironmentError(err, env)
	}
	return redactEnvironment(out, env), redactEnvironment(stderr, env), redactEnvironmentError(err, env)
}
func redactEnvironment(s string, env map[string]string) string {
	values := make([]string, 0, len(env))
	for _, v := range env {
		if v != "" {
			values = append(values, v)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	pairs := make([]string, 0, len(values)*2)
	for _, v := range values {
		pairs = append(pairs, v, "<redacted>")
	}
	// A single pass never interprets a replacement marker as another secret.
	return strings.NewReplacer(pairs...).Replace(s)
}
func redactEnvironmentError(err error, env map[string]string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", redactEnvironment(err.Error(), env))
}

// SetServiceEnvironment re-enters runtime material after logout/reboot. It
// cannot alter the declared key set, ownership or any other replay setting.
func (p *PodmanService) SetServiceEnvironment(service string, env map[string]string) error {
	release, err := lockManagedTransaction()
	if err != nil {
		return err
	}
	defer release()
	if err := checkManagedRecovery(service); err != nil {
		return err
	}
	s, err := p.GetSpec(service)
	if err != nil {
		return err
	}
	if !sessionIDPattern.MatchString(s.EnvironmentID) {
		return fmt.Errorf("service has no private environment reference")
	}
	if len(env) != len(s.RequiredEnv) {
		return fmt.Errorf("environment key set must match the service")
	}
	for _, k := range s.RequiredEnv {
		if _, ok := env[k]; !ok {
			return fmt.Errorf("environment key set must match the service")
		}
	}
	s.Env = env
	if errs := ValidateSpec(*s); len(errs) > 0 {
		return fmt.Errorf("invalid environment: %s", strings.Join(errs, "; "))
	}
	root, err := environmentRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return writeRootJSON(root, s.EnvironmentID+".json", env)
}
