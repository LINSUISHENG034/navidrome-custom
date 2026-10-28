#!/usr/bin/env python3
"""Collect software validation results for the native playback worktree."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import time


SCHEMA_VERSION = 1
SOURCE_PATHS = (
    "go.mod", "go.sum", "main.go", "Makefile", ".nvmrc", "core", "server", "cmd",
    "model", "conf", "utils", "api", "consts", "db", "log", "scheduler",
    "persistence", "adapters", "scanner", "plugins", "resources", "tests",
    "ui/src", "ui/validation", "ui/public", "ui/embed.go", "ui/index.html",
    "ui/package.json", "ui/package-lock.json", "ui/vite.config.js",
    "ui/tsconfig.json", "ui/tsconfig.app.json", "ui/tsconfig.node.json",
    "ui/prettier.config.js", "ui/.eslintrc", "ui/.eslintignore", "fork/scripts",
    "Dockerfile", "contrib/docker-compose/docker-compose.bluetooth.yml",
)
SOURCE_EXTENSIONS = {".go", ".js", ".jsx", ".ts", ".tsx", ".py", ".html", ".json", ".toml"}
PLAYBACK_PACKAGES = ["./core/playback/..."]
BUILD_TAGS = "netgo,sqlite_fts5"
NULL_MPV_SPECS = {
    "mpv-null-lifecycle": "observes paused output without advancing and distinguishes file lifecycle events",
    "mpv-null-paused-seek-candidate": "restores a stable paused position after buffered playback through track commands",
    "mpv-null-transport-recovery": "reports a transport fault with its token and position, then exits the old MPV",
    "mpv-null-natural-eof": "reports natural EOF with the active token while null MPV remains idle",
    "mpv-null-explicit-close": "reports explicit close as retirement, never as natural EOF",
    "mpv-null-context-cancel": "releases the output process when its context is canceled",
}
PHASE_IDS = (
    "playback-normal", "playback-race", "playback-pipewire-private", "playback-pending-service-retirement",
    "playback-stress", "nativeapi-subsonic", "nativeapi-command-authority", "nativeapi-subsonic-command-authority",
    *NULL_MPV_SPECS,
    "ui-full-suite", "ui-audioplayer", "ui-eventstream", "diff-check", "go-formatting", "ui-formatting",
)
BLOCKED_OUTPUTS = (
    "cannot embed directory build/3rdparty: contains no embeddable files",
    "module lookup disabled by GOPROXY=off",
    "cannot find go tool",
    "gcc: not found",
    "c compiler \"gcc\" not found",
    "exec: \"gcc\": executable file not found",
    "exec: \"cc\": executable file not found",
    "vitest: not found",
    "vitest: command not found",
    "cannot find package 'vitest'",
    "prettier: not found",
    "prettier: command not found",
    "cannot find package 'prettier'",
)


class ValidationError(Exception):
    pass


def utc_now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def decode_output(value):
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return str(value)


def safe_environment(*, go=False, mpv_null=False, private_pipewire=False):
    keep = {"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TEMP", "TMP"}
    env = {key: value for key, value in os.environ.items() if key in keep}
    env.update({
        "CI": "1",
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": "/dev/null",
        "ND_ENABLEINSIGHTSCOLLECTOR": "false",
        "ND_JUKEBOX_AUTODISCOVERBLUETOOTH": "false",
    })
    if go:
        env.update({"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOENV": "off", "NOLOG": "1"})
        env["GORACE"] = os.environ.get("GORACE", "atexit_sleep_ms=0")
    if mpv_null:
        env["ND_TEST_MPV_NULL_OUTPUT"] = "1"
    if private_pipewire or os.environ.get("ND_TEST_PRIVATE_PIPEWIRE") == "1":
        env["ND_TEST_PRIVATE_PIPEWIRE"] = "1"
    return env


def isolated_run(argv, **kwargs):
    """Run a process group so a timeout also stops child test processes such as MPV."""
    timeout = kwargs.pop("timeout", None)
    kwargs.pop("check", None)
    process = subprocess.Popen(
        argv,
        stdin=subprocess.DEVNULL,
        start_new_session=(os.name != "nt"),
        **kwargs,
    )
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        if os.name == "nt":
            process.kill()
        else:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        try:
            stdout, stderr = process.communicate(timeout=3)
        except subprocess.TimeoutExpired:
            if os.name == "nt":
                process.kill()
            else:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            stdout, stderr = process.communicate()
        raise subprocess.TimeoutExpired(
            argv, timeout,
            output=stdout if stdout is not None else error.output,
            stderr=stderr if stderr is not None else error.stderr,
        )
    return subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)


def run_record(run, argv, cwd, env, timeout):
    started = time.monotonic()
    try:
        result = run(
            argv,
            cwd=str(cwd),
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            timeout=timeout,
        )
        return {
            "command": list(argv),
            "cwd": str(cwd),
            "exit_code": result.returncode,
            "stdout": decode_output(result.stdout),
            "stderr": decode_output(result.stderr),
            "duration_seconds": round(time.monotonic() - started, 3),
            "timed_out": False,
            "launch_error": None,
        }
    except subprocess.TimeoutExpired as error:
        return {
            "command": list(argv),
            "cwd": str(cwd),
            "exit_code": None,
            "stdout": decode_output(error.stdout),
            "stderr": decode_output(error.stderr),
            "duration_seconds": round(time.monotonic() - started, 3),
            "timed_out": True,
            "launch_error": None,
        }
    except OSError as error:
        return {
            "command": list(argv),
            "cwd": str(cwd),
            "exit_code": None,
            "stdout": "",
            "stderr": "",
            "duration_seconds": round(time.monotonic() - started, 3),
            "timed_out": False,
            "launch_error": f"{type(error).__name__}: {error}",
        }


def parse_go_version(value):
    match = re.search(r"\bgo(\d+)\.(\d+)(?:\.(\d+))?", value)
    if match is None:
        return None
    return tuple(int(part or 0) for part in match.groups())


def required_go_version(repo):
    go_mod = repo / "go.mod"
    if not go_mod.is_file():
        return None
    for line in go_mod.read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"\s*go\s+(\d+\.\d+(?:\.\d+)?)\s*", line)
        if match:
            return parse_go_version("go" + match.group(1))
    return None


def resolve_executable(value):
    if not value:
        return None
    candidate = Path(value).expanduser()
    if candidate.parent != Path(".") or candidate.is_absolute():
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate.resolve())
        return None
    return shutil.which(value)


def discover_go(repo, run, requested=None):
    """Select an already-installed compatible Go toolchain without downloading one."""
    required = required_go_version(repo)
    configured = os.environ.get("ND_VALIDATION_GO")
    requested = requested or configured
    probes = []
    candidates = []

    def add_candidate(value, source):
        executable = resolve_executable(value)
        if executable and all(item["path"] != executable for item in candidates):
            candidates.append({"path": executable, "source": source})

    if requested:
        add_candidate(requested, "explicit override")
        if not candidates:
            return None, {"required": required, "configured": requested, "candidates": [], "probes": probes,
                          "error": "configured Go executable does not resolve to an executable"}
    else:
        add_candidate(shutil.which("go"), "PATH")
        add_candidate(str(Path.home() / ".local/go/bin/go"), "user-local")
        add_candidate("/usr/local/go/bin/go", "system-local")

    probe_env = safe_environment(go=True)
    cache_roots = []
    if os.environ.get("GOMODCACHE"):
        cache_roots.append(Path(os.environ["GOMODCACHE"]).expanduser())
    for candidate in list(candidates):
        record = run_record(run, [candidate["path"], "env", "GOVERSION", "GOMODCACHE", "GOOS", "GOARCH"],
                            repo, probe_env, 15)
        probes.append(record)
        if record["exit_code"] == 0:
            values = record["stdout"].splitlines()
            if len(values) >= 4:
                candidate.update(version=values[0].strip(), parsed_version=parse_go_version(values[0]),
                                 gomodcache=values[1].strip(), goos=values[2].strip(), goarch=values[3].strip())
                cache_roots.append(Path(values[1].strip()).expanduser())

    # Go's downloaded toolchains live in the module cache; their version is read from go env,
    # never inferred from a pinned directory name or fetched by this runner.
    for root in cache_roots:
        toolchains = root / "golang.org"
        if not toolchains.is_dir():
            continue
        for metadata in toolchains.glob("toolchain@*/bin/go"):
            add_candidate(str(metadata), "GOMODCACHE metadata")
    configured_toolchain = os.environ.get("GOTOOLCHAIN", "auto")
    if configured_toolchain.startswith("go"):
        add_candidate(configured_toolchain, "GOTOOLCHAIN executable")

    probed_paths = {record["command"][0] for record in probes}
    for candidate in candidates:
        if candidate["path"] in probed_paths:
            continue
        record = run_record(run, [candidate["path"], "env", "GOVERSION", "GOMODCACHE", "GOOS", "GOARCH"],
                            repo, probe_env, 15)
        probes.append(record)
        if record["exit_code"] == 0:
            values = record["stdout"].splitlines()
            if len(values) >= 4:
                candidate.update(version=values[0].strip(), parsed_version=parse_go_version(values[0]),
                                 gomodcache=values[1].strip(), goos=values[2].strip(), goarch=values[3].strip())

    compatible = [candidate for candidate in candidates if candidate.get("parsed_version") and
                  (required is None or candidate["parsed_version"] >= required)]
    if requested and candidates:
        selected = candidates[0]
        if not selected.get("parsed_version"):
            error = "configured Go binary could not report its version/environment"
        elif required and selected["parsed_version"] < required:
            error = f"configured Go {selected['version']} is older than go.mod requirement {required}"
        else:
            error = None
    elif compatible:
        selected = max(compatible, key=lambda item: item["parsed_version"])
        error = None
    elif candidates:
        selected = max(candidates, key=lambda item: item.get("parsed_version") or (0, 0, 0))
        error = f"no discovered Go toolchain satisfies go.mod requirement {required}"
    else:
        selected = None
        error = "no installed Go executable was discovered"

    details = {
        "required": required,
        "configured": requested,
        "configured_gotoolchain": configured_toolchain,
        "selected": selected,
        "candidates": candidates,
        "probes": probes,
        "error": error,
    }
    return (selected["path"] if selected else None), details


def go_command(go_binary, *args):
    if not go_binary:
        return None
    return [go_binary, *args]


def ginkgo_execution_check(output, expected_spec):
    lines = output.splitlines()
    matching = [line for line in lines if expected_spec in line]
    if not matching:
        return "blocked", f"expected Ginkgo spec was absent from output: {expected_spec}"
    if "[SKIPPED]" in output or "SKIPPED" in output:
        return "blocked", "Ginkgo reported the opt-in null-MPV spec as skipped"
    summaries = [line for line in lines if re.search(r"Ran\s+\d+\s+of\s+\d+\s+Specs?", line)]
    if not summaries:
        return "blocked", "Ginkgo did not report a spec execution summary"
    ran = re.search(r"Ran\s+(\d+)\s+of\s+\d+\s+Specs?", summaries[-1])
    if ran is None or int(ran.group(1)) < 1:
        return "blocked", "Ginkgo reported zero executed specs"
    return None, None


def pipewire_private_execution_check(output):
    mandatory_private = (
        "TestPipeWirePrivateOutput",
        "TestPipeWirePrivateSeek",
        "TestPipeWirePrivateEOF",
    )
    for name in mandatory_private:
        if not re.search(r"^\s*--- PASS: " + re.escape(name) + r"\s", output, re.MULTILINE):
            return "blocked", f"mandatory real-stack private test not executed successfully: {name}"
    if not re.search(r"^\s*--- PASS: TestPipeWireNewTrack", output, re.MULTILINE):
        return "blocked", "synthetic PipeWire lifecycle fixture tests were absent from execution output"
    return None, None


def output_is_environment_blocker(output):
    lowered = output.lower()
    return any(phrase.lower() in lowered for phrase in BLOCKED_OUTPUTS)


def create_run_directory(output_root):
    output_root.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("run-%Y%m%dT%H%M%SZ")
    candidate = output_root / stamp
    suffix = 1
    while candidate.exists():
        candidate = output_root / f"{stamp}-{suffix}"
        suffix += 1
    candidate.mkdir()
    return candidate


def write_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(temporary, path)


class ValidationRunner:
    def __init__(self, repo, output, *, go_binary, go_details, timeout, phase_timeouts,
                 stress_count, mpv_null, run=isolated_run, source_commit=None, provenance=None,
                 dirty_scope_fingerprint=None, source_content_fingerprint=None, selected_phases=None):
        self.repo = Path(repo).resolve()
        self.output = Path(output).resolve()
        self.go_binary = go_binary
        self.go_details = go_details or {}
        self.timeout = timeout
        self.phase_timeouts = phase_timeouts or {}
        self.stress_count = stress_count
        self.mpv_null = mpv_null
        self.run = run
        self.source_commit = source_commit
        self.provenance = provenance or {}
        self.dirty_scope_fingerprint = dirty_scope_fingerprint or {}
        self.source_content_fingerprint = source_content_fingerprint or {}
        self.source_identity_after = None
        self.selected_phases = None if selected_phases is None else frozenset(selected_phases)
        if self.selected_phases is not None:
            if not self.selected_phases or self.selected_phases.difference(PHASE_IDS):
                raise ValidationError("select at least one known validation phase")
        self.results = []
        self.started_at = utc_now()
        self.run_directory = None

    def phase_selected(self, phase_id):
        return self.selected_phases is None or phase_id in self.selected_phases

    def _phase(self, phase_id, title, *, critical=True, category="software", commands=None,
               blocked_reason=None, not_run_reason=None, expected_failure=False, env=None,
               cwd=None, timeout=None, output_check=None):
        phase_started = time.monotonic()
        phase = {
            "id": phase_id,
            "title": title,
            "critical": critical,
            "category": category,
            "status": "not_run",
            "source_commit": self.source_commit,
            "commands": [],
            "duration_seconds": 0.0,
            "reason": None,
        }
        if category == "software" and not self.phase_selected(phase_id):
            not_run_reason = "not selected by --phase"
        if not_run_reason:
            phase["reason"] = not_run_reason
            self.results.append(phase)
            return phase
        if blocked_reason:
            phase["status"] = "blocked"
            phase["reason"] = blocked_reason
            self.results.append(phase)
            return phase

        phase_env = safe_environment(go=phase_id.startswith("playback") or phase_id.startswith("nativeapi") or
                                     phase_id.startswith("mpv-null"),
                                     mpv_null=phase_id.startswith("mpv-null"))
        if env:
            phase_env.update(env)
        for index, argv in enumerate(commands or [], start=1):
            record = run_record(self.run, argv, cwd or self.repo, phase_env, timeout or self.timeout)
            prefix = f"{phase_id}-{index}"
            stdout_path = self.run_directory / f"{prefix}.stdout.log"
            stderr_path = self.run_directory / f"{prefix}.stderr.log"
            stdout_path.write_text(record["stdout"], encoding="utf-8")
            stderr_path.write_text(record["stderr"], encoding="utf-8")
            record["stdout_log"] = str(stdout_path)
            record["stderr_log"] = str(stderr_path)
            phase["commands"].append(record)

        phase["duration_seconds"] = round(time.monotonic() - phase_started, 3)
        if not phase["commands"]:
            phase["status"] = "blocked"
            phase["reason"] = "no executable command was constructed"
        else:
            output_text = "\n".join(
                record["stdout"] + "\n" + record["stderr"] for record in phase["commands"]
            )
            launch_errors = [record["launch_error"] for record in phase["commands"] if record["launch_error"]]
            timed_out = any(record["timed_out"] for record in phase["commands"])
            failed = [record for record in phase["commands"] if record["exit_code"] not in (0, None)]
            if launch_errors:
                phase["status"] = "blocked"
                phase["reason"] = "; ".join(launch_errors)
            elif output_is_environment_blocker(output_text):
                phase["status"] = "blocked"
                phase["reason"] = "required local build/runtime dependency is unavailable"
            elif timed_out:
                phase["status"] = "fail"
                phase["reason"] = "command exceeded its configured timeout"
            elif failed:
                if expected_failure and output_check:
                    checked_status, reason = output_check(output_text)
                    if checked_status:
                        phase["status"] = checked_status
                        phase["reason"] = f"test did not confirm expected spec execution: {reason}"
                    else:
                        phase["status"] = "expectedfail"
                        phase["reason"] = f"expected regression failed ({len(failed)} command(s) exited nonzero)"
                else:
                    phase["status"] = "expectedfail" if expected_failure else "fail"
                    phase["reason"] = f"{len(failed)} command(s) exited nonzero"
            else:
                phase["status"] = "pass"
            if output_check and phase["status"] == "pass":
                checked_status, reason = output_check(output_text)
                if checked_status:
                    phase["status"] = checked_status
                    phase["reason"] = reason
        self.results.append(phase)
        return phase

    def _go_blocked_reason(self):
        if not self.go_binary:
            return self.go_details.get("error") or "no Go toolchain is available"
        if self.go_details.get("error"):
            return self.go_details["error"]
        return None

    def phase_timeout(self, phase_id, default):
        return self.phase_timeouts.get(phase_id, self.timeout if self.timeout is not None else default)

    def run_all(self):
        self.run_directory = create_run_directory(self.output)
        go_error = self._go_blocked_reason()
        tags = f"-tags={BUILD_TAGS}"
        base_go_test = ["test", tags, "-count=1", "-timeout=20m", "-v", *PLAYBACK_PACKAGES]
        self._phase(
            "playback-normal", "Playback packages: normal tests",
            commands=[go_command(self.go_binary, *base_go_test)] if self.go_binary else None,
            blocked_reason=go_error,
            timeout=self.phase_timeout("playback-normal", 1800),
        )
        self._phase(
            "playback-race", "Playback packages: race detector",
            commands=[go_command(self.go_binary, "test", "-race", tags, "-count=1", "-timeout=30m",
                                 *PLAYBACK_PACKAGES)] if self.go_binary else None,
            blocked_reason=go_error,
            timeout=self.phase_timeout("playback-race", 2400),
        )
        pipewire_test = "TestPipeWire"
        self._phase(
            "playback-pipewire-private", "Private PipeWire protocol integration tests",
            commands=[go_command(self.go_binary, "test", tags, "-count=1", "-timeout=90s", "-v",
                                 "./core/playback/mpv", "-run", "^" + pipewire_test + "(NewTrack|Private)")] if self.go_binary else None,
            blocked_reason=go_error,
            env={"ND_TEST_PRIVATE_PIPEWIRE": "1"},
            timeout=self.phase_timeout("playback-pipewire-private", 180),
            output_check=pipewire_private_execution_check,
        )
        retirement_test = "TestPlaybackServerRunWaitsForPendingOutputRetirement"
        self._phase(
            "playback-pending-service-retirement", "Service shutdown waits for pending output retirement",
            commands=[go_command(self.go_binary, "test", "-race", tags, "-count=1", "-timeout=60s", "-v",
                                 "./core/playback", "-run", "^" + retirement_test + "$")] if self.go_binary else None,
            blocked_reason=go_error,
            env={"ND_TEST_PENDING_SERVICE_RETIREMENT": "1"},
            timeout=self.phase_timeout("playback-pending-service-retirement", 180),
            output_check=lambda output: go_execution_check(output, retirement_test),
        )
        stress_commands = [
            go_command(self.go_binary, "test", tags, "-count=1", "-timeout=45m", *PLAYBACK_PACKAGES)
            for _ in range(self.stress_count)
        ] if self.go_binary else None
        self._phase(
            "playback-stress", "Playback packages: independent repeated runs",
            commands=stress_commands,
            blocked_reason=go_error,
            timeout=self.phase_timeout("playback-stress", 3600),
        )
        self._phase(
            "nativeapi-subsonic", "Native API and Subsonic regression tests",
            commands=[go_command(self.go_binary, "test", tags, "-count=1", "-timeout=30m",
                                 "./server/nativeapi/...", "./server/subsonic/...")] if self.go_binary else None,
            blocked_reason=go_error,
            timeout=self.phase_timeout("nativeapi-subsonic", 2400),
        )

        authority_test = "TestJukeboxSessionCommandAuthorityValidation"
        self._phase(
            "nativeapi-command-authority", "Native session-bound command authority contract",
            commands=[go_command(self.go_binary, "test", tags, "-count=1", "-timeout=60s", "-v",
                                 "./server/nativeapi", "-run", "^" + authority_test + "$")] if self.go_binary else None,
            blocked_reason=go_error,
            env={"ND_TEST_SESSION_COMMAND_AUTHORITY": "1"},
            timeout=self.phase_timeout("nativeapi-command-authority", 180),
            output_check=lambda output: go_execution_check(output, authority_test, (
                "non-owner_cannot_mutate_after_heartbeat_denial",
                "owner_command_targets_session-bound_non-default_device",
            )),
        )
        authority_spec = "rejects non-owner mutation after session heartbeat denial"
        self._phase(
            "nativeapi-subsonic-command-authority", "Subsonic non-owner command authority contract",
            commands=[go_command(self.go_binary, "test", tags, "-count=1", "-timeout=60s", "-v",
                                 "./server/subsonic", "-run", "^TestSubsonicApi$",
                                 "-ginkgo.focus=" + authority_spec, "-ginkgo.v")] if self.go_binary else None,
            blocked_reason=go_error,
            env={"ND_TEST_SESSION_COMMAND_AUTHORITY": "1"},
            timeout=self.phase_timeout("nativeapi-subsonic-command-authority", 180),
            output_check=lambda output: ginkgo_execution_check(output, authority_spec),
        )

        if not self.mpv_null:
            for phase_id, spec in NULL_MPV_SPECS.items():
                self._phase(phase_id, f"Explicit MPV null-output test: {spec}",
                            not_run_reason="opt-in omitted; pass --mpv-null or set ND_TEST_MPV_NULL_OUTPUT=1")
        else:
            mpv_path = shutil.which("mpv", path=safe_environment().get("PATH"))
            for phase_id, spec in NULL_MPV_SPECS.items():
                if not mpv_path:
                    self._phase(phase_id, f"Explicit MPV null-output test: {spec}",
                                blocked_reason="MPV executable is not installed or not on PATH")
                    continue
                focus = spec
                command = go_command(
                    self.go_binary, "test", tags, "-count=1", "-timeout=45s", "-v", "./core/playback/mpv",
                    "-run", "^TestMPV$", "-ginkgo.focus=" + focus, "-ginkgo.v",
                )
                self._phase(
                    phase_id,
                    f"Explicit MPV null-output test: {spec}",
                    commands=[command] if command else None,
                    blocked_reason=go_error,
                    timeout=self.phase_timeout(phase_id, 180),
                    output_check=lambda output, expected=spec: ginkgo_execution_check(output, expected),
                )

        npm = resolve_executable("npm")
        npm_env = {"NPM_CONFIG_OFFLINE": "true"}
        self._phase(
            "ui-full-suite", "Complete frontend test suite",
            commands=[[npm, "test", "--", "--run"]] if npm else None,
            blocked_reason=None if npm else "npm executable is unavailable",
            cwd=self.repo / "ui",
            env=npm_env,
            timeout=self.phase_timeout("ui-full-suite", 1800),
        )
        self._phase(
            "ui-audioplayer", "Relevant audioplayer and prototype tests",
            commands=[[npm, "test", "--", "--run", "src/audioplayer"]] if npm else None,
            blocked_reason=None if npm else "npm executable is unavailable",
            cwd=self.repo / "ui",
            env=npm_env,
            timeout=self.phase_timeout("ui-audioplayer", 1200),
        )
        eventstream_commands = [
            [npm, "test", "--", "--run", path]
            for path in ("src/eventStream.test.js", "src/common/useRefreshOnEvents.test.js")
        ] if npm else None
        self._phase(
            "ui-eventstream", "Event-stream and event-refresh tests",
            commands=eventstream_commands,
            blocked_reason=None if npm else "npm executable is unavailable",
            cwd=self.repo / "ui",
            env=npm_env,
            timeout=self.phase_timeout("ui-eventstream", 600),
        )

        git = resolve_executable("git")
        diff_commands = [[git, "diff", "--check"], [git, "diff", "--cached", "--check"]] if git else None
        self._phase(
            "diff-check", "Whitespace and patch consistency",
            commands=diff_commands,
            blocked_reason=None if git else "git executable is unavailable",
            timeout=self.phase_timeout("diff-check", 1800),
        )
        self._run_gofmt_phase(go_error, git)

        prettier = [npm, "run", "check-formatting"] if npm else None
        self._phase(
            "ui-formatting", "Frontend formatting check",
            commands=[prettier] if prettier else None,
            blocked_reason=None if npm else "npm executable is unavailable",
            cwd=self.repo / "ui",
            env={"NPM_CONFIG_OFFLINE": "true"},
            timeout=self.phase_timeout("ui-formatting", 900),
        )
        self._phase(
            "physical-output-hardware", "Physical output and Bluetooth validation",
            category="hardware",
            not_run_reason="not run: this software-only runner never accesses Bluetooth, D-Bus, audio devices, or production containers",
        )
        self.source_identity_after = capture_source_identity(self.repo, self.run)
        return self.report()

    def _run_gofmt_phase(self, go_error, git):
        phase_id = "go-formatting"
        title = "Go formatting check on changed Go files"
        if not self.phase_selected(phase_id):
            return self._phase(phase_id, title)
        phase_started = time.monotonic()
        phase = {
            "id": phase_id,
            "title": title,
            "critical": True,
            "category": "software",
            "status": "blocked",
            "source_commit": self.source_commit,
            "commands": [],
            "duration_seconds": 0.0,
            "reason": None,
        }
        if not git:
            phase["reason"] = "git executable is unavailable"
            self.results.append(phase)
            return phase
        if not self.go_binary:
            phase["reason"] = go_error or "no Go toolchain is available"
            self.results.append(phase)
            return phase

        env = safe_environment(go=True)
        queries = [
            [git, "diff", "--name-only", "--diff-filter=ACMRT", "--", "*.go"],
            [git, "diff", "--cached", "--name-only", "--diff-filter=ACMRT", "--", "*.go"],
            [git, "ls-files", "--others", "--exclude-standard", "--", "*.go"],
        ]
        changed = set()
        for index, argv in enumerate(queries, start=1):
            record = run_record(self.run, argv, self.repo, env, self.phase_timeout("go-formatting", 1800))
            self._save_command_log(phase["id"], index, record)
            phase["commands"].append(record)
            if record["exit_code"] != 0:
                phase["reason"] = "failed to discover changed Go source files"
                phase["status"] = "fail"
                continue
            for item in record["stdout"].splitlines():
                path = (self.repo / item).resolve()
                if path.is_relative_to(self.repo) and path.is_file() and path.suffix == ".go":
                    changed.add(path)
        gofmt = Path(self.go_binary).with_name("gofmt")
        if not changed:
            if phase["status"] != "fail":
                phase["status"] = "pass"
                phase["reason"] = "no changed Go files to format-check"
        elif not gofmt.is_file() or not os.access(gofmt, os.X_OK):
            phase["status"] = "blocked"
            phase["reason"] = "gofmt paired with the selected Go toolchain is unavailable"
        else:
            argv = [str(gofmt), "-l", *sorted(str(path) for path in changed)]
            record = run_record(self.run, argv, self.repo, env, self.phase_timeout("go-formatting", 1800))
            self._save_command_log(phase["id"], len(phase["commands"]) + 1, record)
            phase["commands"].append(record)
            if record["exit_code"] != 0:
                phase["status"] = "fail"
                phase["reason"] = "gofmt exited nonzero"
            elif record["stdout"].strip():
                phase["status"] = "fail"
                phase["reason"] = "gofmt reported unformatted files"
            elif phase["status"] != "fail":
                phase["status"] = "pass"
        phase["duration_seconds"] = round(time.monotonic() - phase_started, 3)
        self.results.append(phase)
        return phase

    def _save_command_log(self, phase_id, index, record):
        prefix = f"{phase_id}-{index}"
        stdout_path = self.run_directory / f"{prefix}.stdout.log"
        stderr_path = self.run_directory / f"{prefix}.stderr.log"
        stdout_path.write_text(record["stdout"], encoding="utf-8")
        stderr_path.write_text(record["stderr"], encoding="utf-8")
        record["stdout_log"] = str(stdout_path)
        record["stderr_log"] = str(stderr_path)

    def scoped_verification(self, source_stability):
        selected = [phase_id for phase_id in PHASE_IDS
                    if self.selected_phases is not None and phase_id in self.selected_phases]
        result = {
            "mode": "full" if self.selected_phases is None else "selected",
            "selected_phases": selected,
            "status": "not_applicable",
            "reasons": [],
        }
        if self.selected_phases is None:
            return result
        reasons = list(source_stability["reasons"])
        phases = {phase["id"]: phase for phase in self.results}
        for phase_id in selected:
            status = phases.get(phase_id, {}).get("status", "not_run")
            if status != "pass":
                reasons.append(f"{phase_id}={status}")
        result.update(status="not_verified" if reasons else "passed", reasons=reasons)
        return result

    def report(self):
        hard_failures = [phase["id"] for phase in self.results
                         if phase["critical"] and phase["status"] in {"fail", "expectedfail"}]
        blocked = [phase["id"] for phase in self.results
                   if phase["critical"] and phase["status"] == "blocked"]
        not_run = [phase["id"] for phase in self.results
                   if phase["critical"] and phase["status"] == "not_run"]
        provenance_missing = self.source_commit is None
        dirty_scope_missing = not self.dirty_scope_fingerprint.get("sha256")
        reasons = []
        if hard_failures:
            reasons.append("critical_failures=" + ",".join(hard_failures))
        if blocked:
            reasons.append("critical_blocked=" + ",".join(blocked))
        if not_run:
            reasons.append("critical_not_run=" + ",".join(not_run))
        if provenance_missing:
            reasons.append("source_commit_unavailable")
        if dirty_scope_missing:
            reasons.append("dirty_scope_fingerprint_unavailable")
        if not self.source_content_fingerprint.get("sha256"):
            reasons.append("source_content_fingerprint_unavailable")
        source_stability = compare_source_identities({
            "source_commit": self.source_commit,
            "provenance": self.provenance,
            "dirty_scope_fingerprint": self.dirty_scope_fingerprint,
            "source_content_fingerprint": self.source_content_fingerprint,
        }, self.source_identity_after)
        reasons.extend(source_stability["reasons"])
        accepted = not reasons
        return {
            "schema_version": SCHEMA_VERSION,
            "started_at": self.started_at,
            "finished_at": utc_now(),
            "repository": str(self.repo),
            "source_commit": self.source_commit,
            "dirty_scope_fingerprint": self.dirty_scope_fingerprint,
            "source_content_fingerprint": self.source_content_fingerprint,
            "provenance": self.provenance,
            "source_identity_after": self.source_identity_after,
            "source_stability": source_stability,
            "scoped_verification": self.scoped_verification(source_stability),
            "go_toolchain": self.go_details,
            "gate": {
                "decision": "accepted" if accepted else "not_accepted",
                "accepted": accepted,
                "critical_failures": hard_failures,
                "critical_blocked": blocked,
                "critical_not_run": not_run,
                "reasons": reasons,
            },
            "phases": self.results,
            "run_directory": str(self.run_directory) if self.run_directory else None,
        }


def go_execution_check(output, expected_test, subtests=()):
    for name in (expected_test, *(expected_test + "/" + name for name in subtests)):
        if not re.search(r"^\s*--- PASS: " + re.escape(name) + r"\s", output, re.MULTILINE):
            return "blocked", f"required test not executed successfully: {name}"
    return None, None


def capture_source_commit(repo, run):
    git = resolve_executable("git")
    if not git:
        return None, {"error": "git executable is unavailable"}
    record = run_record(run, [git, "rev-parse", "HEAD"], repo, safe_environment(), 15)
    commit = record["stdout"].strip() if record["exit_code"] == 0 else None
    if commit and not re.fullmatch(r"[0-9a-f]{40,64}", commit):
        commit = None
        record["validation_error"] = "git returned an invalid object ID"
    return commit, {"command": record, "error": None if commit else "could not capture source commit"}


def capture_dirty_scope_fingerprint(repo, run):
    git = resolve_executable("git")
    if not git:
        return {"sha256": None, "entries": [], "command": None, "error": "git executable is unavailable"}
    env = safe_environment()
    env["GIT_OPTIONAL_LOCKS"] = "0"
    record = run_record(run, [git, "status", "--porcelain=v1", "--untracked-files=normal"], repo, env, 15)
    if record["exit_code"] != 0:
        return {"sha256": None, "entries": [], "command": record, "error": "could not capture dirty worktree scope"}
    status = record["stdout"]
    return {
        "sha256": hashlib.sha256(status.encode("utf-8")).hexdigest(),
        "meaning": "hash of git status text only; not a source-content fingerprint",
        "entries": status.splitlines(),
        "command": record,
        "error": None,
    }


def capture_source_content_fingerprint(repo, run):
    """Hash ordered source paths and bytes, not git status or private untracked files."""
    result = {
        "sha256": None, "entries": [], "commands": [], "error": None,
        "scope": list(SOURCE_PATHS),
        "assumption": "source remains frozen during the run; this is not a machine, dependency-cache or generated-build-output fingerprint",
        "algorithm": "sha256 of ordered length-prefixed path, entry kind and file bytes or symlink target text",
    }
    git = resolve_executable("git")
    if not git:
        result["error"] = "git executable is unavailable"
        return result
    env = safe_environment()
    env["GIT_OPTIONAL_LOCKS"] = "0"
    paths = set()
    for flags in (["--cached"], ["--others", "--exclude-standard"]):
        record = run_record(run, [git, "ls-files", "-z", *flags, "--", *SOURCE_PATHS], repo, env, 15)
        result["commands"].append(record)
        if record["exit_code"] != 0:
            result["error"] = "could not enumerate source files"
            return result
        for relative in record["stdout"].split("\0"):
            if relative and (flags == ["--cached"] or Path(relative).suffix in SOURCE_EXTENSIONS):
                paths.add(relative)
    digest = hashlib.sha256()
    root = Path(repo).resolve()
    try:
        for relative in sorted(paths):
            path = root / relative
            if not path.resolve().is_relative_to(root):
                raise ValueError(f"source path or symlink escapes repository: {relative}")
            if any(parent.is_symlink() for parent in path.parents if parent != root and parent.is_relative_to(root)):
                raise ValueError(f"source parent is a symlink: {relative}")
            kind = "file"
            if path.is_symlink():
                target = path.resolve().relative_to(root).as_posix()
                if not any(target == scope or target.startswith(scope + "/") for scope in SOURCE_PATHS):
                    raise ValueError(f"symlink targets a path outside source scope: {relative}")
                kind = "symlink"
                content = os.fsencode(os.readlink(path))
            else:
                content = path.read_bytes()
            name = relative.encode("utf-8")
            for value in (name, kind.encode("ascii"), content):
                digest.update(len(value).to_bytes(8, "big"))
                digest.update(value)
            result["entries"].append({"path": relative, "kind": kind, "sha256": hashlib.sha256(content).hexdigest()})
        if not paths:
            raise ValueError("no source files were enumerated")
    except (OSError, ValueError) as error:
        result["error"] = str(error)
        return result
    result["sha256"] = digest.hexdigest()
    return result


def capture_source_identity(repo, run):
    source_commit, provenance = capture_source_commit(repo, run)
    return {
        "source_commit": source_commit,
        "provenance": provenance,
        "dirty_scope_fingerprint": capture_dirty_scope_fingerprint(repo, run),
        "source_content_fingerprint": capture_source_content_fingerprint(repo, run),
    }


def compare_source_identities(before, after):
    fingerprints = ("dirty_scope_fingerprint", "source_content_fingerprint")
    reasons = []
    for boundary, identity in (("before", before), ("after", after)):
        identity = identity or {}
        if not identity.get("source_commit") or identity.get("provenance", {}).get("error"):
            reasons.append(f"source_commit_{boundary}_unavailable")
        for name in fingerprints:
            fingerprint = identity.get(name, {})
            if not fingerprint.get("sha256") or fingerprint.get("error"):
                reasons.append(f"{name}_{boundary}_unavailable")
    if reasons:
        return {"status": "unavailable", "reasons": reasons}
    if before["source_commit"] != after["source_commit"]:
        reasons.append("source_commit_changed")
    for name in fingerprints:
        if before[name]["sha256"] != after[name]["sha256"]:
            reasons.append(f"{name}_changed")
    return {"status": "changed" if reasons else "stable", "reasons": reasons}


def parse_phase_timeout(value):
    match = re.fullmatch(r"([a-z][a-z0-9-]*)=([1-9][0-9]*)", value)
    if match is None:
        raise argparse.ArgumentTypeError("expected PHASE=POSITIVE_SECONDS")
    return match.group(1), int(match.group(2))


def phase_timeout_values(overrides):
    values = {}
    for phase_id in PHASE_IDS:
        key = "ND_VALIDATION_TIMEOUT_" + phase_id.upper().replace("-", "_")
        if os.environ.get(key):
            try:
                seconds = int(os.environ[key])
            except ValueError as error:
                raise ValidationError(f"{key} must be a positive integer") from error
            if seconds <= 0:
                raise ValidationError(f"{key} must be a positive integer")
            values[phase_id] = seconds
    for phase_id, seconds in overrides:
        if phase_id not in PHASE_IDS:
            raise ValidationError(f"unknown validation phase for timeout: {phase_id}")
        values[phase_id] = seconds
    return values


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=os.environ.get("ND_VALIDATION_REPO"),
                        help="repository root (default: this script's repository)")
    parser.add_argument("--output", default=os.environ.get("ND_VALIDATION_OUTPUT"),
                        help="report root (default: repo/tmp/native-playback-validation)")
    parser.add_argument("--go", dest="go_binary", default=os.environ.get("ND_VALIDATION_GO"),
                        help="installed Go executable; no toolchain downloads are permitted")
    parser.add_argument("--timeout", type=int, default=int(os.environ["ND_VALIDATION_TIMEOUT"])
                        if os.environ.get("ND_VALIDATION_TIMEOUT") else None,
                        help="override all phase timeouts in seconds")
    parser.add_argument("--phase", action="append", choices=PHASE_IDS, metavar="ID",
                        help="run only a selected phase (repeatable); other phases remain not_run. "
                             "Choices: " + ", ".join(PHASE_IDS))
    parser.add_argument("--phase-timeout", action="append", type=parse_phase_timeout, default=[],
                        metavar="PHASE=SECONDS", help="override one phase timeout (repeatable)")
    parser.add_argument("--stress-count", type=int,
                        default=int(os.environ.get("ND_VALIDATION_STRESS_COUNT", "20")),
                        help="Go test repetition count for playback stress phase")
    parser.add_argument("--mpv-null", action="store_true",
                        default=os.environ.get("ND_TEST_MPV_NULL_OUTPUT") == "1",
                        help="explicitly run the real MPV --ao=null integration specs")
    return parser.parse_args(argv)


def main(argv=None, *, run=isolated_run, stdout=None, stderr=None):
    stdout = stdout or sys.stdout
    stderr = stderr or sys.stderr
    args = parse_args(argv)
    script_repo = Path(__file__).resolve().parents[2]
    repo = Path(args.repo or script_repo).expanduser().resolve()
    if not repo.is_dir():
        print(f"error: repository does not exist: {repo}", file=stderr)
        return 2
    output = Path(args.output).expanduser().resolve() if args.output else repo / "tmp/native-playback-validation"
    if output == Path(output.anchor):
        print("error: output cannot be the filesystem root", file=stderr)
        return 2
    if output == repo or (output.is_relative_to(repo) and not output.is_relative_to(repo / "tmp")):
        print("error: output inside the repository must be under repo/tmp", file=stderr)
        return 2
    if (args.timeout is not None and args.timeout <= 0) or args.stress_count <= 0:
        print("error: timeout and stress-count must be positive", file=stderr)
        return 2
    try:
        phase_timeouts = phase_timeout_values(args.phase_timeout)
    except ValidationError as error:
        print(f"error: {error}", file=stderr)
        return 2

    source_identity = capture_source_identity(repo, run)
    needs_go = args.phase is None or any(
        phase.startswith(("playback-", "nativeapi-", "mpv-null-")) or phase == "go-formatting"
        for phase in args.phase
    )
    if needs_go:
        go_binary, go_details = discover_go(repo, run, args.go_binary)
    else:
        go_binary, go_details = None, {"selected": None, "reason": "not required by selected phases"}
    runner = ValidationRunner(
        repo, output,
        go_binary=go_binary,
        go_details=go_details,
        timeout=args.timeout,
        phase_timeouts=phase_timeouts,
        stress_count=args.stress_count,
        mpv_null=args.mpv_null,
        run=run,
        selected_phases=args.phase,
        **source_identity,
    )
    try:
        report = runner.run_all()
        report_path = runner.run_directory / "report.json"
        write_json(report_path, report)
    except (OSError, ValidationError, ValueError) as error:
        print(f"error: validation runner could not save its report: {error}", file=stderr)
        return 2

    summary = {
        "decision": report["gate"]["decision"],
        "source_commit": report["source_commit"],
        "dirty_scope_sha256": report["dirty_scope_fingerprint"]["sha256"],
        "source_content_sha256": report["source_content_fingerprint"]["sha256"],
        "source_stability": report["source_stability"],
        "scoped_verification": report["scoped_verification"],
        "reasons": report["gate"]["reasons"],
        "report": str(report_path),
        "phases": {phase["id"]: phase["status"] for phase in report["phases"]},
    }
    print(json.dumps(summary, sort_keys=True), file=stdout)
    return 0 if report["gate"]["accepted"] else 1


if __name__ == "__main__":
    sys.exit(main())
