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
            if any(arg.startswith("^TestPipeWire(NewTrack|Private)") for arg in argv):
                return subprocess.CompletedProcess(
                    argv, 0,
                    "=== RUN   TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver\n"
                    "--- PASS: TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver (0.01s)\n"
                    "=== RUN   TestPipeWirePrivateOutput\n"
                    "--- PASS: TestPipeWirePrivateOutput (0.01s)\n"
                    "=== RUN   TestPipeWirePrivateSeek\n"
                    "--- PASS: TestPipeWirePrivateSeek (0.01s)\n"
                    "=== RUN   TestPipeWirePrivateEOF\n"
                    "--- PASS: TestPipeWirePrivateEOF (0.01s)\n",
                    "",
                )
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

    def run_cli(self, *args, process=None, which=None):
        output = io.StringIO()
        errors = io.StringIO()
        env = {key: value for key, value in os.environ.items() if key in {"PATH", "HOME"}}
        with patch.dict(os.environ, env, clear=True):
            with patch.object(validation.shutil, "which", side_effect=which or self.which):
                status = validation.main(
                    ["--repo", str(self.root), "--output", str(self.output), "--go", str(self.go),
                     "--timeout", "5", "--stress-count", "2", *args],
                    run=process or self.process,
                    stdout=output,
                    stderr=errors,
                )
        self.assertEqual(errors.getvalue(), "")
        summary = json.loads(output.getvalue())
        report_path = Path(summary["report"])
        self.assertTrue(report_path.is_file())
        return status, summary, json.loads(report_path.read_text(encoding="utf-8"))

    def test_cli_phase_selection_skips_unselected_commands_and_go_discovery(self):
        status, _, report = self.run_cli("--phase", "diff-check", "--phase", "diff-check")
        self.assertEqual(status, 1)
        executed = [phase for phase in report["phases"] if phase["commands"]]
        self.assertEqual([phase["id"] for phase in executed], ["diff-check"])
        self.assertEqual(len(executed[0]["commands"]), 2)
        for phase in report["phases"]:
            if phase["category"] == "software" and phase["id"] != "diff-check":
                self.assertEqual(phase["status"], "not_run")
                self.assertIn("not selected", phase["reason"])
        self.assertTrue(self.process.calls)
        for argv, _ in self.process.calls:
            self.assertEqual(Path(argv[0]).name, "git")
            self.assertNotIn("--name-only", argv)
            if "ls-files" in argv:
                self.assertIn("-z", argv)
        self.assertIn("physical-output-hardware", report["gate"]["critical_not_run"])

    def test_cli_phase_selection_preserves_canonical_order(self):
        _, _, report = self.run_cli("--phase", "diff-check", "--phase", "ui-audioplayer")
        self.assertEqual([phase["id"] for phase in report["phases"] if phase["commands"]],
                         ["ui-audioplayer", "diff-check"])
        self.assertFalse(any(Path(argv[0]).name == "go" for argv, _ in self.process.calls))

    def test_cli_selected_go_formatting_still_discovers_and_checks_sources(self):
        _, _, report = self.run_cli("--phase", "go-formatting")
        executed = [phase for phase in report["phases"] if phase["commands"]]
        self.assertEqual([phase["id"] for phase in executed], ["go-formatting"])
        self.assertEqual(executed[0]["status"], "pass")
        self.assertEqual(len(executed[0]["commands"]), 3)
        self.assertTrue(any(Path(argv[0]).name == "go" and "env" in argv
                            for argv, _ in self.process.calls))

    def test_cli_unknown_or_hardware_phase_is_rejected_before_probes(self):
        for phase in ("unknown-phase", "physical-output-hardware"):
            with self.subTest(phase=phase):
                with patch.object(sys, "stderr", io.StringIO()):
                    with patch.object(validation.shutil, "which", side_effect=AssertionError("unexpected probe")):
                        with self.assertRaises(SystemExit) as error:
                            validation.main(["--repo", str(self.root), "--phase", phase], run=self.process)
                self.assertEqual(error.exception.code, 2)
        self.assertEqual(self.process.calls, [])
        self.assertFalse(self.output.exists())

    def test_cli_scoped_verification_pass_does_not_waive_full_acceptance(self):
        status, summary, report = self.run_cli("--phase", "diff-check")
        self.assertEqual(status, 1)
        self.assertEqual(report["scoped_verification"], {
            "mode": "selected", "status": "passed", "selected_phases": ["diff-check"], "reasons": [],
        })
        self.assertEqual(report["source_stability"], {"status": "stable", "reasons": []})
        self.assertFalse(report["gate"]["accepted"])
        self.assertEqual(summary["scoped_verification"], report["scoped_verification"])
        self.assertEqual(summary["source_stability"], report["source_stability"])
        self.assertEqual(summary["source_content_sha256"], report["source_content_fingerprint"]["sha256"])
        self.assertEqual(summary["reasons"], report["gate"]["reasons"])

    def test_cli_source_content_drift_invalidates_evidence(self):
        def change_source(argv, **kwargs):
            if "diff" in argv and "--check" in argv:
                (self.root / "go.mod").write_text("module changed\ngo 1.25.0\n", encoding="utf-8")
            return self.process(argv, **kwargs)

        status, _, report = self.run_cli("--phase", "diff-check", process=change_source)
        self.assertEqual(status, 1)
        self.assertIn("source_content_fingerprint_changed", report["gate"]["reasons"])
        self.assertEqual(report["source_stability"]["status"], "changed")
        self.assertEqual(report["scoped_verification"]["status"], "not_verified")
        self.assertNotEqual(report["source_content_fingerprint"]["sha256"],
                            report["source_identity_after"]["source_content_fingerprint"]["sha256"])
        phases = {phase["id"]: phase for phase in report["phases"]}
        self.assertEqual(phases["diff-check"]["status"], "pass")

    def test_cli_scoped_failure_and_missing_dependency_are_not_verified(self):
        for unavailable in (False, True):
            with self.subTest(unavailable=unavailable):
                def fail_ui(argv, **kwargs):
                    if Path(argv[0]).name == "npm":
                        return subprocess.CompletedProcess(argv, 1, "", "synthetic assertion failure")
                    return self.process(argv, **kwargs)

                def tools(name, path=None):
                    return None if unavailable and name == "npm" else self.which(name, path)

                _, _, report = self.run_cli("--phase", "ui-audioplayer", process=fail_ui, which=tools)
                phase = next(phase for phase in report["phases"] if phase["id"] == "ui-audioplayer")
                expected = "blocked" if unavailable else "fail"
                self.assertEqual(phase["status"], expected)
                self.assertEqual(report["scoped_verification"]["status"], "not_verified")
                self.assertIn(f"ui-audioplayer={expected}", report["scoped_verification"]["reasons"])
                self.assertEqual(report["source_stability"]["status"], "stable")

    def test_cli_selected_null_phase_still_requires_opt_in_and_executed_spec(self):
        phase_id = "mpv-null-lifecycle"
        for opt_in, skip in ((False, False), (True, False), (True, True)):
            with self.subTest(opt_in=opt_in, skip=skip):
                def null_process(argv, **kwargs):
                    if skip and any(arg.startswith("-ginkgo.focus=") for arg in argv):
                        return subprocess.CompletedProcess(argv, 0, "Ran 0 of 5 Specs in 0s\n", "")
                    return self.process(argv, **kwargs)

                args = ("--mpv-null",) if opt_in else ()
                _, _, report = self.run_cli("--phase", phase_id, *args, process=null_process)
                phase = next(phase for phase in report["phases"] if phase["id"] == phase_id)
                expected = "blocked" if skip else "pass" if opt_in else "not_run"
                self.assertEqual(phase["status"], expected)
                self.assertEqual(report["scoped_verification"]["status"],
                                 "passed" if expected == "pass" else "not_verified")
                if not opt_in:
                    self.assertIn("opt-in omitted", phase["reason"])
                    self.assertEqual(phase["commands"], [])
                self.assertFalse(report["gate"]["accepted"])

    def test_cli_commit_or_status_drift_invalidates_scoped_verification(self):
        for command, field in (("rev-parse", "source_commit"), ("status", "dirty_scope_fingerprint")):
            with self.subTest(field=field):
                checked = False

                def change_identity(argv, **kwargs):
                    nonlocal checked
                    if "diff" in argv and "--check" in argv:
                        checked = True
                    result = self.process(argv, **kwargs)
                    if checked and command in argv:
                        text = "b" * 40 + "\n" if command == "rev-parse" else result.stdout + "?? changed/\n"
                        return subprocess.CompletedProcess(argv, 0, text, "")
                    return result

                _, _, report = self.run_cli("--phase", "diff-check", process=change_identity)
                self.assertEqual(report["source_stability"]["status"], "changed")
                self.assertEqual(report["scoped_verification"]["status"], "not_verified")
                self.assertIn(f"{field}_changed", report["gate"]["reasons"])
                self.assertEqual(report["source_commit"], self.source_commit)

    def test_cli_missing_identity_at_either_boundary_is_not_verified(self):
        for boundary in ("before", "after"):
            for command, field in (("rev-parse", "source_commit"), ("status", "dirty_scope_fingerprint"),
                                   ("ls-files", "source_content_fingerprint")):
                with self.subTest(boundary=boundary, field=field):
                    checked = False

                    def missing_identity(argv, **kwargs):
                        nonlocal checked
                        if "diff" in argv and "--check" in argv:
                            checked = True
                        if command in argv and checked == (boundary == "after"):
                            return subprocess.CompletedProcess(argv, 1, "", "synthetic identity failure")
                        return self.process(argv, **kwargs)

                    status, _, report = self.run_cli("--phase", "diff-check", process=missing_identity)
                    self.assertEqual(status, 1)
                    self.assertEqual(report["source_stability"]["status"], "unavailable")
                    self.assertEqual(report["scoped_verification"]["status"], "not_verified")
                    self.assertIn(f"{field}_{boundary}_unavailable", report["gate"]["reasons"])
                    phase = next(phase for phase in report["phases"] if phase["id"] == "diff-check")
                    self.assertEqual(phase["status"], "pass")

    def test_cli_writes_report_and_returns_unaccepted_without_hardware(self):
        status, _, report = self.run_cli()
        self.assertEqual(status, 1)
        self.assertEqual(report["scoped_verification"], {
            "mode": "full", "status": "not_applicable", "selected_phases": [], "reasons": [],
        })
        self.assertEqual(report["source_stability"]["status"], "stable")
        self.assertEqual([phase["id"] for phase in report["phases"] if phase["category"] == "software"],
                         list(validation.PHASE_IDS))
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

    def test_pipewire_private_phase_is_executed_with_env(self):
        runner = self.make_runner()
        report = self.run_with_fake_tools(runner)
        phases = {phase["id"]: phase for phase in report["phases"]}
        self.assertIn("playback-pipewire-private", phases)
        pipewire_phase = phases["playback-pipewire-private"]
        self.assertEqual(pipewire_phase["status"], "pass")
        commands = [call for call in self.process.calls
                    if call[1]["env"].get("ND_TEST_PRIVATE_PIPEWIRE") == "1"]
        self.assertTrue(len(commands) >= 1)

    def test_pipewire_private_phase_blocks_when_mandatory_tests_missing(self):
        def fixture_only_process(argv, **kwargs):
            if any(arg.startswith("^TestPipeWire(NewTrack|Private)") for arg in argv):
                return subprocess.CompletedProcess(
                    argv, 0,
                    "=== RUN   TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver\n"
                    "--- PASS: TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver (0.01s)\n",
                    "",
                )
            return self.process(argv, **kwargs)

        runner = self.make_runner(process=fixture_only_process)
        report = self.run_with_fake_tools(runner)
        phases = {phase["id"]: phase for phase in report["phases"]}
        self.assertIn("playback-pipewire-private", phases)
        pipewire_phase = phases["playback-pipewire-private"]
        self.assertEqual(pipewire_phase["status"], "blocked")
        self.assertIn("playback-pipewire-private", report["gate"]["critical_blocked"])
        self.assertEqual(report["gate"]["decision"], "not_accepted")

    def test_pipewire_private_timeout_configuration_and_env(self):
        values = validation.phase_timeout_values([("playback-pipewire-private", 99)])
        self.assertEqual(values["playback-pipewire-private"], 99)

        with patch.dict(os.environ, {"ND_VALIDATION_TIMEOUT_PLAYBACK_PIPEWIRE_PRIVATE": "77"}):
            env_values = validation.phase_timeout_values([])
            self.assertEqual(env_values["playback-pipewire-private"], 77)

        with self.assertRaises(validation.ValidationError):
            validation.phase_timeout_values([("non-existent-phase", 50)])

    def test_pipewire_private_execution_check_enforces_mandatory_tests(self):
        valid_output = (
            "=== RUN   TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver\n"
            "--- PASS: TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver (0.04s)\n"
            "=== RUN   TestPipeWirePrivateOutput\n"
            "--- PASS: TestPipeWirePrivateOutput (0.18s)\n"
            "=== RUN   TestPipeWirePrivateSeek\n"
            "--- PASS: TestPipeWirePrivateSeek (0.22s)\n"
            "=== RUN   TestPipeWirePrivateEOF\n"
            "--- PASS: TestPipeWirePrivateEOF (1.94s)\n"
        )
        status, reason = validation.pipewire_private_execution_check(valid_output)
        self.assertIsNone(status)
        self.assertIsNone(reason)

        fixture_only = (
            "=== RUN   TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver\n"
            "--- PASS: TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver (0.04s)\n"
        )
        status, reason = validation.pipewire_private_execution_check(fixture_only)
        self.assertEqual(status, "blocked")
        self.assertIn("TestPipeWirePrivateOutput", reason)

        missing_seek = (
            "=== RUN   TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver\n"
            "--- PASS: TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver (0.04s)\n"
            "=== RUN   TestPipeWirePrivateOutput\n"
            "--- PASS: TestPipeWirePrivateOutput (0.18s)\n"
            "=== RUN   TestPipeWirePrivateEOF\n"
            "--- PASS: TestPipeWirePrivateEOF (1.94s)\n"
        )
        status, reason = validation.pipewire_private_execution_check(missing_seek)
        self.assertEqual(status, "blocked")
        self.assertIn("TestPipeWirePrivateSeek", reason)

        skipped_private = (
            "=== RUN   TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver\n"
            "--- PASS: TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver (0.04s)\n"
            "=== RUN   TestPipeWirePrivateOutput\n"
            "--- SKIP: TestPipeWirePrivateOutput (0.00s)\n"
            "=== RUN   TestPipeWirePrivateSeek\n"
            "--- PASS: TestPipeWirePrivateSeek (0.22s)\n"
            "=== RUN   TestPipeWirePrivateEOF\n"
            "--- PASS: TestPipeWirePrivateEOF (1.94s)\n"
        )
        status, reason = validation.pipewire_private_execution_check(skipped_private)
        self.assertEqual(status, "blocked")
        self.assertIn("TestPipeWirePrivateOutput", reason)

        zero_matches = "ok  \tgithub.com/navidrome/navidrome/core/playback/mpv\t0.005s [no tests to run]\n"
        status, reason = validation.pipewire_private_execution_check(zero_matches)
        self.assertEqual(status, "blocked")

        real_only_no_fixture = (
            "=== RUN   TestPipeWirePrivateOutput\n"
            "--- PASS: TestPipeWirePrivateOutput (0.18s)\n"
            "=== RUN   TestPipeWirePrivateSeek\n"
            "--- PASS: TestPipeWirePrivateSeek (0.22s)\n"
            "=== RUN   TestPipeWirePrivateEOF\n"
            "--- PASS: TestPipeWirePrivateEOF (1.94s)\n"
        )
        status, reason = validation.pipewire_private_execution_check(real_only_no_fixture)
        self.assertEqual(status, "blocked")
        self.assertIn("lifecycle", reason)

    def test_dockerfile_and_compose_content_changes_affect_fingerprint(self):
        dockerfile = self.root / "Dockerfile"
        dockerfile.write_text("FROM alpine:3.20\n", encoding="utf-8")
        compose = self.root / "contrib/docker-compose/docker-compose.bluetooth.yml"
        compose.parent.mkdir(parents=True)
        compose.write_text("services:\n  navidrome:\n    image: navidrome\n", encoding="utf-8")

        def fake_ls(argv, **kwargs):
            if "ls-files" in argv:
                return subprocess.CompletedProcess(
                    argv, 0,
                    "go.mod\0Dockerfile\0contrib/docker-compose/docker-compose.bluetooth.yml\0",
                    "",
                )
            return self.process(argv, **kwargs)

        base = validation.capture_source_content_fingerprint(self.root, fake_ls)
        self.assertIsNone(base["error"])

        dockerfile.write_text("FROM alpine:3.21\n", encoding="utf-8")
        after_dockerfile = validation.capture_source_content_fingerprint(self.root, fake_ls)
        self.assertNotEqual(base["sha256"], after_dockerfile["sha256"])

        dockerfile.write_text("FROM alpine:3.20\n", encoding="utf-8")
        compose.write_text("services:\n  navidrome:\n    image: navidrome:latest\n", encoding="utf-8")
        after_compose = validation.capture_source_content_fingerprint(self.root, fake_ls)
        self.assertNotEqual(base["sha256"], after_compose["sha256"])

    def test_safe_environment_gorace_propagation(self):
        with patch.dict(os.environ, {"GORACE": "halt_on_error=1"}):
            go_env = validation.safe_environment(go=True)
            self.assertEqual(go_env.get("GORACE"), "halt_on_error=1")
            non_go_env = validation.safe_environment(go=False)
            self.assertNotIn("GORACE", non_go_env)
        with patch.dict(os.environ, {}, clear=True):
            go_env = validation.safe_environment(go=True)
            self.assertEqual(go_env.get("GORACE"), "atexit_sleep_ms=0")


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

    def test_software_only_mode_never_requires_or_configures_mpv(self):
        with patch.object(self.smoke, "source_fingerprint", return_value={"commit": "fixture"}):
            app = self.smoke.AppSmoke(
                self.output, Path("/fake/navidrome"), "/fake/chrome", None,
                software_only=True)
        self.addCleanup(app.cleanup)
        env = app.server_env()
        self.assertEqual(env["ND_MPVPATH"], "/usr/bin/false")
        self.assertNotIn("--ao=null", env["ND_MPVCMDTEMPLATE"])
        self.assertEqual(app.summary["evidence_levels"]["level4"], "UNVERIFIED")
        self.assertFalse(app.summary["real_mpv_authorized"])

    def test_software_only_run_does_not_enter_mpv_playback_flow(self):
        self.app.software_only = True
        with patch.object(self.app, "start_server"), \
                patch.object(self.app, "create_admin_and_scan"), \
                patch.object(self.app, "start_vite"), \
                patch.object(self.app, "media_ids", return_value=["one", "two"]), \
                patch.object(self.app, "jukebox_api") as playback, \
                patch.object(self.app, "run_browser") as legacy_browser, \
                patch.object(self.app, "run_software_browser") as software_browser:
            self.app.run()
        playback.assert_not_called()
        legacy_browser.assert_not_called()
        software_browser.assert_called_once()

    def test_commands_include_explicit_current_controller_scope(self):
        self.app.binding_generation = 3
        with patch.object(self.app, "native", return_value=(200, {})) as native:
            self.app.command("/api/jukebox/play", operation_id="stable-play")
            native.assert_called_once_with("POST", "/api/jukebox/play", body={
                "sessionId": self.app.session_id,
                "clientId": self.app.client_id,
                "bindingGeneration": 3,
                "operationId": "g3:stable-play",
            })

    def test_observer_probe_cannot_inherit_owner_identity(self):
        with patch.object(self.app, "native", return_value=(403, {})) as native:
            self.app.command("/api/jukebox/volume", {"gain": 0.25}, client_id="observer", operation_id="observer-probe")
            native.assert_called_once_with("POST", "/api/jukebox/volume", body={
                "gain": 0.25,
                "sessionId": self.app.session_id,
                "clientId": "observer",
                "bindingGeneration": 1,
                "operationId": "g1:observer-probe",
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
