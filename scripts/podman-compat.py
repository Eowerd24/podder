#!/usr/bin/env python3
"""Run opt-in rootless Podman integration tests in a disposable Linux VM.

The host Podman executable and storage are never used. Images are authenticated
by committed digests from their official distribution servers. Logs and VM
credentials stay in a private temporary directory; only the compact result is
written to --output. A normal run stops QEMU and removes its disk and SSH keys.
"""

import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.parse
import urllib.request
import uuid


TARGETS = {
    "debian13": {
        "url": "https://cloud.debian.org/images/cloud/trixie/20261111-2659/debian-13-genericcloud-amd64-20261111-2659.qcow2",
        "algorithm": "sha512",
        "digest": "3247873136ba09b060fab45e9c115239f81c1d401fce00b8fa9b872013f7135d6a40b12462ab5ac03b9fa4ed5955dd0e8d1c900109a939ad7a0a4dafe7dea47b",
        "version": "5.4.2",
        "packages": ["podman=5.4.2+ds1-2+b2", "uidmap", "crun", "passt", "slirp4netns", "dbus-user-session", "openssh-server", "iproute2", "libgtk-4-1", "libwebkitgtk-6.0-4", "ca-certificates", "tar", "gzip", "sudo"],
    },
    "fedora45": {
        "url": "https://dl.fedoraproject.org/pub/fedora/linux/releases/45/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-45-1.4.x86_64.qcow2",
        "algorithm": "sha256",
        "digest": "50bdc5e70f6a24f540932d15411c1c6ae9fcfa40fb990d876d83c5cc823b2ad0",
        "version": "6.1.3",
        "packages": ["podman-6.1.3-1.fc45", "crun", "passt", "slirp4netns", "shadow-utils-subid", "dbus-daemon", "openssh-server", "iproute", "gtk4", "webkitgtk6.0", "ca-certificates", "tar", "gzip", "sudo"],
    },
}

# This is the verified commit behind the signed upstream v6.2.0 tag, rather
# than a moving branch or the remote-client-only Linux release artifact.
UPSTREAM_COMMIT = "055de046937d29450f310fce0fe3071485d8bed9"
TARGETS["fedora45-upstream6.2"] = {
    **TARGETS["fedora45"],
    "version": "6.2.0",
    "upstream_commit": UPSTREAM_COMMIT,
    "packages": [*TARGETS["fedora45"]["packages"], "git-core", "dnf5-plugins"],
}

DEFAULT_TESTS = [
    "TestPodmanCompatibility",
    "TestRound2N4Adoption",
    "TestRound2N2LostRename",
    "TestRound2N3StartFailure",
    "TestRound2AdoptionLostResults",
    "TestAssuranceKeepIDSession",
    "TestAssurancePrivateEnvironmentTransport",
    "TestAssuranceManagedBindUsesVerifiedStartHandoff",
    "TestAssuranceAdoptionCounterfactual",
    "TestAssuranceAdoptionPostVerifyCounterfactual",
    "TestAssuranceAdoptionBindPositiveControl",
    "TestAssuranceAdoptionBindSourceReplacementRefused",
    "TestAssurancePinnedMountSurvivesPathReplacement",
    "TestAssuranceUnmanagedBindRestartRefusesMissingApprovalWitness",
    "TestAssuranceSessionLockNonLeak",
    "TestAssuranceOCILaunchParentDeath",
]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(args, **kwargs):
    return subprocess.run([str(a) for a in args], check=True, **kwargs)


def sha(path, algorithm="sha256"):
    result = hashlib.new(algorithm)
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def verify_image(path, target):
    require(sha(path, target["algorithm"]) == target["digest"], "Cloud-image digest mismatch")


