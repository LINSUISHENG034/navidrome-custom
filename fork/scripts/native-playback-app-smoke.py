#!/usr/bin/env python3
"""Run a real Navidrome API and browser smoke test in scratch state."""

import argparse
from datetime import datetime, timezone
import json
import hashlib
import importlib.util
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import threading
import tempfile
import time
import wave
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, quote
from urllib.request import Request, urlopen


REPO = Path(__file__).resolve().parents[2]
OUTPUT_ROOT = REPO / "tmp/native-playback-validation/app-smoke"
MPV_TEMPLATE = (
    "mpv --no-config --load-scripts=no --no-video --vo=null --ao=null "
    "--audio-fallback-to-null=no --input-ipc-server=%s %f"
)
LOCAL_DEVICE = "Validation Local Null"
REMOTE_DEVICE = "Validation Remote Null"
LOCAL_DEVICE_ID = "validation-null-local"
REMOTE_DEVICE_ID = "validation-null-remote"
BROWSER_DEVICE = "Validation Browser Local"
BROWSER_DEVICE_ID = "auto"


class SmokeFailure(RuntimeError):
    pass


def utc_now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def utc_now_precise():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds")


def write_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(temporary, path)


def classify_browser_console(details, expected_observers):
    expected, unexpected = [], []
    events = [event for event in details.get("playwright", []) if event.get("type") == "error"]
    forbidden_text = "Failed to load resource: the server responded with a status of 403 (Forbidden)"
    for event in events:
        source = event.get("location", {}).get("url")
        matches = [row for row in details.get("jukebox_http", [])
                   if row.get("method") == "POST" and row.get("status") == 403
                   and row.get("url") == source and row.get("page") == event.get("page")]
        same_events = [row for row in events if row.get("text") == forbidden_text
                       and row.get("location", {}).get("url") == source
                       and row.get("page") == event.get("page")]
        payloads = [row.get("body") or {} for row in matches]
        confirmed = (
            event.get("text") == forbidden_text and source
            and source.endswith("/api/jukebox/session/attach")
            and matches and len(matches) == len(same_events)
            and all((payload.get("sessionId"), payload.get("clientId")) in expected_observers
                    and not payload.get("takeover") for payload in payloads)
        )
        if confirmed:
            expected.append({"console": event, "matched_http_group": matches})
        else:
            unexpected.append(event)
    return {"expected_http_denials": expected, "unexpected_errors": unexpected,
            "page_errors": details.get("page_errors", [])}


def reserve_port_pair():
    for _ in range(100):
        first = socket.socket()
        second = socket.socket()
        try:
            first.bind(("127.0.0.1", 0))
            frontend = first.getsockname()[1]
            backend = frontend + 100
            if backend > 65535:
                continue
            second.bind(("127.0.0.1", backend))
            return frontend, backend
        except OSError:
            pass
        finally:
            first.close()
            second.close()
    raise SmokeFailure("could not reserve a free Vite/API port pair")


def make_fixture_music(music_folder):
    album = music_folder / "Validation Artist" / "Validation Album"
    album.mkdir(parents=True, exist_ok=True)
    tracks = [album / "01 - Smoke One.wav", album / "02 - Smoke Two.wav"]
    if any(path.exists() for path in tracks):
        raise SmokeFailure("refusing to overwrite an existing music fixture")

    sample_rate = 22050
    duration_seconds = 60
    silence = bytes(sample_rate * 2)
    for path in tracks:
        with wave.open(str(path), "wb") as output:
            output.setnchannels(1)
            output.setsampwidth(2)
            output.setframerate(sample_rate)
            for _ in range(duration_seconds):
                output.writeframesraw(silence)
    return tracks


def write_scratch_config(path):
    path.write_text(
        """[jukebox]
enabled = true
default = "Validation Remote Null"
devices = [["Validation Local Null", "validation-null-local"], ["Validation Remote Null", "validation-null-remote"], ["Validation Browser Local", "auto"]]
adminonly = true
autodiscoverbluetooth = false
bluetoothmanagement = false

[scanner]
scanonstartup = false

[deezer]
enabled = false

[lastfm]
enabled = false

[listenbrainz]
enabled = false
""",
        encoding="utf-8",
    )


def validation_module():
    script = Path(__file__).with_name("native-playback-validation.py")
    spec = importlib.util.spec_from_file_location("native_playback_validation", script)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def source_fingerprint():
    validation = validation_module()
    commit, provenance = validation.capture_source_commit(REPO, validation.isolated_run)
    content = validation.capture_source_content_fingerprint(REPO, validation.isolated_run)
    return {
        "commit": commit,
        "worktree_sha256": content["sha256"],
        "worktree_sha256_algorithm": content["algorithm"],
        "source_content_fingerprint": content,
        "provenance": provenance,
    }


