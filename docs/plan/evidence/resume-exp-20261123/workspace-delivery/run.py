"""Run a synthetic fault campaign against unmodified coding-web executable entry points."""
import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
from datetime import datetime, timezone
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import os
import platform
import secrets
import socket
import subprocess
import tempfile
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.request import build_opener, ProxyHandler, Request

HERE = Path(__file__).parent
HTTP = build_opener(ProxyHandler({}))


def digest(parts):
    return hashlib.sha256(b"".join(p.encode() + b"\0" for p in parts)).hexdigest()


class WorkspaceService(BaseHTTPRequestHandler):
    """The fixture durably accepts a single mutation per request ID for this trial."""
    def log_message(self, *args):
        pass

    def do_POST(self):
        state = self.server
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        method = self.path.rsplit("/", 1)[-1]
        status = 200
        if method == "StartWorkspaceOperation":
            op = "wso-" + digest(("acme", "alice", body["requestId"]))
            with state.lock:
                state.starts[op] += 1
                state.operations[op] = body["create"]["workspaceId"]
            result = {"operationId": op}
        elif method == "GetWorkspaceOperation":
            op = body["operationId"]
            with state.lock:
                ws = state.operations.get(op)
                if ws:
                    state.active[op] += 1
                    state.peak[op] = max(state.peak[op], state.active[op])
                    state.active_keys_peak = max(state.active_keys_peak, sum(v > 0 for v in state.active.values()))
            if ws:
                try:
                    if not state.release.wait(15):
                        raise RuntimeError("The harness did not release its pending operation")
                    result = {"status": "WORKSPACE_OPERATION_STATUS_COMPLETED",
                              "result": {"workspace": {"workspaceId": ws, "head": "fixture", "branch": "main"}}}
                finally:
                    with state.lock:
                        state.active[op] -= 1
            else:
                status, result = 404, {"code": "not_found"}
        else:
            raise ValueError("Unexpected workspace method " + method)
        content = json.dumps(result).encode()
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(content)))
            self.end_headers()
            self.wfile.write(content)
        except (BrokenPipeError, ConnectionResetError):
            pass  # The crash scenario deliberately drops the owner's connection.


def call(url, cookie="", payload=None, key=""):
    headers = {"X-Coding-Web": "1", "Cookie": cookie, "Content-Type": "application/json"}
    if key:
        headers.update({"X-Coding-Async": "1", "Idempotency-Key": key})
    req = Request(url, data=None if payload is None else json.dumps(payload).encode(), headers=headers)
    try:
        response = HTTP.open(req, timeout=5)
    except HTTPError as exc:
        response = exc
    with response:
        data = response.read()
        return response.status, json.loads(data) if data.startswith((b"{", b"[")) else data, response.headers


def wait_for(check):
    end = time.monotonic() + 8
    while time.monotonic() < end:
        value = check()
        if value:
            return value
        time.sleep(0.01)
    raise TimeoutError("Timed out while waiting for " + str(check))