def download_image(destination, target):
    # Redirects are expected for distribution mirrors. HTTPS is mandatory at
    # every redirect, and the independently pinned digest authenticates bytes.
    class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, request, fp, code, message, headers, newurl):
            require(urllib.parse.urlparse(newurl).scheme == "https", "Refusing non-HTTPS image redirect")
            return super().redirect_request(request, fp, code, message, headers, newurl)

    require(urllib.parse.urlparse(target["url"]).scheme == "https", "Image URL is not HTTPS")
    opener = urllib.request.build_opener(HTTPSRedirect())
    request = urllib.request.Request(target["url"], headers={"User-Agent": "podder-compatibility/1"})
    with opener.open(request, timeout=90) as response, Path(destination).open("xb") as output:
        require(urllib.parse.urlparse(response.url).scheme == "https", "Image URL is not HTTPS")
        shutil.copyfileobj(response, output, 1024 * 1024)
    verify_image(destination, target)


def source_archive(repo, destination):
    entries = subprocess.check_output(["git", "-C", str(repo), "ls-files", "-z", "--cached", "--others", "--exclude-standard"])
    deleted = subprocess.check_output(["git", "-C", str(repo), "ls-files", "-z", "--deleted"])
    deleted = {os.fsdecode(p) for p in deleted.split(b"\0") if p}
    files = sorted(set(os.fsdecode(p) for p in entries.split(b"\0") if p) - deleted)
    require(files, "Source snapshot is empty")
    with tarfile.open(destination, "w:gz", compresslevel=1) as archive:
        for name in files:
            relative = Path(name)
            require(not relative.is_absolute() and ".." not in relative.parts and ".git" not in relative.parts, "Unsafe source path")
            path = repo / relative
            require(path.is_file() and not path.is_symlink(), "Only regular source files may be transferred")
            archive.add(path, arcname="source/" + relative.as_posix(), recursive=False)


def go_archive(go_dir, destination):
    require((go_dir / "bin/go").is_file(), "--go-dir must contain bin/go")
    with tarfile.open(destination, "w:gz", compresslevel=1) as archive:
        archive.add(go_dir, arcname="go")


def cloud_config(target, public_key, host_private, host_public, token):
    # JSON is a YAML subset accepted by cloud-init; no hand-escaped YAML or
    # shell interpolation of SSH keys is needed.
    return "#cloud-config\n" + json.dumps({
        # Fedora cloud-init may try hostnamectl during init-local, before its
        # system bus exists. Supply no early hostname and set it explicitly in
        # runcmd, once the normal services are available. Readiness still rejects
        # all degraded cloud-init states rather than waiving hostname warnings.
        "preserve_hostname": True,
        "users": [{"name": "poddercompat", "homedir": "/home/poddercompat", "groups": ["wheel"] if target.startswith("fedora45") else ["sudo"],
                   "shell": "/bin/bash", "lock_passwd": True,
                   "sudo": "ALL=(ALL) NOPASSWD:ALL", "ssh_authorized_keys": [public_key.strip()]}],
        "ssh_pwauth": False, "disable_root": True,
        "ssh_keys": {"ed25519_private": host_private, "ed25519_public": host_public.strip()},
        "write_files": [{"path": "/etc/ssh/sshd_config.d/90-podder-compat.conf", "permissions": "0600",
                         "content": "PermitRootLogin no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n"}],
        "package_update": True, "packages": TARGETS[target]["packages"],
        "runcmd": [
            ["hostnamectl", "set-hostname", "podder-compat-" + token[:12]],
            ["bash", "-ceu", "grep -q '^poddercompat:' /etc/subuid || printf 'poddercompat:100000:65536\\n' >> /etc/subuid; grep -q '^poddercompat:' /etc/subgid || printf 'poddercompat:100000:65536\\n' >> /etc/subgid"],
            ["loginctl", "enable-linger", "poddercompat"],
            ["bash", "-ceu", "systemctl start user@$(id -u poddercompat).service"],
            ["bash", "-ceu", "compat_uid=$(id -u poddercompat); runuser -u poddercompat -- env -u DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR=/run/user/$compat_uid systemctl --user daemon-reload; runuser -u poddercompat -- env -u DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR=/run/user/$compat_uid systemctl --user start dbus.socket; test -S /run/user/$compat_uid/bus"],
            ["systemctl", "enable", "--now", "sshd" if target.startswith("fedora45") else "ssh"],
            ["bash", "-ceu", "install -m 0644 /dev/null /var/lib/podder-compat-ready"],
        ],
    }) + "\n"


