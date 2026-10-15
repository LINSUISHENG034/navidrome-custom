import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "native-playback-validation.py"
spec = importlib.util.spec_from_file_location("native_playback_validation", SCRIPT)
validation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validation)


class FakeProcess:
    def __init__(self, *, fail_first_playback=False, fail_transport=False):
        self.calls = []
        self.fail_first_playback = fail_first_playback
        self.fail_transport = fail_transport

    def __call__(self, argv, **kwargs):
        self.calls.append((list(argv), kwargs))
        executable = Path(argv[0]).name
        if executable == "go" and "env" in argv:
            version = "go1.27.1" if "toolchain@" in argv[0] else "go1.25.0"
            return subprocess.CompletedProcess(
                argv, 0, f"{version}\n/tmp/gomodcache\nlinux\namd64\n", ""
            )
        if executable == "go" and "test" in argv:
            if "^TestPlaybackServerRunWaitsForPendingOutputRetirement$" in argv:
                return subprocess.CompletedProcess(argv, 0,
                    "--- PASS: TestPlaybackServerRunWaitsForPendingOutputRetirement (0.01s)\n", "")
            if "^TestJukeboxSessionCommandAuthorityValidation$" in argv:
                return subprocess.CompletedProcess(argv, 0,
                    "=== RUN   TestJukeboxSessionCommandAuthorityValidation\n"
                    "--- PASS: TestJukeboxSessionCommandAuthorityValidation (0.01s)\n"
                    "    --- PASS: TestJukeboxSessionCommandAuthorityValidation/non-owner_cannot_mutate_after_heartbeat_denial (0.00s)\n"
                    "    --- PASS: TestJukeboxSessionCommandAuthorityValidation/owner_command_targets_session-bound_non-default_device (0.00s)\n", "")
            if any("rejects non-owner mutation after session heartbeat denial" in arg for arg in argv):
                return subprocess.CompletedProcess(argv, 0,
                    "• [It] rejects non-owner mutation after session heartbeat denial\n"
                    "Ran 1 of 359 Specs in 1s\n", "")
            focus = next((value.removeprefix("-ginkgo.focus=") for value in argv
                          if value.startswith("-ginkgo.focus=")), "")
            for phase_id, spec_name in validation.NULL_MPV_SPECS.items():
                if spec_name in focus:
                    code = int(self.fail_transport and phase_id == "mpv-null-transport-recovery")
                    output = "• [It] MPV null output prototype " + spec_name
                    summary = "Ran 1 of 5 Specs in 1s\n"
                    error = "synthetic regression failure" if code else ""
                    return subprocess.CompletedProcess(argv, code, output + "\n" + summary, error)
            if self.fail_first_playback and "./core/playback/..." in argv and "-race" not in argv:
                self.fail_first_playback = False
                return subprocess.CompletedProcess(argv, 1, "synthetic playback failure", "fixture stderr")
            return subprocess.CompletedProcess(argv, 0, "ok", "")
        if executable == "git":
            if "rev-parse" in argv:
                return subprocess.CompletedProcess(argv, 0, "a" * 40 + "\n", "")
            if "ls-files" in argv and "-z" in argv:
                return subprocess.CompletedProcess(argv, 0, "go.mod\0", "")
            if "status" in argv:
                return subprocess.CompletedProcess(
                    argv, 0,
                    " M fork/docs/plans/remote-playback-wayfinder/issues/07-playback-route-selection.md\n"
                    "?? fixture-untracked/\n",
                    "",
                )
            return subprocess.CompletedProcess(argv, 0, "", "")
        return subprocess.CompletedProcess(argv, 0, "fixture ok", "")


class ValidationRunnerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "go.mod").write_text("module fixture\ngo 1.25.0\n", encoding="utf-8")
        self.go = self.root / "toolchain/bin/go"
        self.go.parent.mkdir(parents=True)
        self.go.write_text("fixture executable", encoding="utf-8")
        self.go.chmod(0o755)
        gofmt = self.go.with_name("gofmt")
        gofmt.write_text("fixture executable", encoding="utf-8")
        gofmt.chmod(0o755)
        self.output = self.root / "tmp/native-playback-validation"
        self.source_commit = "a" * 40
        self.process = FakeProcess()
        self.which = lambda name, path=None: f"/fake/{name}" if name in {"git", "npm", "mpv"} else None

    def make_runner(self, *, mpv_null=False, process=None):
        return validation.ValidationRunner(
            self.root,
            self.output,
            go_binary=str(self.go),
            go_details={"selected": {"version": "go1.25.0"}},
            timeout=5,
            phase_timeouts={},
            stress_count=3,
            mpv_null=mpv_null,
            run=process or self.process,
            source_commit=self.source_commit,
            provenance={"command": {"exit_code": 0}},
        )

    def run_with_fake_tools(self, runner):
        with patch.object(validation.shutil, "which", side_effect=self.which):
            report = runner.run_all()
        return report

    def test_paused_seek_candidate_is_an_additional_executed_phase(self):
        report = self.run_with_fake_tools(self.make_runner(mpv_null=True))
        phases = {phase["id"]: phase for phase in report["phases"]}
        candidate = phases["mpv-null-paused-seek-candidate"]
        self.assertEqual(candidate["status"], "pass")
        self.assertIn(
            "-ginkgo.focus=restores a stable paused position after buffered playback through track commands",
            candidate["commands"][0]["command"],
        )
        self.assertEqual(phases["mpv-null-lifecycle"]["status"], "pass")
        self.assertIn(
            "-ginkgo.focus=observes paused output without advancing and distinguishes file lifecycle events",
            phases["mpv-null-lifecycle"]["commands"][0]["command"],
        )
        self.assertFalse(phases["mpv-null-lifecycle"].get("expected_failure", False))

    def test_raw_paused_seek_failure_is_not_hidden_by_candidate_success(self):
        def raw_failure(argv, **kwargs):
            result = self.process(argv, **kwargs)
            if "-ginkgo.focus=observes paused output without advancing and distinguishes file lifecycle events" in argv:
                return subprocess.CompletedProcess(argv, 1, result.stdout, "raw paused seek remains unstable")
            return result

        report = self.run_with_fake_tools(self.make_runner(mpv_null=True, process=raw_failure))
        phases = {phase["id"]: phase for phase in report["phases"]}
        self.assertEqual(phases["mpv-null-lifecycle"]["status"], "fail")
        self.assertEqual(phases["mpv-null-paused-seek-candidate"]["status"], "pass")
        self.assertEqual(report["gate"]["decision"], "not_accepted")

    def test_collects_all_phases_after_a_failure_and_preserves_output(self):
        process = FakeProcess(fail_first_playback=True)
        runner = self.make_runner(process=process)
        report = self.run_with_fake_tools(runner)

        phases = {phase["id"]: phase for phase in report["phases"]}
        self.assertEqual(phases["playback-normal"]["status"], "fail")
        self.assertEqual(phases["playback-race"]["status"], "pass")
        self.assertEqual(phases["ui-audioplayer"]["status"], "pass")
        self.assertEqual(phases["ui-full-suite"]["status"], "pass")
        self.assertEqual(phases["ui-full-suite"]["commands"][0]["command"][-2:], ["--", "--run"])
        self.assertEqual(phases["ui-eventstream"]["status"], "pass")
        self.assertEqual(
            {command["command"][-1] for command in phases["ui-eventstream"]["commands"]},
            {"src/eventStream.test.js", "src/common/useRefreshOnEvents.test.js"},
        )
        self.assertEqual(phases["physical-output-hardware"]["status"], "not_run")
        stress = phases["playback-stress"]["commands"]
        self.assertEqual(len(stress), 3)
        self.assertTrue(all("-count=1" in command["command"] for command in stress))
        self.assertFalse(any("-count=3" in command["command"] for command in stress))
        self.assertEqual(report["gate"]["decision"], "not_accepted")
        command = phases["playback-normal"]["commands"][0]
        self.assertEqual(command["exit_code"], 1)
        self.assertEqual(command["stdout"], "synthetic playback failure")
        self.assertEqual(command["stderr"], "fixture stderr")
        self.assertTrue(Path(command["stdout_log"]).is_file())
        self.assertEqual(command["cwd"], str(self.root))
        self.assertEqual(phases["playback-normal"]["source_commit"], self.source_commit)
        self.assertEqual(process.calls[0][1]["env"]["ND_ENABLEINSIGHTSCOLLECTOR"], "false")
        self.assertEqual(process.calls[0][1]["env"]["ND_JUKEBOX_AUTODISCOVERBLUETOOTH"], "false")

    def test_all_real_null_specs_execute_and_transport_failure_is_critical(self):
        process = FakeProcess(fail_transport=True)
        runner = self.make_runner(mpv_null=True, process=process)
        report = self.run_with_fake_tools(runner)

        phases = {phase["id"]: phase for phase in report["phases"]}
        null_phases = [phase_id for phase_id in validation.NULL_MPV_SPECS]
        self.assertEqual(set(null_phases), {
            "mpv-null-lifecycle", "mpv-null-paused-seek-candidate",
            "mpv-null-transport-recovery", "mpv-null-natural-eof",
            "mpv-null-explicit-close", "mpv-null-context-cancel",
        })
        self.assertTrue(all(phases[phase_id]["status"] == "pass" for phase_id in null_phases
                            if phase_id != "mpv-null-transport-recovery"))
        self.assertEqual(phases["mpv-null-transport-recovery"]["status"], "fail")
        self.assertIn("mpv-null-transport-recovery", report["gate"]["critical_failures"])
        self.assertEqual(report["gate"]["decision"], "not_accepted")
        null_commands = [call for call in process.calls if "./core/playback/mpv" in call[0]
                         and any(arg.startswith("-ginkgo.focus=") for arg in call[0])]
        self.assertEqual(len(null_commands), len(null_phases))
        for command, kwargs in null_commands:
            self.assertEqual(kwargs["env"]["ND_TEST_MPV_NULL_OUTPUT"], "1")
            self.assertEqual(kwargs["env"]["ND_ENABLEINSIGHTSCOLLECTOR"], "false")
            self.assertEqual(kwargs["env"]["ND_JUKEBOX_AUTODISCOVERBLUETOOTH"], "false")
            self.assertLess(command.index("-v"), command.index("./core/playback/mpv"))

    def test_command_authority_contracts_are_explicit_and_critical(self):
        runner = self.make_runner()
        report = self.run_with_fake_tools(runner)
        phases = {phase["id"]: phase for phase in report["phases"]}
        for phase_id in ("nativeapi-command-authority", "nativeapi-subsonic-command-authority"):
            self.assertEqual(phases[phase_id]["status"], "pass")
            self.assertTrue(phases[phase_id]["critical"])
        commands = [call for call in self.process.calls
                    if call[1]["env"].get("ND_TEST_SESSION_COMMAND_AUTHORITY") == "1"]
        self.assertEqual(len(commands), 2)
        self.assertTrue(all(call[1]["env"]["GOTOOLCHAIN"] == "local" for call in commands))

    def test_pending_service_retirement_is_explicit_and_critical(self):
        def pending_retirement(argv, **kwargs):
            if kwargs["env"].get("ND_TEST_PENDING_SERVICE_RETIREMENT") == "1":
                self.assertIn("-race", argv)
                return subprocess.CompletedProcess(argv, 1, "Run returned with unretired preparing output", "")
            return self.process(argv, **kwargs)

        report = self.run_with_fake_tools(self.make_runner(process=pending_retirement))
        self.assertIn("playback-pending-service-retirement", report["gate"]["critical_failures"])
        phase = next(phase for phase in report["phases"] if phase["id"] == "playback-pending-service-retirement")
        self.assertEqual(phase["status"], "fail")
        self.assertTrue(phase["critical"])

    def test_command_authority_failures_are_not_waived(self):
        def denied_contract(argv, **kwargs):
            if kwargs["env"].get("ND_TEST_SESSION_COMMAND_AUTHORITY") == "1":
                return subprocess.CompletedProcess(argv, 1, "contract RED: mutation accepted", "")
            return self.process(argv, **kwargs)

        report = self.run_with_fake_tools(self.make_runner(process=denied_contract))
        self.assertIn("nativeapi-command-authority", report["gate"]["critical_failures"])
        self.assertIn("nativeapi-subsonic-command-authority", report["gate"]["critical_failures"])
        self.assertEqual(report["gate"]["decision"], "not_accepted")

    def test_native_contract_missing_subtest_is_not_pass(self):
        status, reason = validation.go_execution_check(
            "--- PASS: TestContract (0.01s)\n"
            "    --- SKIP: TestContract/required_behavior (0.00s)\n",
            "TestContract", ("required_behavior",),
        )
        self.assertEqual(status, "blocked")
        self.assertIn("required_behavior", reason)

    def test_native_contract_default_skip_is_not_pass(self):
        status, reason = validation.go_execution_check(
            "=== RUN   TestJukeboxSessionCommandAuthorityValidation\n"
            "--- SKIP: TestJukeboxSessionCommandAuthorityValidation (0.00s)\n",
            "TestJukeboxSessionCommandAuthorityValidation",
        )
        self.assertEqual(status, "blocked")
        self.assertIn("not executed", reason)

    def test_transport_failure_is_not_reclassified_as_expected_failure(self):
        runner = self.make_runner(process=FakeProcess(fail_transport=True))
        runner.run_directory = validation.create_run_directory(self.output)
        expected = validation.NULL_MPV_SPECS["mpv-null-transport-recovery"]
        phase = runner._phase(
            "mpv-null-transport-recovery", "transport regression",
            commands=[["go", "test", "-ginkgo.focus=" + expected]],
            output_check=lambda output: validation.ginkgo_execution_check(output, expected),
        )
        self.assertEqual(phase["status"], "fail")
        self.assertNotEqual(phase["status"], "expectedfail")

    def test_default_null_mpv_skips_are_not_reported_as_pass(self):
        runner = self.make_runner(mpv_null=False)
        report = self.run_with_fake_tools(runner)
        phases = {phase["id"]: phase for phase in report["phases"]}
        self.assertEqual({phase_id: phases[phase_id]["status"] for phase_id in validation.NULL_MPV_SPECS},
                         {phase_id: "not_run" for phase_id in validation.NULL_MPV_SPECS})
        self.assertEqual(report["gate"]["decision"], "not_accepted")

    def test_offline_dependency_failure_is_blocked_not_failed(self):
        def unavailable_dependency(argv, **_kwargs):
            return subprocess.CompletedProcess(argv, 1, "", "module lookup disabled by GOPROXY=off")

        runner = self.make_runner(process=unavailable_dependency)
        runner.run_directory = validation.create_run_directory(self.output)
        phase = runner._phase(
            "playback-normal", "offline dependencies", commands=[["go", "test"]]
        )
        self.assertEqual(phase["status"], "blocked")
        self.assertIn("required local build/runtime dependency", phase["reason"])

    def test_ginkgo_skips_and_zero_execution_are_blocked(self):
        spec_name = validation.NULL_MPV_SPECS["mpv-null-lifecycle"]
        skipped = f"S [SKIPPED] {spec_name}\nRan 1 of 1 Specs in 1s"
        no_runs = f"• [It] {spec_name}\nRan 0 of 1 Specs in 0s"
        self.assertEqual(validation.ginkgo_execution_check(skipped, spec_name)[0], "blocked")
        self.assertEqual(validation.ginkgo_execution_check(no_runs, spec_name)[0], "blocked")

    def test_missing_executable_is_blocked_and_timeout_is_failure(self):
        runner = self.make_runner()
        runner.run_directory = validation.create_run_directory(self.output)

        def missing_process(*_args, **_kwargs):
            raise FileNotFoundError("synthetic executable missing")

        runner.run = missing_process
        missing = validation.ValidationRunner._phase(
            runner, "missing", "missing executable", commands=[["does-not-exist"]]
        )
        self.assertEqual(missing["status"], "blocked")

        def timeout(*_args, **_kwargs):
            raise subprocess.TimeoutExpired(["slow"], 1, output="partial", stderr="waiting")

        timed_out_runner = self.make_runner(process=timeout)
        timed_out_runner.run_directory = validation.create_run_directory(self.output)
        timed_out = validation.ValidationRunner._phase(
            timed_out_runner, "slow", "timed out", commands=[["slow"]], timeout=1
        )
        self.assertEqual(timed_out["status"], "fail")
        self.assertTrue(timed_out["commands"][0]["timed_out"])
        self.assertEqual(timed_out["commands"][0]["stdout"], "partial")

    def test_discovers_cached_toolchain_from_go_environment_metadata(self):
        gomodcache = self.root / "gomodcache"
        cached = gomodcache / "golang.org/toolchain@v0.0.1-go1.27.1.linux-amd64/bin/go"
        cached.parent.mkdir(parents=True)
        cached.write_text("fixture executable", encoding="utf-8")
        cached.chmod(0o755)

        calls = []

        def discover_process(argv, **kwargs):
            calls.append((argv, kwargs))
            if "toolchain@" in argv[0]:
                version = "go1.27.1"
            else:
                version = "go1.25.0"
            return subprocess.CompletedProcess(
                argv, 0, f"{version}\n{gomodcache}\nlinux\namd64\n", ""
            )

        with patch.object(validation.shutil, "which", return_value=str(self.go)):
            selected, details = validation.discover_go(self.root, discover_process)
        self.assertEqual(selected, str(cached))
        self.assertEqual(details["selected"]["version"], "go1.27.1")
        self.assertTrue(calls)
        self.assertTrue(all(call[1]["env"]["GOTOOLCHAIN"] == "local" for call in calls))
        self.assertTrue(all(call[1]["env"]["GOPROXY"] == "off" for call in calls))

    def test_cli_writes_report_and_returns_unaccepted_without_hardware(self):
        output = io.StringIO()
        errors = io.StringIO()
        env = {key: value for key, value in os.environ.items() if key in {"PATH", "HOME"}}
        with patch.dict(os.environ, env, clear=True):
            with patch.object(validation.shutil, "which", side_effect=self.which):
                status = validation.main(
                    ["--repo", str(self.root), "--output", str(self.output), "--go", str(self.go),
                     "--timeout", "5", "--stress-count", "2"],
                    run=self.process,
                    stdout=output,
                    stderr=errors,
                )
        self.assertEqual(status, 1)
        summary = json.loads(output.getvalue())
        report_path = Path(summary["report"])
        self.assertTrue(report_path.is_file())
        report = json.loads(report_path.read_text(encoding="utf-8"))
        self.assertEqual(report["source_commit"], self.source_commit)
        dirty_scope = report["dirty_scope_fingerprint"]
        status_text = (
            " M fork/docs/plans/remote-playback-wayfinder/issues/07-playback-route-selection.md\n"
            "?? fixture-untracked/\n"
        )
        self.assertEqual(dirty_scope["sha256"], hashlib.sha256(status_text.encode()).hexdigest())
        self.assertIn("07-playback-route-selection.md", dirty_scope["entries"][0])
        self.assertIn("not a source-content fingerprint", dirty_scope["meaning"])
        content = report["source_content_fingerprint"]
        self.assertIsNone(content["error"])
        self.assertEqual(content["entries"][0]["sha256"], hashlib.sha256(
            (self.root / "go.mod").read_bytes()
        ).hexdigest())
        self.assertEqual(report["gate"]["decision"], "not_accepted")
        self.assertIn("physical-output-hardware", report["gate"]["critical_not_run"])
        self.assertEqual(errors.getvalue(), "")

    def test_source_content_fingerprint_changes_without_status_change(self):
        with patch.object(validation.shutil, "which", side_effect=self.which):
            before = validation.capture_source_content_fingerprint(self.root, self.process)
            (self.root / "go.mod").write_text("module fixture\ngo 1.27.0\n", encoding="utf-8")
            after = validation.capture_source_content_fingerprint(self.root, self.process)
        self.assertIsNone(before["error"])
        self.assertIsNone(after["error"])
        self.assertNotEqual(before["sha256"], after["sha256"])
        self.assertEqual(before["entries"][0]["path"], "go.mod")
        self.assertEqual(before["entries"][0]["sha256"], hashlib.sha256(
            b"module fixture\ngo 1.25.0\n"
        ).hexdigest())

    def test_real_git_fingerprint_ignores_private_untracked_files_and_doc_changes(self):
        subprocess.run(["git", "init", "--quiet", str(self.root)], check=True,
                       env=validation.safe_environment(), capture_output=True)
        subprocess.run(["git", "-C", str(self.root), "add", "go.mod"], check=True,
                       env=validation.safe_environment(), capture_output=True)
        private = self.root / ".claude/private.py"
        private.parent.mkdir()
        private.write_text("private content", encoding="utf-8")
        with patch.object(validation.shutil, "which", wraps=validation.shutil.which):
            before = validation.capture_source_content_fingerprint(self.root, validation.isolated_run)
            private.write_text("changed secret", encoding="utf-8")
            docs = self.root / "fork/docs/issues"
            docs.mkdir(parents=True)
            (docs / "fixture.md").write_text("unrelated", encoding="utf-8")
            after = validation.capture_source_content_fingerprint(self.root, validation.isolated_run)
        self.assertIsNone(before["error"])
        self.assertEqual(before["sha256"], after["sha256"])
        self.assertEqual([entry["path"] for entry in before["entries"]], ["go.mod"])

    def test_in_scope_symlink_hashes_link_text_without_reading_target(self):
        target = self.root / "core/fixture"
        target.parent.mkdir()
        target.mkdir()
        (self.root / "go.mod").unlink()
        (self.root / "go.mod").symlink_to("core/fixture")
        with patch.object(validation.shutil, "which", side_effect=self.which):
            result = validation.capture_source_content_fingerprint(self.root, self.process)
        self.assertIsNone(result["error"])
        self.assertEqual(result["entries"][0]["kind"], "symlink")
        self.assertEqual(result["entries"][0]["sha256"], hashlib.sha256(b"core/fixture").hexdigest())

    def test_source_content_fingerprint_does_not_follow_symlinks(self):
        outside = self.root / "private"
        outside.write_text("must not be read", encoding="utf-8")
        (self.root / "go.mod").unlink()
        (self.root / "go.mod").symlink_to(outside)
        with patch.object(validation.shutil, "which", side_effect=self.which):
            result = validation.capture_source_content_fingerprint(self.root, self.process)
        self.assertIsNone(result["sha256"])
        self.assertIn("symlink", result["error"])

    def test_isolated_process_captures_command_output(self):
        result = validation.isolated_run(
            [sys.executable, "-I", "-c", "print('validation fixture')"],
            cwd=str(self.root),
            env=validation.safe_environment(),
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            timeout=5,
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "validation fixture\n")
        self.assertEqual(result.stderr, "")

    def test_go_binary_override_is_used_by_discovery(self):
        def probe(argv, **kwargs):
            self.assertEqual(argv[0], str(self.go))
            self.assertEqual(kwargs["env"]["GOTOOLCHAIN"], "local")
            self.assertEqual(kwargs["env"]["GOPROXY"], "off")
            return subprocess.CompletedProcess(argv, 0, "go1.25.0\n/tmp/cache\nlinux\namd64\n", "")

        selected, details = validation.discover_go(self.root, probe, str(self.go))
        self.assertEqual(selected, str(self.go))
        self.assertIsNone(details["error"])