def worker(binary, directory, service_url, signing_key):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        address = "127.0.0.1:" + str(sock.getsockname()[1])
    env = {"PATH": os.defpath, "CODING_ADDR": address, "CODING_TASK_API": service_url,
           "CODING_DATA_DIR": directory, "CODING_WORKSPACES": "runtime", "CODING_DEV_LOGIN": "1",
           "AR_DEV_SIGNING_KEY": signing_key}
    process = subprocess.Popen([binary], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = "http://" + address

    def ready():
        if process.poll() is not None:
            raise RuntimeError(f"coding-web exited with {process.returncode}")
        try:
            return call(url + "/healthz")[0] == 200
        except URLError:
            return False

    try:
        wait_for(ready)
        code, _, headers = call(url + "/api/login", payload={"tenant": "acme", "user": "alice"})
        assert code == 200, code
        return process, url, headers["Set-Cookie"].split(";", 1)[0]
    except BaseException:
        process.kill()
        process.wait()
        raise


def run_trial(binary, scenario, number, quiet):
    with ExitStack() as stack:
        state = ThreadingHTTPServer(("127.0.0.1", 0), WorkspaceService)
        state.lock, state.release = threading.Lock(), threading.Event()
        state.starts, state.peak, state.active, state.operations = Counter(), Counter(), Counter(), {}
        state.active_keys_peak = 0
        threading.Thread(target=state.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True).start()
        stack.callback(state.server_close)
        stack.callback(state.shutdown)
        directory = stack.enter_context(tempfile.TemporaryDirectory())
        peers = []
        def stop_peer(process):
            if process.poll() is None:
                process.kill()
            process.wait()
        n = 4 if scenario in ("duplicate-get-4", "mixed-4") else 1 if scenario == "single" else 2
        signing_key = secrets.token_hex(32)
        for _ in range(n):
            peer = worker(binary, directory, "http://127.0.0.1:" + str(state.server_port), signing_key)
            peers.append(peer)
            stack.callback(stop_peer, peer[0])
        stack.callback(state.release.set)
        key = f"workspace-eval-{scenario}-{number:02d}"
        def submit(peer, name="Project", request_key=key):
            _, url, cookie = peers[peer]
            return call(url + "/api/projects", cookie, {"name": name}, request_key)[:2]
        def poll(peer, oid):
            _, url, cookie = peers[peer]
            return call(url + "/api/workspace-operations/" + oid, cookie)[:2]
        def started():
            with state.lock:
                return bool(state.active_keys_peak)
        code, first = submit(0)
        assert code == 202, (code, first)
        oid = first["operation_id"]
        outputs = [oid]
        wait_for(started)
        case_start = time.monotonic()
        replay_codes, conflict_code = [], None
        if scenario == "process-handoff":
            stop_peer(peers[0][0])
            replay_codes.append(poll(1, oid)[0])
        elif scenario == "two-independent":
            code, second = submit(1, "Project 2", key + "-second")
            assert code == 202
            outputs.append(second["operation_id"])
        elif scenario == "same-key-conflict":
            conflict_code, _ = submit(1, "Different payload")
        elif n > 1:
            def replay(i):
                use_post = scenario == "duplicate-post-2" or (scenario == "mixed-4" and i % 2 == 0)
                code, body = submit(i) if use_post else poll(i, oid)
                assert body.get("operation_id") == oid
                return code
            with ThreadPoolExecutor(n - 1) as pool:
                replay_codes = list(pool.map(replay, range(1, n)))
        replay_elapsed_ms = round((time.monotonic() - case_start) * 1000, 2)
        time.sleep(quiet)
        with state.lock:
            peak = max(state.peak.values(), default=0)
            parallel_keys = state.active_keys_peak
        state.release.set()
        reader = 1 if scenario == "process-handoff" else 0
        def finished(operation_id):
            code, data = poll(reader, operation_id)
            assert code == 200
            return data if data["status"] in ("COMPLETED", "FAILED") else None
        final = [wait_for(lambda op=op: finished(op)) for op in outputs]
        _, projects, _ = call(peers[reader][1] + "/api/projects", peers[reader][2])
        unique = len(set(outputs))
        checks = {"user_result": all(f.get("http_status") == 201 for f in final),
                  "one_project_per_key": len(projects) == unique == len(state.operations),
                  "one_poll_owner": peak == 1 if scenario != "process-handoff" else None,
                  "distinct_keys_overlap": (parallel_keys == 2 and unique == 2) if scenario == "two-independent" else None,
                  "conflict": conflict_code == 409 if scenario == "same-key-conflict" else None,
                  "fast_http": replay_elapsed_ms < 1000}
        return {"id": key, "scenario": scenario, "replicas": n, "max_concurrent_status_calls_one_key": peak,
                "max_concurrent_distinct_keys": parallel_keys, "runtime_start_calls": sum(state.starts.values()),
                "runtime_unique_mutations": len(state.operations), "project_count": len(projects),
                "replay_http_status": replay_codes, "conflict_status": conflict_code,
                "final_http_status": [f.get("http_status") for f in final], "replay_batch_ms": replay_elapsed_ms,
                "checks": checks, "pass": all(value is not False for value in checks.values())}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", type=Path)
    parser.add_argument("out", type=Path)
    args = parser.parse_args()
    cohort_bytes = (HERE / "cohort.json").read_bytes()
    cohort = json.loads(cohort_bytes)
    started = datetime.now(timezone.utc).isoformat()
    cases = [run_trial(args.binary.resolve(), scenario, rep, cohort["quiet_window_seconds"])
             for rep in range(1, cohort["repetitions"] + 1) for scenario in cohort["scenarios"]]
    doc = {"started_utc": started, "finished_utc": datetime.now(timezone.utc).isoformat(),
           "platform": platform.platform(), "binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(),
           "cohort_sha256": hashlib.sha256(cohort_bytes).hexdigest(),
           "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
           "passed": sum(row["pass"] for row in cases), "total": len(cases), "cases": cases}
    args.out.write_text(json.dumps(doc, indent=2) + "\n")
    print(f"{doc['passed']}/{doc['total']} {args.out}")


if __name__ == "__main__":
    main()
