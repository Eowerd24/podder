#!/usr/bin/env python3
"""Boundary tests for the disposable VM runner; these never start an engine."""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("podman_compat", Path(__file__).with_name("podman-compat.py"))
compat = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(compat)


def events(*values):
    return "\n".join(json.dumps(value) for value in values)


class CompatibilityBoundaries(unittest.TestCase):
    def test_default_runtime_fallback_is_not_reported_as_modern_coverage(self):
        version = {"Client": {"Version": "5.4.2"}}
        info = {"host": {"security": {"rootless": True}, "cgroupManager": "systemd", "cgroupVersion": "v2", "rootlessNetworkCmd": "pasta"},
                "store": {"graphDriverName": "overlay"}}
        compat.validate_engine_environment(version, info, compat.TARGETS["debian13"])
        for section, key, replacement in [("host", "cgroupManager", "cgroupfs"), ("host", "rootlessNetworkCmd", "slirp4netns"),
                                          ("store", "graphDriverName", "vfs")]:
            changed = json.loads(json.dumps(info))
            changed[section][key] = replacement
            with self.assertRaises(RuntimeError):
                compat.validate_engine_environment(version, changed, compat.TARGETS["debian13"])

    def test_image_checksum_rejects_changed_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            image = Path(directory) / "image"
            image.write_bytes(b"official fixture")
            target = {"algorithm": "sha256", "digest": compat.sha(image)}
            compat.verify_image(image, target)
            image.write_bytes(b"modified fixture")
            with self.assertRaisesRegex(RuntimeError, "digest mismatch"):
                compat.verify_image(image, target)

    def test_empty_or_missing_test_execution_is_not_success(self):
        with self.assertRaisesRegex(RuntimeError, "No test2json"):
            compat.test_results("", ["TestRequired"])
        result = compat.test_results(events({"Action": "pass"}), ["TestRequired"])
        self.assertFalse(result["passed"])
        self.assertEqual(result["missing"], ["TestRequired"])

    def test_selected_skip_or_child_skip_fails_coverage(self):
        for name in ["TestRequired", "TestRequired/subcase"]:
            output = events({"Action": "skip", "Test": name}, {"Action": "pass", "Test": "TestRequired"})
            result = compat.test_results(output, ["TestRequired"])
            self.assertFalse(result["passed"])
            self.assertIn(name, result["unexpected_skips"])

    def test_explicit_skip_exception_does_not_hide_another_skip(self):
        output = events({"Action": "skip", "Test": "TestRequired/known"},
                        {"Action": "skip", "Test": "TestRequired/unknown"},
                        {"Action": "pass", "Test": "TestRequired"})
        result = compat.test_results(output, ["TestRequired"], ["TestRequired/known"])
        self.assertFalse(result["passed"])
        self.assertEqual(result["unexpected_skips"], ["TestRequired/unknown"])

    def test_selected_top_level_skip_cannot_be_excused(self):
        with self.assertRaisesRegex(RuntimeError, "selected top-level tests must pass"):
            compat.test_results(events({"Action": "skip", "Test": "TestRequired"}),
                                ["TestRequired"], ["TestRequired"])
        with self.assertRaisesRegex(RuntimeError, "qualified child"):
            compat.test_results(events({"Action": "pass", "Test": "TestRequired"}),
                                ["TestRequired"], ["TestOther/child"])

    def test_package_failure_cannot_be_hidden_by_passing_tests(self):
        output = events({"Action": "pass", "Test": "TestRequired"}, {"Action": "fail"})
        self.assertFalse(compat.test_results(output, ["TestRequired"])["passed"])

    def test_truncated_stream_or_later_pass_cannot_hide_a_failure(self):
        output = events({"Action": "pass", "Test": "TestRequired"})
        self.assertFalse(compat.test_results(output, ["TestRequired"])["passed"])
        output = events({"Action": "fail", "Test": "TestRequired/subcase"},
                        {"Action": "pass", "Test": "TestRequired/subcase"},
                        {"Action": "pass", "Test": "TestRequired"}, {"Action": "pass"})
        result = compat.test_results(output, ["TestRequired"])
        self.assertFalse(result["passed"])
        self.assertEqual(result["failed"], ["TestRequired/subcase"])

    def test_all_required_tests_must_pass(self):
        output = events({"Action": "pass", "Test": "TestOne/subcase"},
                        {"Action": "pass", "Test": "TestOne"},
                        {"Action": "pass", "Test": "TestTwo"}, {"Action": "pass"})
        result = compat.test_results(output, ["TestOne", "TestTwo"])
        self.assertTrue(result["passed"])
        self.assertEqual(result["subtests_passed"], 1)

    def test_invalid_event_stream_is_rejected(self):
        with self.assertRaises(json.JSONDecodeError):
            compat.test_results("not JSON", ["TestRequired"])
        with self.assertRaisesRegex(RuntimeError, "Malformed"):
            compat.test_results('{"Output":"missing action"}', ["TestRequired"])

    def test_ssh_uses_only_pinned_ephemeral_identity_and_host_key(self):
        with mock.patch.object(subprocess, "run") as invoke:
            compat.ssh(Path("/tmp/podder-compat-fixture"), 12345, "true")
        args = invoke.call_args.args[0]
        self.assertIn("StrictHostKeyChecking=yes", args)
        self.assertIn("UserKnownHostsFile=/tmp/podder-compat-fixture/known_hosts", args)
        self.assertIn("GlobalKnownHostsFile=/dev/null", args)
        self.assertIn("IdentitiesOnly=yes", args)
        self.assertEqual(args[1:3], ["-F", "/dev/null"])
        self.assertIn("poddercompat@127.0.0.1", args)
        self.assertNotIn("StrictHostKeyChecking=no", args)

    def test_cloud_subids_have_actual_newlines_and_no_password_login(self):
        config = json.loads(compat.cloud_config("debian13", "ssh-ed25519 fixture client", "PRIVATE\n",
                                                "ssh-ed25519 fixture host", "a" * 32).split("\n", 1)[1])
        self.assertFalse(config["ssh_pwauth"])
        self.assertTrue(config["disable_root"])
        self.assertTrue(config["users"][0]["lock_passwd"])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ["subuid", "subgid"]:
                (root / name).touch()
            subids = next(command for command in config["runcmd"] if command[0] == "bash" and "/etc/subuid" in command[2])
            command = subids[2].replace("/etc/subuid", str(root / "subuid")).replace("/etc/subgid", str(root / "subgid"))
            subprocess.run(["bash", "-ceu", command], check=True)
            for name in ["subuid", "subgid"]:
                self.assertEqual((root / name).read_bytes(), b"poddercompat:100000:65536\n")

    def test_hostname_change_is_deferred_until_services_are_running(self):
        config = json.loads(compat.cloud_config("fedora45", "ssh-ed25519 fixture client", "PRIVATE\n",
                                                "ssh-ed25519 fixture host", "a" * 32).split("\n", 1)[1])
        self.assertTrue(config["preserve_hostname"])
        self.assertNotIn("hostname", config)
        self.assertNotIn("fqdn", config)
        self.assertNotIn("local-hostname", compat.cloud_metadata("a" * 32))
        late_hostname = [command for command in config["runcmd"] if command[0] == "hostnamectl"]
        self.assertEqual(late_hostname, [["hostnamectl", "set-hostname", "podder-compat-" + "a" * 12]])

    def test_source_snapshot_includes_new_tests_and_excludes_ignored_secrets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "repo"
            repo.mkdir()
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            (repo / ".gitignore").write_text(".env\n*.qcow2\n")
            (repo / "deleted.go").write_text("package main\n")
            subprocess.run(["git", "-C", str(repo), "add", "deleted.go"], check=True)
            (repo / "deleted.go").unlink()
            (repo / "new_test.go").write_text("package main\n")
            (repo / ".env").write_text("PRIVATE=do-not-transfer\n")
            (repo / "guest.qcow2").write_bytes(b"not source")
            compat.source_archive(repo, root / "source.tar.gz")
            with tarfile.open(root / "source.tar.gz") as archive:
                names = archive.getnames()
            self.assertIn("source/new_test.go", names)
            self.assertNotIn("source/.env", names)
            self.assertNotIn("source/guest.qcow2", names)
            self.assertNotIn("source/deleted.go", names)
            self.assertFalse(any(".git/" in name for name in names))

    def test_binary_and_source_are_snapshotted_before_provisioning(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo, go_dir = root / "repo", root / "go"
            repo.mkdir()
            (go_dir / "bin").mkdir(parents=True)
            go = go_dir / "bin/go"
            go.write_text('#!/bin/sh\nprintf "go version go1.26.11 linux/amd64\\n"\n')
            go.chmod(0o755)
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            subprocess.run(["git", "-C", str(repo), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                            "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "fixture"], check=True)
            (repo / "new_test.go").write_text("package main\n")
            binary = root / "fixture.test"
            binary.write_bytes(b"compiled original fixture")
            original_digest = hashlib.sha256(binary.read_bytes()).hexdigest()
            args = argparse.Namespace(repo=repo, output=root / "result.json", target="debian13", test=["TestRequired"],
                                      test_binary=binary, go_dir=go_dir, image=None, keep_vm=False, allow_skip=[])
            original_exists = Path.exists

            def exists(path):
                return str(path) == "/dev/kvm" or original_exists(path)

            def provisioning_boundary(destination, target):
                work = destination.parent
                self.assertEqual((work / "podder.test").read_bytes(), b"compiled original fixture")
                self.assertTrue((work / "source.tar.gz").is_file())
                self.assertTrue((work / "go.tar.gz").is_file())
                binary.write_bytes(b"subsequent repair build")
                self.assertEqual(compat.sha(work / "podder.test"), original_digest)
                raise RuntimeError("controlled stop before provisioning")

            with mock.patch.object(Path, "exists", exists), mock.patch.object(compat.os, "access", return_value=True), \
                    mock.patch.object(compat.shutil, "which", return_value="/usr/bin/fixture"), \
                    mock.patch.object(compat, "download_image", side_effect=provisioning_boundary), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(compat.run_vm(args), 1)
            result = json.loads((root / "result.json").read_text())
            self.assertEqual(result["test_binary_sha256"], original_digest)
            self.assertEqual(result["vm_cleanup"], "completed")

    def test_changed_process_identity_is_never_signalled(self):
        state = {"pid": 12345, "identity": {"start_ticks": "original"}}
        with mock.patch.object(compat, "proc_identity", return_value={"start_ticks": "reused"}), mock.patch.object(compat.os, "kill") as kill:
            with self.assertRaisesRegex(RuntimeError, "identity changed"):
                compat.stop_owned_vm(state)
            kill.assert_not_called()

    def test_hidden_process_in_another_pid_namespace_is_not_assumed_stopped(self):
        state = {"pid": 12345, "identity": {}, "pid_namespace": "pid:[launch]"}
        with mock.patch.object(compat, "pid_namespace", return_value="pid:[sandbox]"), \
                mock.patch.object(compat, "proc_identity") as identity, mock.patch.object(compat.os, "kill") as kill:
            with self.assertRaisesRegex(RuntimeError, "another PID namespace"):
                compat.stop_owned_vm(state)
            identity.assert_not_called()
            kill.assert_not_called()

    def test_legacy_record_does_not_hide_an_invisible_vm(self):
        with mock.patch.object(compat, "proc_identity", return_value=None), mock.patch.object(compat.os, "kill") as kill:
            with self.assertRaisesRegex(RuntimeError, "cannot prove it stopped"):
                compat.stop_owned_vm({"pid": 12345, "identity": {}})
            kill.assert_not_called()

    def test_stopped_vm_in_recorded_pid_namespace_needs_no_signal(self):
        state = {"pid": 12345, "identity": {}, "pid_namespace": "pid:[launch]"}
        with mock.patch.object(compat, "pid_namespace", return_value="pid:[launch]"), \
                mock.patch.object(compat, "proc_identity", return_value=None), mock.patch.object(compat.os, "kill") as kill:
            compat.stop_owned_vm(state)
            kill.assert_not_called()

    @unittest.skipUnless(os.name == "posix", "POSIX termination cleanup test")
    def test_sigterm_reaches_finally_instead_of_leaving_resources(self):
        code = '''import importlib.util, pathlib, sys, time
spec = importlib.util.spec_from_file_location("compat", sys.argv[1])
compat = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compat)
with compat.termination_signals():
    try:
        pathlib.Path(sys.argv[2]).write_text("ready")
        time.sleep(30)
    finally:
        pathlib.Path(sys.argv[3]).write_text("cleaned")
'''
        with tempfile.TemporaryDirectory() as directory:
            ready, cleaned = Path(directory) / "ready", Path(directory) / "cleaned"
            child = subprocess.Popen([sys.executable, "-c", code, str(Path(compat.__file__).resolve()), str(ready), str(cleaned)],
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline and not ready.exists():
                    time.sleep(0.01)
                self.assertTrue(ready.exists())
                child.terminate()
                child.wait(timeout=5)
                self.assertEqual(cleaned.read_text(), "cleaned")
            finally:
                if child.poll() is None:
                    child.kill()
                child.wait(timeout=5)

    @unittest.skipUnless(Path("/proc/self/stat").exists(), "Linux process identity test")
    def test_finished_child_is_not_mistaken_for_an_owned_running_vm(self):
        child = subprocess.Popen([sys.executable, "-c", "pass"])
        try:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                state = Path("/proc", str(child.pid), "stat").read_text().rpartition(") ")[2].split()[0]
                if state == "Z":
                    break
                time.sleep(0.01)
            self.assertEqual(state, "Z")
            self.assertIsNone(compat.proc_identity(child.pid))
        finally:
            child.wait(timeout=5)

    def test_owned_process_is_stopped_without_signalling_replacement(self):
        identity = {"start_ticks": "original"}
        state = {"pid": 12345, "identity": identity}
        with mock.patch.object(compat, "proc_identity", side_effect=[identity, {"start_ticks": "replacement"}]), mock.patch.object(compat.os, "kill") as kill:
            compat.stop_owned_vm(state)
            kill.assert_called_once_with(12345, compat.signal.SIGTERM)

    def test_lingering_owned_process_after_sigkill_keeps_disk_and_keys(self):
        with tempfile.TemporaryDirectory(prefix="podder-compat-") as directory:
            work = Path(directory)
            work.chmod(0o700)
            identity = {"start_ticks": "original"}
            state = {"pid": 12345, "identity": identity, "pid_namespace": "pid:[launch]"}
            (work / "vm.json").write_text(json.dumps(state))
            (work / "guest.qcow2").write_bytes(b"owned disk")
            (work / "client_ed25519").write_bytes(b"owned temporary credential")
            with mock.patch.object(compat, "pid_namespace", return_value="pid:[launch]"), \
                    mock.patch.object(compat, "proc_identity", return_value=identity), \
                    mock.patch.object(compat.time, "monotonic", side_effect=[0, 16, 16, 22]), \
                    mock.patch.object(compat.os, "kill") as kill:
                with self.assertRaisesRegex(RuntimeError, "remains alive after SIGKILL"):
                    compat.cleanup_directory(work)
                self.assertEqual(kill.call_args_list, [mock.call(12345, compat.signal.SIGTERM), mock.call(12345, compat.signal.SIGKILL)])
            self.assertTrue((work / "guest.qcow2").is_file())
            self.assertTrue((work / "client_ed25519").is_file())

    def test_sigkill_cleanup_waits_for_owned_process_to_disappear(self):
        identity = {"start_ticks": "original"}
        state = {"pid": 12345, "identity": identity, "pid_namespace": "pid:[launch]"}
        with mock.patch.object(compat, "pid_namespace", return_value="pid:[launch]"), \
                mock.patch.object(compat, "proc_identity", side_effect=[identity, identity, None]), \
                mock.patch.object(compat.time, "monotonic", side_effect=[0, 16, 16, 17]), \
                mock.patch.object(compat.os, "kill") as kill:
            compat.stop_owned_vm(state)
            self.assertEqual(kill.call_args_list, [mock.call(12345, compat.signal.SIGTERM), mock.call(12345, compat.signal.SIGKILL)])

    def test_cleanup_refuses_unrelated_directory(self):
        with tempfile.TemporaryDirectory(prefix="unrelated-") as directory:
            with self.assertRaisesRegex(RuntimeError, "Not a compatibility"):
                compat.cleanup_directory(Path(directory))
            self.assertTrue(Path(directory).is_dir())


if __name__ == "__main__":
    unittest.main()