class AppSmoke:
    def __init__(self, output, binary, chrome, mpv, trace_processes=False):
        self.output = output.resolve()
        self.binary = binary.resolve()
        self.chrome = chrome
        self.mpv = mpv.resolve()
        self.trace_processes = trace_processes
        self.trace_prefix = None
        self.transcript = []
        self.errors = []
        self.redactions = []
        self.redaction_lock = threading.Lock()
        self.server = None
        self.vite = None
        self.server_log = None
        self.server_log_thread = None
        self.vite_log = None
        self.username = "smoke_" + secrets.token_hex(5)
        self.password = secrets.token_urlsafe(24)
        self.token = None
        self.subsonic_token = None
        self.subsonic_salt = None
        self.session_id = "jukebox-session:" + self.username
        self.client_id = "api-smoke-" + secrets.token_hex(6)
        self.frontend_port, self.backend_port = reserve_port_pair()
        self.server_url = f"http://127.0.0.1:{self.backend_port}"
        self.vite_url = f"http://127.0.0.1:{self.frontend_port}"
        self.short_temp = Path(tempfile.mkdtemp(prefix="ndsm-", dir="/tmp"))
        self.mpv_pids = set()
        self.mpv_stop_observations = []
        self.summary = {
            "started_at": utc_now(),
            "repository": str(REPO),
            "source": source_fingerprint(),
            "output": str(self.output),
            "server_url": self.server_url,
            "vite_url": self.vite_url,
            "phases": {},
            "mpv_stop_observations": self.mpv_stop_observations,
            "browser": {
                "status": "not_run",
                "console_errors": [],
                "console_details": {"playwright": [], "cdp": [], "page_errors": []},
                "screenshots": [],
            },
        }

    def add_redaction(self, value):
        if value:
            with self.redaction_lock:
                self.redactions.append(str(value))

    def redact(self, value):
        text = str(value)
        with self.redaction_lock:
            values = tuple(self.redactions)
        for secret in values:
            text = text.replace(secret, "<redacted>")
        return re.sub(
            r"(?i)((?:jwt|token|password|subsonictoken|subsonicsalt)=)[^&\\s,\"']+",
            r"\1<redacted>", text,
        )

    def server_env(self):
        work = self.output / "work"
        home = work / "home"
        temp = work / "tmp"
        for path in (home, temp, self.output / "cache/xdg"):
            path.mkdir(parents=True, exist_ok=True)
        path_env = os.pathsep.join((str(self.mpv.parent), "/usr/bin", "/bin"))
        return {
            "PATH": path_env,
            "HOME": str(home),
            "TMPDIR": str(self.short_temp),
            "TEMP": str(self.short_temp),
            "TMP": str(self.short_temp),
            "XDG_CONFIG_HOME": str(home / ".config"),
            "XDG_CACHE_HOME": str(self.output / "cache/xdg"),
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "CI": "1",
            "NOLOG": "1",
            "ND_CONFIGFILE": str(self.output / "navidrome.toml"),
            "ND_ADDRESS": "127.0.0.1",
            "ND_PORT": str(self.backend_port),
            "ND_DATAFOLDER": str(self.output / "data"),
            "ND_CACHEFOLDER": str(self.output / "cache"),
            "ND_MUSICFOLDER": str(self.output / "music"),
            "ND_ENABLEINSIGHTSCOLLECTOR": "false",
            "ND_ENABLEEXTERNALSERVICES": "false",
            "ND_JUKEBOX_ENABLED": "true",
            "ND_JUKEBOX_AUTODISCOVERBLUETOOTH": "false",
            "ND_JUKEBOX_BLUETOOTHMANAGEMENT": "false",
            "ND_SCANNER_SCANONSTARTUP": "false",
            "ND_MPVCMDTEMPLATE": MPV_TEMPLATE,
            "ND_MPVPATH": str(self.mpv),
        }

    def start_server(self):
        for folder in ("data", "cache", "music", "work", "browser"):
            (self.output / folder).mkdir(parents=True, exist_ok=True)
        if (self.output / "data/navidrome.db").exists():
            raise SmokeFailure("scratch database already exists; refusing to reuse it")
        write_scratch_config(self.output / "navidrome.toml")
        tracks = make_fixture_music(self.output / "music")
        self.summary["fixture_tracks"] = [str(path) for path in tracks]
        self.server_log = (self.output / "server.log").open("w", encoding="utf-8")
        command = [str(self.binary)]
        if self.trace_processes:
            strace = shutil.which("strace", path="/usr/bin:/bin")
            if not strace:
                raise SmokeFailure("strace is unavailable for the requested MPV exit-status diagnosis")
            self.trace_prefix = self.output / "process.trace"
            command = [
                strace, "-ff", "-ttt", "-T", "-e", "trace=process",
                "-o", str(self.trace_prefix), str(self.binary),
            ]
            self.summary["process_trace_prefix"] = str(self.trace_prefix)
        self.server = subprocess.Popen(
            command, cwd=self.output / "work", env=self.server_env(),
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            start_new_session=True, text=True, bufsize=1,
        )
        self.server_log_thread = threading.Thread(target=self._drain_server_log, daemon=True)
        self.server_log_thread.start()
        self.summary["server_pid"] = self.server.pid
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            if self.server.poll() is not None:
                raise SmokeFailure(f"test server exited during startup (code {self.server.returncode})")
            try:
                code, body = self.request("GET", self.server_url + "/ping", "/ping")
                if code == 200:
                    self.summary["phases"]["startup"] = {"status": "pass", "http": code, "body": body}
                    return
            except (URLError, TimeoutError, OSError, SmokeFailure):
                pass
            time.sleep(0.25)
        raise SmokeFailure("test server did not become ready on /ping within 90 seconds")

    def _drain_server_log(self):
        if self.server is None or self.server.stdout is None:
            return
        for line in self.server.stdout:
            self.server_log.write(self.redact(line))
            self.server_log.flush()

    def request(self, method, url, route, *, body=None, token=None, timeout=20):
        headers = {"Accept": "application/json"}
        payload = None
        if body is not None:
            payload = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if token:
            headers["X-ND-Authorization"] = "Bearer " + token
        request = Request(url, data=payload, headers=headers, method=method)
        try:
            with urlopen(request, timeout=timeout) as response:
                status = response.status
                content = response.read()
        except HTTPError as error:
            status = error.code
            content = error.read()
        except URLError as error:
            raise SmokeFailure(f"{route} request failed: {type(error.reason).__name__}") from error

        text = content.decode("utf-8", errors="replace")
        try:
            parsed = json.loads(text) if text else None
        except json.JSONDecodeError:
            parsed = text[:4000]
        self.transcript.append({
            "method": method,
            "route": route,
            "status": status,
            "body": self.sanitize(parsed),
        })
        return status, parsed

    def sanitize(self, value):
        if isinstance(value, dict):
            return {
                key: ("<redacted>" if any(word in key.lower() for word in ("token", "password", "salt", "authorization"))
                      else self.sanitize(item))
                for key, item in value.items()
            }
        if isinstance(value, list):
            return [self.sanitize(item) for item in value]
        if isinstance(value, str):
            return self.redact(value)
        return value

    def require(self, status, expected, route):
        if status != expected:
            raise SmokeFailure(f"{route} returned HTTP {status}, expected {expected}")

    def native(self, method, path, *, body=None):
        return self.request(method, self.server_url + path, path, body=body, token=self.token)

    def command(self, path, body=None, *, client_id=None):
        payload = dict(body or {})
        if "sessionId" in payload or "clientId" in payload:
            raise SmokeFailure("command payload must not override controller scope")
        payload.update(sessionId=self.session_id, clientId=client_id or self.client_id)
        return self.native("POST", path, body=payload)

    def subsonic(self, path, *, parameters=None):
        params = {
            "u": self.username,
            "t": self.subsonic_token,
            "s": self.subsonic_salt,
            "v": "1.16.1",
            "c": "native-playback-app-smoke",
            "f": "json",
            **(parameters or {}),
        }
        url = self.server_url + "/rest/" + path + "?" + urlencode(params)
        return self.request("GET", url, "/rest/" + path)

    @staticmethod
    def subsonic_root(body):
        if not isinstance(body, dict):
            raise SmokeFailure("Subsonic response was not a JSON object")
        root = body.get("subsonic-response") or body.get("subsonicResponse")
        if not isinstance(root, dict) or root.get("status") != "ok":
            raise SmokeFailure("Subsonic response did not report status=ok")
        return root

    def create_admin_and_scan(self):
        code, auth = self.request(
            "POST", self.server_url + "/auth/createAdmin", "/auth/createAdmin",
            body={"username": self.username, "password": self.password},
        )
        self.require(code, 200, "/auth/createAdmin")
        if not isinstance(auth, dict) or not auth.get("token"):
            raise SmokeFailure("admin bootstrap returned no JWT")
        self.token = auth["token"]
        self.subsonic_token = auth.get("subsonicToken")
        self.subsonic_salt = auth.get("subsonicSalt")
        self.add_redaction(self.password)
        self.add_redaction(self.token)
        self.add_redaction(self.subsonic_token)
        self.add_redaction(self.subsonic_salt)
        self.summary["phases"]["admin-bootstrap"] = {"status": "pass", "username": self.username}

        code, started = self.subsonic("startScan")
        self.require(code, 200, "/rest/startScan")
        self.subsonic_root(started)
        deadline = time.monotonic() + 90
        scan_status = None
        while time.monotonic() < deadline:
            code, body = self.subsonic("getScanStatus")
            self.require(code, 200, "/rest/getScanStatus")
            root = self.subsonic_root(body)
            scan_status = root.get("scanStatus") or {}
            if not scan_status.get("scanning"):
                break
            time.sleep(0.25)
        if scan_status is None or scan_status.get("scanning"):
            raise SmokeFailure("library scan did not finish within 90 seconds")
        self.summary["phases"]["scan"] = {
            "status": "pass",
            "count": scan_status.get("count"),
            "folder_count": scan_status.get("folderCount"),
        }

    def media_ids(self):
        query = "?_start=0&_end=10&_sort=title&_order=ASC"
        code, body = self.native("GET", "/api/song/" + query)
        self.require(code, 200, "/api/song/")
        items = body if isinstance(body, list) else None
        if isinstance(body, dict):
            for key in ("data", "items", "results", "songs"):
                if isinstance(body.get(key), list):
                    items = body[key]
                    break
        if not items:
            params = {
                "query": "Smoke",
                "songCount": "10",
                "albumCount": "0",
                "artistCount": "0",
            }
            auth_params = {
                "u": self.username,
                "t": self.subsonic_token,
                "s": self.subsonic_salt,
                "v": "1.16.1",
                "c": "native-playback-app-smoke",
                "f": "json",
                **params,
            }
            code, search = self.request(
                "GET", self.server_url + "/rest/search3?" + urlencode(auth_params), "/rest/search3",
            )
            self.require(code, 200, "/rest/search3")
            root = self.subsonic_root(search)
            entries = (root.get("searchResult3") or {}).get("song") or []
            items = [{"id": item.get("id"), "title": item.get("title")} for item in entries]
        songs = [item for item in items if isinstance(item, dict) and item.get("id")]
        ids = list(dict.fromkeys(str(item["id"]) for item in songs))
        if len(ids) < 2:
            raise SmokeFailure(f"library API returned {len(ids)} distinct fixture song IDs; expected at least two")
        self.summary["phases"]["song-list"] = {
            "status": "pass",
            "count": len(songs),
            "fixture_ids": ids[:2],
        }
        return ids[:2]

    def jukebox_api(self, ids):
        code, devices = self.native("GET", "/api/jukebox/devices/")
        self.require(code, 200, "/api/jukebox/devices/")
        if not isinstance(devices, list) or len(devices) != 3:
            raise SmokeFailure("expected two scratch remote devices and one browser Local route")
        expected = {LOCAL_DEVICE_ID, REMOTE_DEVICE_ID, BROWSER_DEVICE_ID}
        if {item.get("deviceName") for item in devices} != expected:
            raise SmokeFailure("device list did not match the validation-only routes")
        if not any(item.get("isDefault") and item.get("deviceName") == REMOTE_DEVICE_ID for item in devices):
            raise SmokeFailure("scratch remote null output is not the configured default")
        self.summary["phases"]["devices"] = {"status": "pass", "devices": self.sanitize(devices)}

        code, attached = self.native("POST", "/api/jukebox/session/attach", body={
            "sessionId": self.session_id,
            "clientId": self.client_id,
        })
        self.require(code, 200, "/api/jukebox/session/attach")
        if attached.get("ownershipState") != "attached":
            raise SmokeFailure("session attach did not return attached ownership")

        code, set_status = self.command("/api/jukebox/set", {"ids": ids})
        self.require(code, 200, "/api/jukebox/set")
        code, gain_status = self.command("/api/jukebox/volume", {"gain": 0.375})
        self.require(code, 200, "/api/jukebox/volume")
        code, started = self.command("/api/jukebox/start")
        self.require(code, 200, "/api/jukebox/start")
        mpv = self.wait_for_mpv()
        if not all(flag in mpv["cmdline"] for flag in ("--no-config", "--load-scripts=no", "--no-video", "--vo=null", "--ao=null", "--audio-fallback-to-null=no")):
            raise SmokeFailure("active MPV command did not include the required null-only safety flags")

        code, status = self.native("GET", f"/api/jukebox/session/status?sessionId={quote(self.session_id)}")
        self.require(code, 200, "/api/jukebox/session/status")
        if not status.get("playing") or status.get("currentIndex") != 0 or status.get("gain") != 0.375:
            raise SmokeFailure("session status did not reflect playing/index/gain after start")
        code, snapshot = self.native("GET", f"/api/jukebox/session/snapshot?sessionId={quote(self.session_id)}")
        self.require(code, 200, "/api/jukebox/session/snapshot")
        playlist = snapshot.get("jukeboxPlaylist") or {}
        entries = playlist.get("entry") or []
        if [entry.get("id") for entry in entries] != ids:
            raise SmokeFailure("session snapshot queue did not match the selected fixture IDs")
        if playlist.get("currentIndex") != 0 or playlist.get("gain") != 0.375:
            raise SmokeFailure("session snapshot index/gain did not match current device state")

        code, paused = self.command("/api/jukebox/pause")
        self.require(code, 200, "/api/jukebox/pause")
        if paused.get("playing"):
            raise SmokeFailure("pause response still reports playing")
        code, seek = self.command("/api/jukebox/seek", {"position": 2})
        self.require(code, 200, "/api/jukebox/seek")
        if seek.get("position") != 2:
            raise SmokeFailure(f"seek response reported position {seek.get('position')}, expected 2")
        self.summary["phases"]["api-playback"] = {
            "status": "pass",
            "session_id": self.session_id,
            "queue_ids": ids,
            "index": status.get("currentIndex"),
            "gain": status.get("gain"),
            "mpv_pid": mpv["pid"],
            "mpv_safety_flags": ["--no-config", "--load-scripts=no", "--no-video", "--vo=null", "--ao=null", "--audio-fallback-to-null=no"],
        }
        self.mpv_pids.add(mpv["pid"])

        stopped = self.stop_jukebox_and_observe("api-playback", [mpv["pid"]])
        code, after = self.native("GET", f"/api/jukebox/session/snapshot?sessionId={quote(self.session_id)}")
        self.require(code, 200, "/api/jukebox/session/snapshot (after stop)")
        playlist_after = after.get("jukeboxPlaylist") or {}
        if playlist_after.get("playing") or playlist_after.get("currentIndex") != 0:
            raise SmokeFailure("stop did not leave the queue paused at index 0")
        if [entry.get("id") for entry in playlist_after.get("entry", [])] != ids:
            raise SmokeFailure("stop unexpectedly changed the session queue")
        if playlist_after.get("gain") != 0.375:
            raise SmokeFailure("stop unexpectedly changed the configured gain")
        self.summary["phases"]["stop-and-release"] = {
            "status": "pass",
            "playing": stopped.get("playing"),
            "queue_count": len(playlist_after.get("entry", [])),
            "index": playlist_after.get("currentIndex"),
            "gain": playlist_after.get("gain"),
            "mpv_pid_closed": True,
            "mpv_exit_observation": self.mpv_stop_observations[-1],
        }
        self.summary["fixture_ids"] = ids
        code, _ = self.native("POST", "/api/jukebox/session/detach", body={
            "sessionId": self.session_id,
            "clientId": self.client_id,
        })
        self.require(code, 200, "/api/jukebox/session/detach (API phase release)")

    def wait_for_playing(self, timeout=20):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            code, status = self.native("GET", "/api/jukebox/status")
            if code == 200 and status.get("playing"):
                return status
            time.sleep(0.25)
        raise SmokeFailure("server-side playback did not start within the configured timeout")

    def active_mpv_processes(self):
        fixture_root = str(self.output / "music") + os.sep
        active = {}
        for process in Path("/proc").iterdir():
            if not process.name.isdigit():
                continue
            pid = int(process.name)
            command = self.proc_cmdline(pid)
            if (
                command
                and Path(command[0]).name.startswith("mpv")
                and any(argument.startswith(fixture_root) for argument in command[1:])
            ):
                active[pid] = command
        return active

    def wait_for_mpv(self):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            processes = self.active_mpv_processes()
            if processes:
                pid, command = next(iter(processes.items()))
                self.mpv_pids.add(pid)
                return {"pid": pid, "cmdline": command}
            time.sleep(0.1)
        raise SmokeFailure("no MPV process for the generated fixture was observed")

    def child_pids(self):
        if self.server is None:
            return []
        path = Path(f"/proc/{self.server.pid}/task/{self.server.pid}/children")
        try:
            return [int(value) for value in path.read_text().split()]
        except (FileNotFoundError, PermissionError, ValueError):
            return []

    @staticmethod
    def proc_cmdline(pid):
        try:
            return [part for part in Path(f"/proc/{pid}/cmdline").read_bytes().decode(errors="replace").split("\0") if part]
        except (FileNotFoundError, PermissionError):
            return []

    @staticmethod
    def process_snapshot(pid):
        try:
            stat = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8")
        except (FileNotFoundError, PermissionError):
            return None
        fields = stat.rsplit(")", 1)[-1].split()
        if len(fields) < 20:
            return {"pid": pid, "state": fields[0] if fields else "unknown"}
        try:
            parent_pid = int(fields[1])
            start_ticks = int(fields[19])
        except ValueError:
            parent_pid, start_ticks = None, None
        return {
            "pid": pid,
            "state": fields[0],
            "parent_pid": parent_pid,
            "start_ticks": start_ticks,
            "cmdline": AppSmoke.proc_cmdline(pid),
        }

    def wait_for_process_exit(self, pid, timeout=10, baseline=None):
        started = time.monotonic()
        started_at = utc_now_precise()
        baseline = baseline if baseline is not None else self.process_snapshot(pid)
        baseline_ticks = baseline.get("start_ticks") if baseline else None
        last_state = baseline.get("state") if baseline else None
        state_changes = []
        deadline = started + timeout
        while time.monotonic() < deadline:
            current = self.process_snapshot(pid)
            if current is None or (
                baseline_ticks is not None and current.get("start_ticks") != baseline_ticks
            ):
                return {
                    "pid": pid,
                    "started_at": started_at,
                    "exit_observed_at": utc_now_precise(),
                    "elapsed_seconds": round(time.monotonic() - started, 4),
                    "result": "pid_absent_or_reused",
                    "last_state": last_state,
                    "state_changes": state_changes,
                }
            if current.get("state") != last_state:
                last_state = current.get("state")
                state_changes.append({"at": utc_now_precise(), "state": last_state})
            time.sleep(0.01)
        raise SmokeFailure(f"MPV child PID {pid} remained after Jukebox stop")

    def stop_jukebox_and_observe(self, phase, pids):
        pids = sorted(set(pids))
        before = {str(pid): self.process_snapshot(pid) for pid in pids}
        started_at = utc_now_precise()
        started = time.monotonic()
        code, response = self.command("/api/jukebox/stop")
        response_at = utc_now_precise()
        self.require(code, 200, f"/api/jukebox/stop ({phase})")
        exits = [
            self.wait_for_process_exit(pid, baseline=before[str(pid)])
            for pid in pids
        ]
        self.mpv_stop_observations.append({
            "phase": phase,
            "pids": pids,
            "before": before,
            "request_started_at": started_at,
            "response_received_at": response_at,
            "request_seconds": round(time.monotonic() - started, 4),
            "exit_observations": exits,
        })
        return response

    def start_vite(self):
        vite = REPO / "ui/node_modules/.bin/vite"
        if not vite.is_file():
            raise SmokeFailure("existing Vite executable is unavailable; no dependencies were installed")
        node = shutil.which("node")
        if not node:
            raise SmokeFailure("Node executable is unavailable; no dependencies were installed")
        env = {
            "PATH": os.pathsep.join((str(Path(node).parent), "/usr/bin", "/bin")),
            "HOME": str(self.output / "work/home"),
            "TMPDIR": str(self.output / "work/tmp"),
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "CI": "1",
            "NPM_CONFIG_OFFLINE": "true",
            "PORT": str(self.frontend_port),
            "NODE_OPTIONS": "--dns-result-order=ipv4first",
        }
        self.vite_log = (self.output / "vite.log").open("w", encoding="utf-8")
        self.vite = subprocess.Popen(
            [str(vite), "--host", "127.0.0.1", "--port", str(self.frontend_port), "--strictPort"],
            cwd=REPO / "ui", env=env, stdin=subprocess.DEVNULL,
            stdout=self.vite_log, stderr=subprocess.STDOUT, start_new_session=True,
        )
        self.summary["vite_pid"] = self.vite.pid
        deadline = time.monotonic() + 60
        fixture_url = self.vite_url + "/validation/remote-player.html"
        while time.monotonic() < deadline:
            if self.vite.poll() is not None:
                raise SmokeFailure(f"Vite exited during startup (code {self.vite.returncode})")
            try:
                code, body = self.request("GET", fixture_url, "/validation/remote-player.html")
                if code == 200 and "remote-player.jsx" in str(body):
                    self.summary["phases"]["validation-fixture"] = {"status": "pass", "path": "/validation/remote-player.html"}
                    return
            except (URLError, TimeoutError, OSError, SmokeFailure):
                pass
            time.sleep(0.25)
        raise SmokeFailure("Vite did not serve the independent validation fixture")

    def begin_browser_evidence(self):
        errors, checks = [], {}
        details = {"playwright": [], "cdp": [], "page_errors": [], "jukebox_http": []}
        self.summary["browser"].update({
            "status": "running", "functional_contract_status": "not_run",
            "console_errors": errors, "confirmed_observers": [],
            "checks": checks, "console_details": details,
        })
        return errors, checks, details

    def run_browser(self):
        try:
            from playwright.sync_api import sync_playwright
        except ImportError:
            self.summary["browser"] = {"status": "blocked", "reason": "Playwright Python package is unavailable"}
            return
        if not self.chrome:
            self.summary["browser"] = {"status": "blocked", "reason": "installed Chrome executable is unavailable"}
            return
        if not self.token:
            self.summary["browser"] = {"status": "blocked", "reason": "admin bootstrap did not produce a browser token"}
            return

        browser_dir = self.output / "browser"
        browser_dir.mkdir(exist_ok=True)
        browser_errors, browser_checks, browser_console_details = self.begin_browser_evidence()
        context = None
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(
                executable_path=self.chrome,
                headless=True,
                args=["--no-sandbox", "--disable-dev-shm-usage", "--disable-audio-output"],
            )
            context = browser.new_context(
                viewport={"width": 1440, "height": 960},
                service_workers="block",
            )
            app_page = context.new_page()
            self._track_page_errors(app_page, browser_errors, browser_console_details)
            try:
                app_page.goto(self.server_url + "/", wait_until="domcontentloaded", timeout=30000)
                app_page.locator('input[name="username"]').wait_for(timeout=20000)
                login_image = browser_dir / "default-app-login.png"
                app_page.screenshot(path=str(login_image), full_page=True)
                browser_checks["default_app_path"] = "/"
                browser_checks["login_screenshot"] = str(login_image)

                app_page.locator('input[name="username"]').fill(self.username)
                app_page.locator('input[name="password"]').fill(self.password)
                app_page.locator('button[type="submit"]').click()
                home_image = browser_dir / "default-app-home.png"
                app_page.screenshot(path=str(home_image), full_page=True)
                browser_checks["default_app_login"] = "pass"
                browser_checks["home_screenshot"] = str(home_image)
                browser_checks["browser_token_stored"] = bool(
                    app_page.evaluate("Boolean(localStorage.getItem('token'))")
                )

                app_page.goto(self.server_url + "/#/song", wait_until="domcontentloaded")
                song_text = app_page.get_by_text("Smoke One", exact=False).first
                song_text.wait_for(timeout=30000)
                song_text.click()

                selector = app_page.locator('[data-testid="device-selector-button"]')
                selector.wait_for(timeout=30000)
                selector.click()
                local_item = app_page.locator('[role="menuitem"]').filter(has_text=LOCAL_DEVICE).first
                local_item.wait_for(timeout=10000)
                with app_page.expect_response(
                    lambda response: response.request.method == "POST"
                    and response.url.split("?", 1)[0].endswith("/api/jukebox/devices/switch"),
                    timeout=15000,
                ) as switch_info:
                    local_item.click()
                switch_response = switch_info.value
                if switch_response.status != 200:
                    raise SmokeFailure(f"browser device switch returned HTTP {switch_response.status}")
                app_page.wait_for_timeout(500)
                audio_state = app_page.evaluate(
                    """() => {
                      const audio = document.querySelector('audio')
                      return audio ? {count: document.querySelectorAll('audio').length, muted: audio.muted, paused: audio.paused} : {count: 0}
                    }"""
                )
                if audio_state.get("count") and not audio_state.get("muted"):
                    raise SmokeFailure("browser audio element was not muted after entering Jukebox mode")
                remote_status = self.wait_for_playing()
                mpv = self.wait_for_mpv()
                if not all(flag in mpv["cmdline"] for flag in ("--no-config", "--load-scripts=no", "--no-video", "--vo=null", "--ao=null", "--audio-fallback-to-null=no")):
                    raise SmokeFailure("browser-started MPV did not use the required null-only safety flags")
                fixture_ids = self.summary.get("fixture_ids", [])
                if len(fixture_ids) > 1:
                    app_client_id = app_page.evaluate("sessionStorage.getItem('jukebox-client-id')")
                    code, added = self.command("/api/jukebox/add", {"ids": [fixture_ids[1]]}, client_id=app_client_id)
                    self.require(code, 200, "/api/jukebox/add (browser queue fixture)")
                browser_checks["default_app_device_switch"] = {
                    "status": 200,
                    "audio": audio_state,
                    "server_playing": True,
                    "mpv_pid": mpv["pid"],
                }
                switched_image = browser_dir / "default-app-jukebox.png"
                app_page.screenshot(path=str(switched_image), full_page=True)
                browser_checks["jukebox_screenshot"] = str(switched_image)

                selector.click()
                browser_local = app_page.locator('[role="menuitem"]').filter(has_text=BROWSER_DEVICE).first
                with app_page.expect_response(
                    lambda response: response.request.method == "POST"
                    and response.url.split("?", 1)[0].endswith("/api/jukebox/session/detach"),
                    timeout=15000,
                ) as detach_info:
                    browser_local.click()
                if detach_info.value.status != 200:
                    raise SmokeFailure("Local return did not release the controller session")
                app_page.wait_for_function(
                    "(() => { const a = document.querySelector('audio'); return a && !a.muted && !a.paused })()"
                )
                before_local = app_page.evaluate("document.querySelector('audio').currentTime")
                app_page.wait_for_function(
                    "previous => document.querySelector('audio').currentTime > previous + 0.2", arg=before_local,
                )
                if self.active_mpv_processes():
                    raise SmokeFailure("remote output survived explicit Local return")
                browser_checks["default_app_local_return"] = {
                    "session_detach_http": 200, "unmuted": True,
                    "browser_time_advances": True, "remote_mpv_released": True,
                }
            except Exception as error:
                browser_errors.append(self.redact(f"default app: {type(error).__name__}: {error}"))
                self.summary["browser"]["status"] = "fail"
                self.summary["browser"]["functional_contract_status"] = "fail"
                failure_image = browser_dir / "default-app-failure.png"
                app_page.screenshot(path=str(failure_image), full_page=True)
                browser_checks["default_app_failure_screenshot"] = str(failure_image)
                browser_checks["default_app_failure_state"] = app_page.evaluate(
                    """() => {
                      const audio = document.querySelector('audio')
                      return {controllerId: sessionStorage.getItem('jukebox-client-id'),
                        audio: audio ? {muted: audio.muted, paused: audio.paused, position: audio.currentTime} : null}
                    }"""
                )
                code, failed_snapshot = self.native(
                    "GET", f"/api/jukebox/session/snapshot?sessionId={quote(self.session_id)}",
                )
                browser_checks["default_app_failure_snapshot"] = {"status": code, "body": failed_snapshot}
            finally:
                app_page.close()

            for path, payload in (
                ("/api/jukebox/devices/switch", {"deviceName": LOCAL_DEVICE_ID}),
                ("/api/jukebox/set", {"ids": self.summary.get("fixture_ids", [])}),
                ("/api/jukebox/volume", {"gain": 0.375}),
            ):
                code, _ = self.native("POST", path, body=payload)
                self.require(code, 200, path + " (independent-controller setup)")
            self.client_id = "browser-controller-1"
            fixture_page = context.new_page()
            self._track_page_errors(fixture_page, browser_errors, browser_console_details)
            seed = json.dumps({
                "token": self.token,
                "username": self.username,
                "userId": str(auth_id(self.transcript)),
                "name": self.username,
                "subsonicSalt": self.subsonic_salt,
                "subsonicToken": self.subsonic_token,
            }, separators=(",", ":"))
            seed_script = (
                "(() => { const auth = " + seed + "; "
                "localStorage.setItem('token', auth.token); "
                "localStorage.setItem('username', auth.username); "
                "localStorage.setItem('name', auth.name); "
                "localStorage.setItem('userId', auth.userId); "
                "localStorage.setItem('role', 'admin'); "
                "localStorage.setItem('subsonic-salt', auth.subsonicSalt); "
                "localStorage.setItem('subsonic-token', auth.subsonicToken); "
                "localStorage.setItem('is-authenticated', 'true'); })()"
            )
            fixture_page.add_init_script(seed_script)

            def open_controller(client_id, session_id=None):
                page = context.new_page()
                self._track_page_errors(page, browser_errors, browser_console_details)
                page.add_init_script(seed_script)
                parameters = {"sessionId": session_id or self.session_id}
                if client_id is not None:
                    parameters["clientId"] = client_id
                page.goto(
                    self.vite_url + "/validation/remote-player.html?" + urlencode(parameters),
                    wait_until="domcontentloaded", timeout=30000,
                )
                page.get_by_role("heading", name="Remote player", exact=True).wait_for(timeout=30000)
                page.wait_for_function("document.querySelectorAll('audio').length === 0")
                return page

            try:
                fixture_url = (
                    self.vite_url + "/validation/remote-player.html?"
                    + urlencode({"sessionId": self.session_id, "clientId": "browser-controller-1"})
                )
                fixture_page.goto(fixture_url, wait_until="domcontentloaded", timeout=30000)
                fixture_page.get_by_role("heading", name="Remote player", exact=True).wait_for(timeout=30000)
                fixture_page.locator("fieldset").wait_for(timeout=10000)
                fixture_page.wait_for_function("document.querySelectorAll('audio').length === 0")
                initial = browser_dir / "remote-controller-initial.png"
                fixture_page.screenshot(path=str(initial), full_page=True)
                browser_checks["prototype_entry"] = "/validation/remote-player.html"
                browser_checks["prototype_audio_elements"] = 0
                browser_checks["prototype_initial_screenshot"] = str(initial)

                pause = fixture_page.get_by_role("button", name="Pause")
                if pause.count():
                    pause.click()
                    fixture_page.get_by_text("Paused", exact=True).wait_for(timeout=15000)
                play = fixture_page.get_by_role("button", name="Play")
                play.wait_for(timeout=15000)
                play.click()
                fixture_page.get_by_text("Playing", exact=True).wait_for(timeout=15000)
                self.wait_for_mpv()
                playing_image = browser_dir / "remote-controller-playing.png"
                fixture_page.screenshot(path=str(playing_image), full_page=True)
                browser_checks["prototype_user_play"] = "pass"
                browser_checks["prototype_playing_screenshot"] = str(playing_image)

                code, changed_gain = self.command("/api/jukebox/volume", {"gain": 0.375})
                self.require(code, 200, "/api/jukebox/volume (SSE probe)")
                fixture_page.get_by_text("Gain: 0.375", exact=True).wait_for(timeout=15000)
                browser_checks["sse_snapshot_refresh"] = "pass"

                observer_page = open_controller("browser-controller-2")
                observer_page.wait_for_function("document.querySelector('fieldset')?.disabled === true")
                code, observer_heartbeat = self.native("POST", "/api/jukebox/session/heartbeat", body={
                    "sessionId": self.session_id, "clientId": "browser-controller-2",
                })
                self.require(code, 403, "/api/jukebox/session/heartbeat (observer)")
                code, observer_mutation = self.command(
                    "/api/jukebox/volume", {"gain": 0.25}, client_id="browser-controller-2",
                )
                self.require(code, 403, "/api/jukebox/volume (observer attack)")
                code, _ = self.native("POST", "/api/jukebox/volume", body={"gain": 0.25})
                self.require(code, 403, "/api/jukebox/volume (unscoped attack)")
                code, subsonic_attack = self.subsonic("jukeboxControl", parameters={
                    "action": "setGain", "gain": "0.25", "sessionId": self.session_id,
                    "clientId": "browser-controller-2",
                })
                self.require(code, 200, "/rest/jukeboxControl (observer protocol response)")
                attack_root = subsonic_attack.get("subsonic-response") or subsonic_attack.get("subsonicResponse") or {}
                if attack_root.get("status") != "failed":
                    raise SmokeFailure("Subsonic observer mutation bypassed ownership")
                code, observer_snapshot = self.native(
                    "GET", f"/api/jukebox/session/snapshot?sessionId={quote(self.session_id)}",
                )
                self.require(code, 200, "/api/jukebox/session/snapshot (read-only observer)")
                before_takeover = observer_snapshot.get("sessionStatus", {})
                if before_takeover.get("ownerClientId") != "browser-controller-1" or before_takeover.get("gain") != 0.375:
                    raise SmokeFailure("observer attempts changed ownership or gain")
                self.summary["browser"]["confirmed_observers"].append({
                    "session_id": self.session_id, "client_id": "browser-controller-2",
                })
                browser_checks["controller2_read_only_probe"] = {
                    "snapshot_http": 200, "heartbeat_http": 403, "mutation_http": 403,
                    "unscoped_mutation_http": 403, "subsonic_status": "failed",
                    "owner_unchanged": True, "actual_second_page": True,
                }

                observer_page.get_by_role("button", name="Take control", exact=True).click()
                observer_page.wait_for_function("document.querySelector('fieldset')?.disabled === false")
                code, takeover_snapshot = self.native(
                    "GET", f"/api/jukebox/session/snapshot?sessionId={quote(self.session_id)}",
                )
                self.require(code, 200, "/api/jukebox/session/snapshot (explicit takeover)")
                takeover_status = takeover_snapshot.get("sessionStatus", {})
                if takeover_status.get("ownerClientId") != "browser-controller-2":
                    raise SmokeFailure("explicit Take control did not change owner")
                if (takeover_snapshot.get("jukeboxPlaylist", {}).get("entry") != observer_snapshot.get("jukeboxPlaylist", {}).get("entry")
                        or takeover_status.get("currentIndex") != before_takeover.get("currentIndex")
                        or takeover_status.get("position", 0) < before_takeover.get("position", 0) - 1):
                    raise SmokeFailure("explicit takeover reset queue/index/position")
                code, _ = self.command("/api/jukebox/pause", client_id="browser-controller-1")
                self.require(code, 403, "/api/jukebox/pause (retired owner attack)")
                self.client_id = "browser-controller-2"
                browser_checks["explicit_takeover"] = {
                    "owner": self.client_id, "old_owner_mutation_http": 403,
                    "queue_preserved": True, "position_not_rewound": True,
                }
                fixture_page.close()
                observer_page.close()
                session_code, detached_status = self.native(
                    "GET", f"/api/jukebox/session/status?sessionId={quote(self.session_id)}"
                )
                if session_code not in (200, 404):
                    raise SmokeFailure(f"closed controller session returned unexpected HTTP {session_code}")
                browser_checks["session_status_after_close_http"] = session_code
                code, after_close = self.native("GET", "/api/jukebox/status")
                self.require(code, 200, "/api/jukebox/status (after page close)")
                if not after_close.get("playing"):
                    raise SmokeFailure("server playback stopped when the browser controller closed")
                active_pids = list(self.active_mpv_processes())
                if not active_pids:
                    raise SmokeFailure("MPV null-output process did not survive browser page close")
                browser_checks["page_close_preserves_remote_playback"] = {
                    "playing": True,
                    "mpv_pids": active_pids,
                    "session_status_http": session_code,
                    "session_exists_after_close": session_code == 200,
                }

                restored_page = open_controller(self.client_id)
                restored_page.get_by_text("Playing", exact=True).wait_for(timeout=15000)
                restored_page.get_by_text("Smoke One", exact=False).first.wait_for(timeout=10000)
                restored_page.wait_for_function("document.querySelectorAll('audio').length === 0")
                restored_image = browser_dir / "remote-controller-restored.png"
                restored_page.screenshot(path=str(restored_image), full_page=True)
                browser_checks["reopen_restores_snapshot"] = "pass"
                browser_checks["restored_screenshot"] = str(restored_image)
                restored_page.close()

                self.client_id = "final-verifier"
                code, final_attach = self.native("POST", "/api/jukebox/session/attach", body={
                    "sessionId": self.session_id,
                    "clientId": self.client_id,
                    "takeover": True,
                })
                self.require(code, 200, "/api/jukebox/session/attach (explicit final verifier)")
                final_stop = self.stop_jukebox_and_observe("browser-final-cleanup", active_pids)
                code, final_snapshot = self.native("GET", f"/api/jukebox/session/snapshot?sessionId={quote(self.session_id)}")
                self.require(code, 200, "/api/jukebox/session/snapshot (final verifier)")
                final_playlist = final_snapshot.get("jukeboxPlaylist") or {}
                if final_playlist.get("playing") or final_playlist.get("currentIndex") != 0:
                    raise SmokeFailure("final teardown did not preserve paused index 0")
                if len(final_playlist.get("entry", [])) != 2 or final_playlist.get("gain") != 0.375:
                    raise SmokeFailure("final teardown did not preserve the two-item queue and gain")
                browser_checks["final_stop_release"] = {
                    "playing": final_stop.get("playing"),
                    "queue_count": len(final_playlist.get("entry", [])),
                    "index": final_playlist.get("currentIndex"),
                    "gain": final_playlist.get("gain"),
                    "mpv_pids_closed": True,
                    "mpv_exit_observations": self.mpv_stop_observations[-1]["exit_observations"],
                }
                code, _ = self.native("POST", "/api/jukebox/session/detach", body={
                    "sessionId": self.session_id,
                    "clientId": "final-verifier",
                })
                self.require(code, 200, "/api/jukebox/session/detach (final verifier)")

                clone_session = self.session_id + "-opener-clone"
                clone_requests = []

                def record_clone_attachment(request):
                    if (request.method == "POST"
                            and request.url.split("?", 1)[0].endswith("/api/jukebox/session/attach")):
                        payload = request.post_data_json or {}
                        if payload.get("sessionId") == clone_session:
                            clone_requests.append((request.frame.page, payload))

                context.on("request", record_clone_attachment)
                clone_parent = clone_child = None
                try:
                    clone_parent = open_controller(None, session_id=clone_session)
                    clone_parent.wait_for_function("document.querySelector('fieldset')?.disabled === false")
                    clone_url = self.vite_url + "/validation/remote-player.html?" + urlencode({"sessionId": clone_session})
                    with context.expect_page() as popup_info:
                        clone_parent.evaluate("url => window.open(url, '_blank')", clone_url)
                    clone_child = popup_info.value
                    self._track_page_errors(clone_child, browser_errors, browser_console_details)
                    clone_child.get_by_role("heading", name="Remote player", exact=True).wait_for(timeout=30000)
                    clone_child.wait_for_function("Boolean(document.querySelector('fieldset'))")
                    clone_child.wait_for_timeout(500)
                    parent_ids = [payload.get("clientId") for page, payload in clone_requests if page == clone_parent]
                    child_ids = [payload.get("clientId") for page, payload in clone_requests if page == clone_child]
                    parent_identity = parent_ids[-1] if parent_ids else None
                    child_identity = child_ids[-1] if child_ids else None
                    child_readonly = clone_child.locator("fieldset").evaluate(
                        """element => element.disabled &&
                          Array.from(element.querySelectorAll('button,input,select,textarea'))
                            .every(control => control.matches(':disabled'))"""
                    )
                    clone_image = browser_dir / "opener-cloned-controller.png"
                    clone_child.screenshot(path=str(clone_image), full_page=True)
                    browser_checks["opener_cloned_identity"] = {
                        "parent_controller_id": parent_identity,
                        "child_controller_id": child_identity,
                        "parent_session_storage_id": clone_parent.evaluate("sessionStorage.getItem('jukebox-client-id')"),
                        "child_session_storage_id": clone_child.evaluate("sessionStorage.getItem('jukebox-client-id')"),
                        "child_readonly": child_readonly,
                        "child_has_real_opener": clone_child.evaluate("Boolean(window.opener)"),
                        "screenshot": str(clone_image),
                        "attach_requests": [payload for _, payload in clone_requests],
                    }
                    if not parent_identity or not child_identity or parent_identity == child_identity or not child_readonly:
                        raise SmokeFailure("opener-cloned tab did not receive a distinct read-only controller identity")
                    self.summary["browser"]["confirmed_observers"].append({
                        "session_id": clone_session, "client_id": child_identity,
                    })
                finally:
                    context.remove_listener("request", record_clone_attachment)
                    for page in (clone_child, clone_parent):
                        if page and not page.is_closed():
                            page.close()
                    code, _ = self.native("POST", "/api/jukebox/session/attach", body={
                        "sessionId": clone_session, "clientId": "clone-cleanup", "takeover": True,
                    })
                    self.require(code, 200, "/api/jukebox/session/attach (clone cleanup)")
                    code, _ = self.native("POST", "/api/jukebox/session/detach", body={
                        "sessionId": clone_session, "clientId": "clone-cleanup",
                    })
                    self.require(code, 200, "/api/jukebox/session/detach (clone cleanup)")

                # Level 3 Issue 10: Browser Output Readiness and Real Media Failure Handling
                level3_page = context.new_page()
                self._track_page_errors(level3_page, browser_errors, browser_console_details)
                try:
                    level3_url = self.vite_url + "/validation/remote-player.html?" + urlencode({
                        "sessionId": self.session_id,
                        "clientId": "browser-level3-verifier",
                    })
                    level3_page.goto(level3_url, wait_until="domcontentloaded", timeout=30000)
                    level3_page.wait_for_function("Boolean(document.querySelector('fieldset'))")

                    level3_result = level3_page.evaluate("""async () => {
                        const { createPlaybackControlModule, PlaybackStatus } = await import('/src/audioplayer/playbackControlModule.js')
                        const { createBrowserAudioAdapter } = await import('/src/audioplayer/browserAudioAdapter.js')
                        const { createMockRemoteAudioAdapter } = await import('/src/audioplayer/mockRemoteAudioAdapter.js')

                        // Contract 1: Complete pending-target-to-mounted-element path & absence of detached playback
                        const controller = createPlaybackControlModule()
                        const remoteAdapter = createMockRemoteAudioAdapter()
                        controller.setAdapter(remoteAdapter)

                        const unattachedBrowser = createBrowserAudioAdapter({ unattached: true })
                        let publishedPending = null
                        controller.subscribe((s) => {
                            if (s.pendingOutputTarget) publishedPending = s.pendingOutputTarget
                        })

                        const migrationPromise = controller.migrateOutput({
                            targetAdapter: unattachedBrowser,
                            targetDevice: { name: 'Browser Output', type: 'browser' },
                            readinessTimeoutMs: 2000,
                        })

                        if (!publishedPending || publishedPending.name !== 'Browser Output') {
                            throw new Error('pendingOutputTarget was not published before waiting for readiness')
                        }

                        if (unattachedBrowser.getAudioElement() !== null) {
                            throw new Error('Detached audio element was created early')
                        }

                        // Host mounts real element in DOM
                        const realEl = document.createElement('audio')
                        realEl.id = 'real-level3-audio'
                        document.body.appendChild(realEl)
                        unattachedBrowser.attachElement(realEl)

                        await migrationPromise
                        if (controller.getState().pendingOutputTarget !== null) {
                            throw new Error('pendingOutputTarget was not cleared upon completion')
                        }
                        if (unattachedBrowser.getAudioElement() !== realEl) {
                            throw new Error('Target adapter is not using the real mounted DOM element')
                        }

                        // Contract 2: Late attachment cannot clear, rewind or replace context
                        const lateController = createPlaybackControlModule()
                        const lateAdapter = createBrowserAudioAdapter({ unattached: true })
                        lateController.setAdapter(lateAdapter)
                        await lateController.addQueueItems([
                            { queueItemId: 'track-A', musicSrc: 'data:audio/wav;base64,UklGRiQAAABXQVZFZm10IBAAAAABAAEARKwAAIhYAQACABAAZGF0YQAAAAA=' },
                            { queueItemId: 'track-B', musicSrc: 'data:audio/wav;base64,UklGRiQAAABXQVZFZm10IBAAAAABAAEARKwAAIhYAQACABAAZGF0YQAAAAA=' },
                        ])
                        await lateController.skipToQueueItem('track-B')
                        await lateController.seek(18.5)

                        const lateEl = document.createElement('audio')
                        document.body.appendChild(lateEl)
                        lateAdapter.attachElement(lateEl)

                        const stateAfterLate = lateController.getState()
                        if (stateAfterLate.currentQueueItemId !== 'track-B') {
                            throw new Error('Late attachment replaced currentQueueItemId')
                        }
                        if (stateAfterLate.position !== 18.5) {
                            throw new Error('Late attachment rewound position')
                        }
                        if (lateEl.currentTime !== 18.5) {
                            throw new Error('Late attachment element currentTime was not synchronized to migrated position')
                        }

                        // Contract 3: Media error propagation
                        const errorController = createPlaybackControlModule()
                        const errorAudio = document.createElement('audio')
                        document.body.appendChild(errorAudio)
                        const errorAdapter = createBrowserAudioAdapter({ audioElement: errorAudio })
                        errorController.setAdapter(errorAdapter)
                        await errorController.addQueueItems([
                            { queueItemId: 'err-1', musicSrc: '/api/invalid-track-not-found-404.mp3' }
                        ])

                        const errorPlay = errorController.play().catch(e => e)
                        Object.defineProperty(errorAudio, 'error', {
                            configurable: true,
                            value: { code: 4, message: 'Source not supported / 404' }
                        })
                        errorAudio.dispatchEvent(new Event('error'))

                        const errorState = errorController.getState()
                        if (errorState.status !== PlaybackStatus.ERROR) {
                            throw new Error('Media error did not enter PlaybackStatus.ERROR')
                        }

                        // Contract 4: Autoplay rejection and explicit user recovery
                        const recoverAudio = document.createElement('audio')
                        document.body.appendChild(recoverAudio)
                        const recoverAdapter = createBrowserAudioAdapter({ audioElement: recoverAudio })
                        const recoverController = createPlaybackControlModule()
                        recoverController.setAdapter(recoverAdapter)
                        await recoverController.addQueueItems([
                            { queueItemId: 'rec-1', musicSrc: 'data:audio/wav;base64,UklGRiQAAABXQVZFZm10IBAAAAABAAEARKwAAIhYAQACABAAZGF0YQAAAAA=' }
                        ])

                        const origPlay = recoverAudio.play.bind(recoverAudio)
                        recoverAudio.play = () => Promise.reject(new DOMException('Autoplay blocked in Chrome', 'NotAllowedError'))

                        const failedPlay = await recoverController.play().catch(e => e)
                        if (recoverController.getState().status !== PlaybackStatus.ERROR) {
                            throw new Error('Autoplay rejection fabricated PLAYING instead of ERROR')
                        }
                        if (recoverAudio.paused !== true) {
                            throw new Error('Autoplay rejection did not maintain paused state')
                        }

                        // User explicit recovery
                        recoverAudio.play = origPlay
                        await recoverController.play()
                        if (recoverController.getState().status !== PlaybackStatus.PLAYING) {
                            throw new Error('Explicit user play recovery failed to achieve PLAYING')
                        }
                        if (recoverController.getState().error !== null) {
                            throw new Error('Explicit user play recovery failed to clear error')
                        }

                        realEl.remove()
                        lateEl.remove()
                        errorAudio.remove()
                        recoverAudio.remove()

                        return {
                            pendingTargetSatisfied: true,
                            realElementMounted: true,
                            absenceOfDetachedPlayback: true,
                            lateAttachmentPreservedContext: true,
                            mediaErrorPropagated: true,
                            autoplayRejectionHandled: true,
                            userExplicitRecoverySucceeded: true,
                        }
                    }""")
                    level3_screenshot = browser_dir / "issue10-level3-verification.png"
                    level3_page.screenshot(path=str(level3_screenshot), full_page=True)
                    browser_checks["issue10_level3_contracts"] = {
                        "status": "pass",
                        "details": level3_result,
                        "screenshot": str(level3_screenshot),
                    }
                finally:
                    level3_page.close()

                required_checks = {
                    "default_app_login", "default_app_device_switch", "default_app_local_return",
                    "prototype_audio_elements", "prototype_user_play", "sse_snapshot_refresh",
                    "controller2_read_only_probe", "explicit_takeover",
                    "page_close_preserves_remote_playback", "reopen_restores_snapshot",
                    "final_stop_release", "opener_cloned_identity",
                    "issue10_level3_contracts",
                }
                if required_checks.difference(browser_checks):
                    raise SmokeFailure("not all real browser functional contracts completed")
                self.summary["browser"]["functional_contract_status"] = "pass"
                if not browser_errors:
                    self.summary["browser"]["status"] = "pass"
                else:
                    self.summary["browser"]["status"] = "pass_with_console_errors"
            except Exception as error:
                browser_errors.append(self.redact(f"browser flow: {type(error).__name__}: {error}"))
                self.summary["browser"]["status"] = "fail"
                self.summary["browser"]["functional_contract_status"] = "fail"
            finally:
                self.summary["browser"]["console_errors"] = browser_errors
                self.summary["browser"]["console_details"] = browser_console_details
                self.summary["browser"]["checks"] = browser_checks
                self.summary["browser"]["screenshots"] = [value for key, value in browser_checks.items() if key.endswith("screenshot") and isinstance(value, str)]
                context.close()
                browser.close()

    def _track_page_errors(self, page, errors, details):
        def page_url():
            return self.redact(page.url.split("?", 1)[0])

        def argument_text(argument):
            if argument.get("type") == "undefined":
                return "undefined"
            if argument.get("subtype") == "null":
                return "null"
            if "value" in argument:
                value = argument["value"]
                if isinstance(value, str):
                    return value
                return json.dumps(value, sort_keys=True, ensure_ascii=False)
            return argument.get("description") or argument.get("type", "unknown")

        def record_jukebox_response(response):
            request = response.request
            route = response.url.split("?", 1)[0]
            if "/api/jukebox/" not in route and not route.endswith("/api/jukebox/status"):
                return
            payload = None
            if request.method == "POST":
                try:
                    payload = request.post_data_json
                except Exception:
                    payload = {"parse_error": True}
            details["jukebox_http"].append({
                "at": utc_now_precise(), "page": page_url(),
                "method": request.method, "url": self.redact(response.url),
                "status": response.status, "body": self.sanitize(payload),
            })

        page.on("response", record_jukebox_response)
        cdp = page.context.new_cdp_session(page)
        cdp.send("Runtime.enable")

        def record_cdp_console(event):
            event_type = event.get("type", "")
            if event_type not in ("error", "warning"):
                return
            arguments = [
                {
                    "type": argument.get("type", "unknown"),
                    "subtype": argument.get("subtype"),
                    "text": self.redact(argument_text(argument)),
                }
                for argument in event.get("args", [])
            ]
            text = " ".join(argument["text"] for argument in arguments)
            trace = event.get("stackTrace") or {}
            frames = []
            depth = 0
            while trace and depth < 8 and len(frames) < 40:
                for frame in trace.get("callFrames", []):
                    frames.append({
                        "function": self.redact(str(frame.get("functionName", ""))),
                        "url": self.redact(str(frame.get("url", ""))),
                        "line": frame.get("lineNumber"),
                        "column": frame.get("columnNumber"),
                    })
                    if len(frames) >= 40:
                        break
                trace = trace.get("parent") or {}
                depth += 1
            details["cdp"].append({
                "type": event_type,
                "text": self.redact(text),
                "page": page_url(),
                "location": frames[0] if frames else None,
                "stack": frames,
                "arguments": arguments,
            })

        cdp.on("Runtime.consoleAPICalled", record_cdp_console)

        def record_page_error(error):
            text = self.redact(str(error))
            stack = getattr(error, "stack", None) or str(error)
            details["page_errors"].append({
                "type": "pageerror",
                "text": text,
                "page": page_url(),
                "stack": self.redact(str(stack)),
            })
            errors.append(self.redact("pageerror: " + text))

        def record_console_message(message):
            if message.type not in ("error", "warning"):
                return
            location = message.location or {}
            details["playwright"].append({
                "type": message.type,
                "text": self.redact(message.text),
                "page": page_url(),
                "location": {
                    "url": self.redact(str(location.get("url", ""))),
                    "line": location.get("lineNumber"),
                    "column": location.get("columnNumber"),
                },
            })
            if message.type == "error":
                errors.append(self.redact("console: " + message.text))

        page.on("pageerror", record_page_error)
        page.on("console", record_console_message)

        def record_request_failure(request):
            route = request.url.split("?", 1)[0]
            failure = request.failure or ""
            if "ERR_ABORTED" in failure:
                return
            if route.endswith("/api/events") and not failure:
                return
            errors.append(self.redact(f"request failed: {route} ({failure})"))

        page.on("requestfailed", record_request_failure)

    def process_trace_summary(self):
        if self.trace_prefix is None:
            return {"enabled": False}
        traces = sorted(self.output.glob(self.trace_prefix.name + ".*"))
        mpv_processes = []
        wait_calls = []
        for path in traces:
            suffix = path.name.rsplit(".", 1)[-1]
            try:
                traced_pid = int(suffix)
            except ValueError:
                continue
            try:
                lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
            except OSError:
                continue
            if traced_pid in self.mpv_pids:
                events = [
                    line.strip()
                    for line in lines
                    if any(token in line for token in (
                        "execve(", "exit_group(", "+++ exited", "+++ killed",
                    ))
                ]
                mpv_processes.append({
                    "pid": traced_pid,
                    "trace_file": str(path),
                    "lifecycle_events": events,
                })
            for line in lines:
                if not any(token in line for token in ("wait4(", "waitid(", "waitpid(")):
                    continue
                if any(
                    f"si_pid={pid}," in line
                    or f"wait4({pid}," in line
                    or f"waitpid({pid}," in line
                    or f"waitid(P_PID, {pid}," in line
                    for pid in self.mpv_pids
                ):
                    wait_calls.append({"traced_parent_pid": traced_pid, "line": line.strip()})
        return {
            "enabled": True,
            "trace_prefix": str(self.trace_prefix),
            "mpv_processes": mpv_processes,
            "wait_calls": wait_calls,
        }

    def cleanup(self):
        for process in (self.vite, self.server):
            if process is None:
                continue
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=5)
        if self.server_log_thread:
            self.server_log_thread.join(timeout=2)
        for name in ("vite_log", "server_log"):
            stream = getattr(self, name)
            if stream:
                stream.close()
        shutil.rmtree(self.short_temp, ignore_errors=True)
        self.summary["finished_at"] = utc_now()
        self.summary["server_reaped"] = self.server is None or self.server.poll() is not None
        self.summary["vite_reaped"] = self.vite is None or self.vite.poll() is not None
        self.summary["mpv_pids_after_cleanup"] = {
            str(pid): self.process_snapshot(pid) for pid in sorted(self.mpv_pids)
        }
        self.summary["process_trace"] = self.process_trace_summary()
        browser = self.summary["browser"]
        browser["screenshots"] = [str(path) for path in sorted((self.output / "browser").glob("*.png"))]
        expected_observers = {(item["session_id"], item["client_id"]) for item in browser.get("confirmed_observers", [])}
        browser["console_classification"] = classify_browser_console(browser.get("console_details", {}), expected_observers)
        write_json(self.output / "api-transcript.json", self.transcript)
        write_json(self.output / "report.json", self.summary)

    def run(self):
        try:
            self.start_server()
            self.create_admin_and_scan()
            ids = self.media_ids()
            self.jukebox_api(ids)
            self.start_vite()
            self.run_browser()
        except Exception as error:
            self.errors.append(self.redact(f"{type(error).__name__}: {error}"))
            self.summary["status"] = "fail"
            if self.summary["browser"].get("status") in {"running", "fail"}:
                self.summary["browser"]["status"] = "fail"
                self.summary["browser"]["functional_contract_status"] = "fail"
        else:
            self.summary["status"] = (
                "pass" if self.summary["browser"].get("status") == "pass" else self.summary["browser"].get("status", "not_run")
            )
        finally:
            self.cleanup()
        self.summary["errors"] = self.errors
        write_json(self.output / "report.json", self.summary)
        return 0 if self.summary.get("status") == "pass" else 1