def cloud_metadata(token):
    # No local-hostname: cloud-init's early hostname stage runs before the
    # system bus in Fedora45. cloud_config sets it later without warnings.
    return {"instance-id": "podder-compat-" + token}


def ssh_options(work, port):
    return ["-F", "/dev/null", "-i", str(work / "client_ed25519"),
            "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
            "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + str(work / "known_hosts"),
            "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostKeyAlias=podder-compat",
            "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=15",
            "-o", "ServerAliveCountMax=3", "-p", str(port)]


def ssh(work, port, command, **kwargs):
    return subprocess.run(["ssh", *ssh_options(work, port), "poddercompat@127.0.0.1", command], **kwargs)


def copy_to_guest(work, port, path):
    # scp uses -P, whereas the shared ssh options use -p.
    options = ssh_options(work, port)
    options[-2] = "-P"
    run(["scp", *options, path, "poddercompat@127.0.0.1:incoming/"], stdout=subprocess.DEVNULL)


def build_upstream(work, port, commit, timeout):
    require(re.fullmatch("[0-9a-f]{40}", commit), "Invalid pinned upstream commit")
    # All source execution and package installation happen inside the dedicated
    # guest. Preserve the distribution's runtime configuration and dependencies;
    # build the full engine and matching port/Quadlet helpers from one commit.
    script = "\n".join([
        "set -eu",
        "cd podder-compat",
        "export PATH=\"$PWD/go/bin:$PATH\"",
        "export GOCACHE=/home/poddercompat/.cache/go-build GOPATH=/home/poddercompat/gopath",
        "export GOTOOLCHAIN=local CGO_ENABLED=1 GOMEMLIMIT=1500MiB GOMAXPROCS=2",
        "export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0",
        "git init -q upstream",
        "cd upstream",
        "git remote add origin https://github.com/podman-container-tools/podman.git",
        "git -c credential.helper= fetch --depth=1 origin " + commit,
        "git -c advice.detachedHead=false checkout --detach FETCH_HEAD",
        "test \"$(git rev-parse HEAD)\" = " + commit,
        "sudo dnf -y builddep rpm/podman.spec",
        "sudo dnf -y install 'containers-common-extra >= 5:0.68.0' catatonit conmon podman-sequoia",
        "make -j1 bin/podman bin/rootlessport bin/quadlet PREFIX=/usr GO=\"$PWD/../go/bin/go\" GOFLAGS='-trimpath -mod=vendor -p=2' GIT_COMMIT=" + commit,
        "sudo install -m 0755 bin/podman /usr/bin/podman.upstream",
        "sudo mv /usr/bin/podman.upstream /usr/bin/podman",
        "sudo install -m 0755 bin/rootlessport /usr/libexec/podman/rootlessport",
        "sudo install -m 0755 bin/quadlet /usr/libexec/podman/quadlet",
        "sudo restorecon /usr/bin/podman /usr/libexec/podman/rootlessport /usr/libexec/podman/quadlet",
    ])
    print("Building upstream Podman6.2.0 and matching helpers inside the guest", flush=True)
    completed = ssh(work, port, script, capture_output=True, text=True, timeout=timeout)
    (work / "upstream-build.log").write_text(completed.stdout + completed.stderr)
    require(completed.returncode == 0, "Upstream guest build failed: " + completed.stderr[-2000:])


def validate_engine_environment(version, info, target):
    client = version.get("Client", version)
    actual_version = client.get("Version", "")
    require(actual_version == target["version"], "Expected Podman " + target["version"] + ", found " + actual_version)
    if "upstream_commit" in target:
        require(client.get("GitCommit") == target["upstream_commit"], "Engine does not identify the pinned upstream source commit")
    host = info.get("host", info.get("Host", {}))
    require(host.get("security", host.get("Security", {})).get("rootless", False), "Guest engine is not rootless")
    require(host.get("cgroupManager") == "systemd", "Default guest engine fell back from systemd cgroups")
    require(host.get("cgroupVersion") == "v2", "Default guest engine must use cgroup v2")
    require(info.get("store", {}).get("graphDriverName") == "overlay", "Default guest engine must use overlay storage")
    require(host.get("rootlessNetworkCmd") == "pasta", "Default guest engine must use pasta networking")
    return actual_version, host