class AppSmokeEvidenceTests(unittest.TestCase):
    def setUp(self):
        script = SCRIPT.with_name("native-playback-app-smoke.py")
        spec = importlib.util.spec_from_file_location("native_playback_app_smoke", script)
        self.smoke = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.smoke)
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.output = Path(temporary.name)
        with patch.object(self.smoke, "source_fingerprint", return_value={"commit": "fixture"}):
            self.app = self.smoke.AppSmoke(
                self.output, Path("/fake/navidrome"), "/fake/chrome", Path("/fake/mpv"))
        self.addCleanup(self.app.cleanup)

    def test_confirmed_observer_denial_is_classified_without_hiding_raw_console(self):
        details = {
            "playwright": [{"type": "error", "text": "Failed to load resource: the server responded with a status of 403 (Forbidden)",
                            "page": "http://fixture/controller", "location": {"url": "http://fixture/api/jukebox/session/attach"}}],
            "jukebox_http": [{"method": "POST", "status": 403, "page": "http://fixture/controller",
                              "url": "http://fixture/api/jukebox/session/attach",
                              "body": {"sessionId": "s", "clientId": "observer"}}],
        }
        result = self.smoke.classify_browser_console(details, {("s", "observer")})
        self.assertEqual(len(result["expected_http_denials"]), 1)
        self.assertEqual(result["unexpected_errors"], [])
        self.assertEqual(len(details["playwright"]), 1)

    def test_unconfirmed_same_route_denial_prevents_ambiguous_console_classification(self):
        details = {
            "playwright": [{"type": "error", "text": "Failed to load resource: the server responded with a status of 403 (Forbidden)",
                            "page": "http://fixture/controller", "location": {"url": "http://fixture/api/jukebox/session/attach"}}],
            "jukebox_http": [
                {"method": "POST", "status": 403, "page": "http://fixture/controller",
                 "url": "http://fixture/api/jukebox/session/attach", "body": {"sessionId": "s", "clientId": client}}
                for client in ("observer", "owner")
            ],
        }
        result = self.smoke.classify_browser_console(details, {("s", "observer")})
        self.assertEqual(result["expected_http_denials"], [])
        self.assertEqual(len(result["unexpected_errors"]), 1)

    def test_root_and_unsolicited_404_console_errors_remain_unexpected(self):
        details = {"playwright": [
            {"type": "error", "text": "undefined", "page": "http://fixture/", "location": {"url": "http://fixture/app.js"}},
            {"type": "error", "text": "Failed to load resource: the server responded with a status of 404 (Not Found)",
             "page": "http://fixture/controller", "location": {"url": "http://fixture/api/jukebox/session/detach"}},
        ]}
        result = self.smoke.classify_browser_console(details, {("s", "observer")})
        self.assertEqual(result["expected_http_denials"], [])
        self.assertEqual(result["unexpected_errors"], details["playwright"])

    def test_browser_evidence_remains_available_on_outer_setup_failure(self):
        errors, checks, details = self.app.begin_browser_evidence()
        errors.append("preserved browser failure")
        checks["default_app_local_return"] = {"status": "fail"}
        details["jukebox_http"].append({"route": "/api/jukebox/session/detach", "status": 404})
        browser = self.app.summary["browser"]
        self.assertIs(browser["console_errors"], errors)
        self.assertIs(browser["checks"], checks)
        self.assertIs(browser["console_details"], details)
        self.assertEqual(browser["status"], "running")

    def test_commands_include_explicit_current_controller_scope(self):
        with patch.object(self.app, "native", return_value=(200, {})) as native:
            self.app.command("/api/jukebox/play")
            native.assert_called_once_with("POST", "/api/jukebox/play", body={
                "sessionId": self.app.session_id,
                "clientId": self.app.client_id,
            })

    def test_observer_probe_cannot_inherit_owner_identity(self):
        with patch.object(self.app, "native", return_value=(403, {})) as native:
            self.app.command("/api/jukebox/volume", {"gain": 0.25}, client_id="observer")
            native.assert_called_once_with("POST", "/api/jukebox/volume", body={
                "gain": 0.25,
                "sessionId": self.app.session_id,
                "clientId": "observer",
            })

    def test_command_payload_cannot_override_controller_scope(self):
        with patch.object(self.app, "native") as native:
            with self.assertRaises(self.smoke.SmokeFailure):
                self.app.command("/api/jukebox/volume", {"gain": 0.25, "clientId": "forged"})
            native.assert_not_called()

    def test_wait_trace_matches_only_owned_child_exit(self):
        app = self.app
        output = self.output
        app.trace_prefix = output / "process.trace"
        app.mpv_pids = {123}
        (output / "process.trace.123").write_text(
            "1791888057.233 exit_group(0) = ?\n+++ exited with 0 +++\n", encoding="utf-8")
        (output / "process.trace.456").write_text(
            "waitid(P_ALL, 0, {si_pid=123, si_code=CLD_EXITED, si_status=0}, WEXITED) = 0\n"
            "wait4(123, [{WIFEXITED(s)}], 0, NULL) = 123\n"
            "wait4(1234, [{WIFEXITED(s)}], 0, NULL) = 1234\n", encoding="utf-8")
        report = app.process_trace_summary()
        self.assertEqual(len(report["wait_calls"]), 2)
        self.assertTrue(all(row["traced_parent_pid"] == 456 for row in report["wait_calls"]))
        self.assertEqual(report["mpv_processes"][0]["pid"], 123)
        self.assertIn("exited with 0", report["mpv_processes"][0]["lifecycle_events"][1])


if __name__ == "__main__":
    unittest.main()