def auth_id(transcript):
    for item in transcript:
        if item.get("route") == "/auth/createAdmin":
            body = item.get("body") or {}
            return body.get("id", "validation-user")
    return "validation-user"


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="isolated Navidrome binary built with netgo,sqlite_fts5")
    parser.add_argument("--output", required=True, help="new run directory under tmp/native-playback-validation/app-smoke")
    parser.add_argument("--chrome", default=shutil.which("google-chrome"), help="installed Chrome executable")
    parser.add_argument("--mpv", default=shutil.which("mpv"), help="installed MPV executable")
    parser.add_argument("--trace-processes", action="store_true",
                        help="trace child process exit status with the existing strace utility")
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv)
    output = Path(args.output).expanduser().resolve()
    expected_root = OUTPUT_ROOT.resolve()
    if output == expected_root or not output.is_relative_to(expected_root):
        print("error: output must be a child of tmp/native-playback-validation/app-smoke", file=sys.stderr)
        return 2
    if not output.is_dir():
        print("error: output directory must already exist", file=sys.stderr)
        return 2
    binary = Path(args.binary).expanduser().resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        print("error: Navidrome binary is not executable", file=sys.stderr)
        return 2
    if not args.mpv:
        print("error: MPV executable is unavailable", file=sys.stderr)
        return 2
    runner = AppSmoke(output, binary, args.chrome, Path(args.mpv), args.trace_processes)
    result = runner.run()
    print(json.dumps({
        "status": runner.summary.get("status"),
        "source": runner.summary.get("source"),
        "report": str(output / "report.json"),
        "screenshots": runner.summary.get("browser", {}).get("screenshots", []),
        "errors": runner.errors,
    }, sort_keys=True))
    return result


if __name__ == "__main__":
    sys.exit(main())