def validate_allowed_skips(expected, allowed_skips):
    require(all(isinstance(name, str) and "/" in name and name.split("/", 1)[0] in expected
                and name.split("/", 1)[1] for name in allowed_skips),
            "Skip exceptions must name qualified child cases of selected tests; selected top-level tests must pass")


def test_results(events, expected, allowed_skips=()):
    validate_allowed_skips(expected, allowed_skips)
    final = {}
    skips = set()
    failures = set()
    package_failed = False
    package_finished = False
    count = 0
    for line in events.splitlines():
        if not line.strip():
            continue
        event = json.loads(line)
        require(isinstance(event, dict) and isinstance(event.get("Action"), str), "Malformed test2json event")
        count += 1
        name, action = event.get("Test"), event["Action"]
        if not name and action == "fail":
            package_failed = True
        if not name and action in {"pass", "fail"}:
            package_finished = True
        if name and action in {"pass", "fail", "skip"}:
            final[name] = action
            if action == "skip":
                skips.add(name)
            if action == "fail":
                failures.add(name)
    require(count > 0, "No test2json events were produced")
    missing = [name for name in expected if name not in final]
    unexpected_skips = sorted(skips - set(allowed_skips))
    failed = sorted(failures)
    selected = {name: final.get(name, "missing") for name in expected}
    passed = package_finished and not package_failed and not missing and not failed and not unexpected_skips
    passed = passed and all(result == "pass" for result in selected.values())
    return {"passed": passed, "selected": selected, "subtests_passed": sum("/" in n and a == "pass" for n, a in final.items()),
            "package_finished": package_finished, "missing": missing, "failed": failed, "unexpected_skips": unexpected_skips}


def proc_identity(pid):
    try:
        fields = Path("/proc", str(pid), "stat").read_text().rpartition(") ")[2].split()
        if fields[0] in {"Z", "X"}:
            return None
        return {"start_ticks": fields[19], "cmdline_sha256": sha(Path("/proc", str(pid), "cmdline")),
                "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip()}
    except (FileNotFoundError, ProcessLookupError, PermissionError):
        return None


def pid_namespace():
    return os.readlink("/proc/self/ns/pid")


@contextlib.contextmanager
def termination_signals(ignore=False):
    def terminate(signum, _frame):
        raise InterruptedError("Compatibility run interrupted by " + signal.Signals(signum).name)

    previous = {sig: signal.getsignal(sig) for sig in [signal.SIGTERM, signal.SIGINT]}
    try:
        for sig in previous:
            signal.signal(sig, signal.SIG_IGN if ignore else terminate)
        yield
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


def stop_owned_vm(state):
    pid = state["pid"]
    namespace = state.get("pid_namespace")
    if namespace is not None:
        require(namespace == pid_namespace(), "VM was launched in another PID namespace; run cleanup in its launch context")
    current = proc_identity(pid)
    if current is None:
        require(namespace is not None, "Legacy VM process is not visible; cannot prove it stopped in this PID namespace")
        return
    require(current == state["identity"], "QEMU process identity changed; refusing to signal it")
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        current = proc_identity(pid)
        if current is None:
            return
        if current != state["identity"]:
            # A zombie retains its process identity but can no longer access
            # the disk. A reused PID must never receive a follow-up signal.
            return
        time.sleep(0.1)
    if proc_identity(pid) == state["identity"]:
        os.kill(pid, signal.SIGKILL)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            current = proc_identity(pid)
            if current is None or current != state["identity"]:
                return
            time.sleep(0.1)
        require(proc_identity(pid) != state["identity"],
                "Owned QEMU process remains alive after SIGKILL; retain its disk and credentials")


def cleanup_directory(work):
    work = work.resolve()
    require(work.parent == Path(tempfile.gettempdir()).resolve() and work.name.startswith("podder-compat-"), "Not a compatibility temporary directory")
    info = work.lstat()
    require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid() and not info.st_mode & 0o077,
            "Temporary directory must be private and owned")
    state_file = work / "vm.json"
    if state_file.exists():
        stop_owned_vm(json.loads(state_file.read_text()))
    shutil.rmtree(work)


def run_vm(args):
    repo = args.repo.resolve()
    output = args.output.resolve()
    require(repo != output and repo not in output.parents, "--output must be outside the source repository")
    tests = list(dict.fromkeys(args.test or DEFAULT_TESTS))
    require(tests and all(re.fullmatch(r"Test[A-Za-z0-9_]+", n) for n in tests), "Expected explicit top-level Go test names")
    validate_allowed_skips(tests, args.allow_skip)
    require(args.test_binary.is_file() and not args.test_binary.is_symlink(), "A compiled integration test executable is required")
    require(Path("/dev/kvm").exists() and os.access("/dev/kvm", os.R_OK | os.W_OK), "KVM access is required; no slow/emulated fallback is selected implicitly")
    for executable in ["qemu-system-x86_64", "qemu-img", "genisoimage", "ssh", "scp", "ssh-keygen"]:
        require(shutil.which(executable), "Missing host executable: " + executable)
    work = Path(tempfile.mkdtemp(prefix="podder-compat-"))
    os.chmod(work, 0o700)
    result = {"schema": 1, "target": args.target, "expected_podman": TARGETS[args.target]["version"],
              "source_commit": subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip(),
              "source_dirty": bool(subprocess.check_output(["git", "-C", str(repo), "status", "--porcelain"], text=True)),
              "tests": tests, "passed": False}
    process = None
    try:
        print("Compatibility workspace: " + str(work), flush=True)
        # Snapshot before provisioning: a caller may make the next repair while
        # this VM boots. Always execute the copied binary and keep its exact
        # accompanying source snapshot, rather than rereading moving paths.
        source_archive(repo, work / "source.tar.gz")
        shutil.copyfile(args.test_binary, work / "podder.test")
        os.chmod(work / "podder.test", 0o700)
        go_dir = args.go_dir.resolve()
        go_archive(go_dir, work / "go.tar.gz")
        result["source_archive_sha256"] = sha(work / "source.tar.gz")
        result["test_binary_sha256"] = sha(work / "podder.test")
        result["go_toolchain"] = subprocess.check_output([str(go_dir / "bin/go"), "version"], cwd=work, text=True).strip()
        result["fixture_profiles"] = {"default_stack_test": "TestPodmanCompatibility" if "TestPodmanCompatibility" in tests else None,
                                      "legacy_vfs_cgroupfs_tests": [name for name in tests if name != "TestPodmanCompatibility"]}
        target = TARGETS[args.target]
        image = work / "base.qcow2"
        if args.image is None:
            print("Downloading pinned " + args.target + " image", flush=True)
            download_image(image, target)
        else:
            require(args.image.is_file() and not args.image.is_symlink(), "--image must be a regular pinned cloud image")
            # QEMU must consume the bytes checked here, rather than an external
            # path that could be replaced between hashing and opening it.
            shutil.copyfile(args.image, image)
            verify_image(image, target)
        result["cloud_image"] = {"url": target["url"], target["algorithm"]: target["digest"]}
        run(["qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", image, work / "guest.qcow2", "24G"])
        for name in ["client_ed25519", "host_ed25519"]:
            run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", work / name], stdout=subprocess.DEVNULL)
        host_public = (work / "host_ed25519.pub").read_text()
        (work / "known_hosts").write_text("podder-compat " + host_public)
        token = uuid.uuid4().hex
        (work / "user-data").write_text(cloud_config(args.target, (work / "client_ed25519.pub").read_text(),
                                                     (work / "host_ed25519").read_text(), host_public, token))
        (work / "meta-data").write_text(json.dumps(cloud_metadata(token)) + "\n")
        run(["genisoimage", "-quiet", "-output", work / "seed.iso", "-volid", "cidata", "-joliet", "-rock", work / "user-data", work / "meta-data"])
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        command = ["qemu-system-x86_64", "-name", "podder-compat-" + token, "-enable-kvm", "-cpu", "host",
                   "-smp", str(args.cpus), "-m", str(args.memory), "-drive", "file=" + str(work / "guest.qcow2") + ",if=virtio,format=qcow2",
                   "-drive", "file=" + str(work / "seed.iso") + ",media=cdrom,readonly=on", "-netdev",
                   "user,id=net0,hostfwd=tcp:127.0.0.1:" + str(port) + "-:22", "-device", "virtio-net-pci,netdev=net0",
                   "-device", "virtio-rng-pci", "-display", "none", "-monitor", "none", "-serial", "file:" + str(work / "serial.log")]
        with (work / "qemu.log").open("wb") as log:
            process = subprocess.Popen(command, stdout=log, stderr=log, start_new_session=True)
        time.sleep(0.2)
        require(process.poll() is None, "QEMU failed to start; inspect the private qemu.log with --keep-vm")
        state = {"pid": process.pid, "identity": proc_identity(process.pid), "pid_namespace": pid_namespace(),
                 "port": port, "target": args.target}
        require(state["identity"] is not None, "QEMU process disappeared")
        (work / "vm.json").write_text(json.dumps(state, indent=2) + "\n")
        deadline = time.monotonic() + args.boot_timeout
        print("Waiting for rootless guest provisioning", flush=True)
        while time.monotonic() < deadline:
            require(process.poll() is None, "QEMU exited during provisioning")
            status = ssh(work, port, "test -f /var/lib/podder-compat-ready && sudo cloud-init status --wait --format json", capture_output=True, text=True, timeout=20)
            if status.returncode == 0:
                break
            if status.returncode == 2 and status.stdout.strip():
                raise RuntimeError("Guest cloud-init reported provisioning errors: " + status.stdout[-2000:])
            time.sleep(5)
        else:
            raise RuntimeError("Guest provisioning timed out; inspect private serial.log with --keep-vm")
        print("Guest ready; transferring source and Go toolchain", flush=True)
        run_dir = "mkdir -m 700 -p incoming podder-compat && chmod 700 incoming podder-compat"
        require(ssh(work, port, run_dir).returncode == 0, "Guest workspace creation failed")
        for path in [work / "source.tar.gz", work / "go.tar.gz", work / "podder.test"]:
            copy_to_guest(work, port, path)
        setup = "cd podder-compat && tar -xzf ../incoming/source.tar.gz && tar -xzf ../incoming/go.tar.gz && cp ../incoming/podder.test ./podder.test && chmod 700 podder.test"
        require(ssh(work, port, setup).returncode == 0, "Guest source extraction failed")
        if "upstream_commit" in target:
            build_upstream(work, port, target["upstream_commit"], args.build_timeout)
            result["upstream_source"] = {"repository": "https://github.com/podman-container-tools/podman",
                                         "commit": target["upstream_commit"],
                                         "binaries": ["podman", "rootlessport", "quadlet"],
                                         "runtime_dependencies": "Fedora45 distribution packages"}
        metadata = []
        for command in ["podman version --format json", "podman info --format json"]:
            response = ssh(work, port, "export DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u)/bus; " + command,
                           capture_output=True, text=True, timeout=60)
            require(response.returncode == 0, "Rootless engine preflight failed: " + response.stderr[-2000:])
            metadata.append(json.loads(response.stdout))
        version, info = metadata
        actual_version, host = validate_engine_environment(version, info, target)
        result["podman_version"] = actual_version
        result["environment"] = {"kernel": host.get("kernel"), "distribution": host.get("distribution"),
                                 "cgroup_version": host.get("cgroupVersion"), "oci_runtime": host.get("ociRuntime"),
                                 "cgroup_manager": host.get("cgroupManager"), "rootless_network_command": host.get("rootlessNetworkCmd"),
                                 "storage_driver": info.get("store", {}).get("graphDriverName"),
                                 "conmon": host.get("conmon"), "network_backend_info": host.get("networkBackendInfo"),
                                 "network_backend": host.get("networkBackend"), "security": host.get("security")}
        pattern = "^(" + "|".join(tests) + ")$"
        environment = "PATH=\"$PWD/../go/bin:$PATH\" GOCACHE=/home/poddercompat/.cache/go-build GOPATH=/home/poddercompat/gopath DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u)/bus GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off PODDER_ASSURANCE_INTEGRATION=1 PODDER_COMPAT_EXPECT_VERSION=" + shlex.quote(actual_version)
        command = "cd podder-compat/source && " + environment + " ../go/bin/go tool test2json -t -p github.com/Eowerd24/podder ../podder.test -test.v -test.count=1 -test.timeout=" + shlex.quote(args.test_timeout) + " -test.run=" + shlex.quote(pattern)
        print("Running " + str(len(tests)) + " selected tests on Podman " + actual_version, flush=True)
        completed = ssh(work, port, command, capture_output=True, text=True, timeout=args.command_timeout)
        (work / "test-events.jsonl").write_text(completed.stdout)
        (work / "test-stderr.log").write_text(completed.stderr)
        result.update(test_results(completed.stdout, tests, args.allow_skip))
        result["exit_code"] = completed.returncode
        result["passed"] = result["passed"] and completed.returncode == 0
        if not result["passed"]:
            print("Compatibility test failures: " + json.dumps({k: result[k] for k in ["missing", "failed", "unexpected_skips"]}), flush=True)
            # Test output is from a disposable fixture only. Keep detailed logs
            # private by default; --keep-vm allows local diagnosis.
    except Exception as error:
        result["error"] = str(error)
        print("Compatibility run failed: " + str(error), file=sys.stderr, flush=True)
    finally:
        # Once cleanup begins, let it finish before honoring another interrupt.
        with termination_signals(ignore=True):
            if args.keep_vm:
                result["vm_cleanup"] = "retained_by_request"
                print("Retained private VM workspace: " + str(work), flush=True)
                print("Cleanup: python3 scripts/podman-compat.py --cleanup " + shlex.quote(str(work)), flush=True)
            else:
                try:
                    if process is not None:
                        state_path = work / "vm.json"
                        if state_path.exists():
                            stop_owned_vm(json.loads(state_path.read_text()))
                        elif process.poll() is None:
                            process.terminate()
                        process.wait(timeout=20)
                    shutil.rmtree(work)
                    result["vm_cleanup"] = "completed"
                except Exception as error:
                    result["passed"] = False
                    result["vm_cleanup"] = "failed"
                    result["cleanup_error"] = str(error)
                    print("VM cleanup failed; private workspace retained at " + str(work), file=sys.stderr, flush=True)
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    return 0 if result["passed"] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cleanup", type=Path, help="Stop and remove a retained private VM workspace")
    parser.add_argument("--target", choices=TARGETS)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--test-binary", type=Path)
    parser.add_argument("--go-dir", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--image", type=Path, help="Already downloaded image; the committed digest is still required")
    parser.add_argument("--test", action="append", help="Expected top-level Go test; repeat for each required test")
    parser.add_argument("--allow-skip", action="append", default=[], help="Explicit qualified test name allowed to skip")
    parser.add_argument("--memory", type=int, default=4096)
    parser.add_argument("--cpus", type=int, default=4)
    parser.add_argument("--boot-timeout", type=int, default=1200)
    parser.add_argument("--command-timeout", type=int, default=1800)
    parser.add_argument("--build-timeout", type=int, default=1800)
    parser.add_argument("--test-timeout", default="25m")
    parser.add_argument("--keep-vm", action="store_true", help="Retain private VM/disks/keys for diagnosis; clean up explicitly afterwards")
    args = parser.parse_args()
    if args.cleanup:
        cleanup_directory(args.cleanup)
        return 0
    if not all([args.target, args.test_binary, args.go_dir, args.output]):
        parser.error("--target, --test-binary, --go-dir and --output are required for a run")
    with termination_signals():
        return run_vm(args)


if __name__ == "__main__":
    sys.exit(main())
