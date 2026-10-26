"""本地拉起 coding agent：先用旁边 Runtime 仓库的 scripts/dev_stack.py 拉起 Runtime（Temporal、task-service、Model Gateway、Worker），
再编译并拉起 coding-web。只用标准库。

    python scripts/dev.py up [--runtime 目录] [--root 目录] [--port 8095] [dev_stack up 的其余选项，例如 --sandbox docker、
                             --provider mock --decisions stub、--provider-env 文件、--tenant-effect 租户=effect]
    python scripts/dev.py status                 Runtime 各组件与 coding-web 的状态
    python scripts/dev.py start | kill           只拉起（先重新编译）或强杀 coding-web（Runtime 不动）
    python scripts/dev.py env                    只打印 coding-web 的环境变量（密钥打码）
    python scripts/dev.py down                   停掉 coding-web 与整套 Runtime

- Runtime 的检出默认在本仓库旁边（../agent-runtime），--runtime 或 AR_RUNTIME_DIR 可改。dev_stack.py 在 Runtime 仓库里用
  uv run --no-sync 跑（Worker 用 Runtime 的虚拟环境）；PATH 上没有 uv 时用跑这个脚本的 Python。
- 运行目录（--root，默认 ~/.agentruntime/coding-dev）是 Runtime 与 coding-web 共用的：coding-web 的数据在 <目录>/coding，
  日志 logs/coding.log，状态 coding.json。coding-web 与 Worker 共用工作区（Runtime 报告里的 workspace_root），
  也共用开发租户策略文件（沙箱等级 = Task 的 effect 上限，见 docs/plan/coding-agent-mvp.md 第 7.3 节）。
- 第三方登录：~/.agentruntime/oauth.env（或旧的 github-oauth.env）里配了哪几家就打开哪几家（格式 NAME = value；
  CODING_<平台>_CLIENT_ID 与 _CLIENT_SECRET，可选 _URL、_API_URL、_SCOPES、_NAME；平台是 GITHUB、GITEE、GITLAB、GITEA、
  BITBUCKET、GOOGLE）。OAuth 回调 http://127.0.0.1:<端口>/api/auth/<平台>/callback，所以端口默认固定 8095。
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_RUNTIME = REPO_ROOT.parent / "agent-runtime"
DEFAULT_ROOT = Path.home() / ".agentruntime" / "coding-dev"
DEFAULT_PORT = 8095
OAUTH_ENVS = (Path.home() / ".agentruntime" / "github-oauth.env", Path.home() / ".agentruntime" / "oauth.env")
OAUTH_PROVIDERS = ("GITHUB", "GITEE", "GITLAB", "GITEA", "BITBUCKET", "GOOGLE")
OAUTH_NAMES = tuple(f"CODING_{p}_{k}" for p in OAUTH_PROVIDERS for k in ("CLIENT_ID", "CLIENT_SECRET", "URL", "API_URL", "SCOPES", "NAME"))
EXE = ".exe" if os.name == "nt" else ""


class DevError(Exception):
    pass


# --- 小工具（与 Runtime 的 dev_stack.py 同一套做法） ---


def read_env_file(path: Path) -> dict[str, str]:
    """NAME = value 一行一个；# 开头的是注释。"""
    out: dict[str, str] = {}
    for line in path.read_text("utf-8").splitlines():
        name, sep, value = line.partition("=")
        if sep and not name.strip().startswith("#"):
            out[name.strip()] = value.strip()
    return out


def wait_until(check: Any, what: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    last: BaseException | None = None
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except Exception as exc:  # noqa: BLE001 — 轮询期间的错误只记最后一个
            last = exc
        time.sleep(0.3)
    raise DevError(f"{timeout:.0f} 秒内没有等到：{what}（最后的错误：{last!r}）")


def http_ok(url: str) -> bool:
    with urllib.request.urlopen(url, timeout=2) as resp:
        return resp.status == 200


def alive(pid: int | None) -> bool:
    if not pid:
        return False
    if os.name == "nt":
        import ctypes

        kernel32 = ctypes.windll.kernel32
        handle = kernel32.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
        if not handle:
            return False
        code = ctypes.c_ulong()
        ok = kernel32.GetExitCodeProcess(handle, ctypes.byref(code))
        kernel32.CloseHandle(handle)
        return bool(ok) and code.value == 259  # STILL_ACTIVE
    try:
        os.kill(pid, 0)
    except OSError:
        return False
    return True


def kill(pid: int | None) -> None:
    if not alive(pid):
        return
    assert pid is not None
    if os.name == "nt":
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(pid)], capture_output=True, check=False)
    else:
        try:
            os.killpg(pid, signal.SIGKILL)
        except OSError:
            os.kill(pid, signal.SIGKILL)
    deadline = time.monotonic() + 15
    while alive(pid) and time.monotonic() < deadline:
        time.sleep(0.2)


def spawn(args: list[str], env: dict[str, str], log: Path) -> int:
    """后台起一个进程，脚本退出后它还在；标准输出与标准错误都写 log。"""
    log.parent.mkdir(parents=True, exist_ok=True)
    out = log.open("ab")
    kwargs: dict[str, Any] = {}
    if os.name == "nt":
        kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP | subprocess.DETACHED_PROCESS
    else:
        kwargs["start_new_session"] = True
    return subprocess.Popen(args, env=env, stdin=subprocess.DEVNULL, stdout=out, stderr=subprocess.STDOUT, **kwargs).pid


# --- Runtime（旁边仓库的 dev_stack.py） ---


class Runtime:
    def __init__(self, path: Path, root: Path) -> None:
        self.path = path.resolve()
        self.root = root.resolve()
        if not (self.path / "scripts" / "dev_stack.py").exists():
            raise DevError(f"在 {self.path} 找不到 Runtime 仓库（scripts/dev_stack.py）：用 --runtime 或 AR_RUNTIME_DIR 指定")

    def command(self, *args: str) -> list[str]:
        uv = shutil.which("uv")
        base = [uv, "run", "--no-sync", "python"] if uv else [sys.executable]
        return [*base, str(self.path / "scripts" / "dev_stack.py"), *args, "--root", str(self.root)]

    def run(self, *args: str, capture: bool = False) -> str:
        proc = subprocess.run(self.command(*args), cwd=self.path, text=True, encoding="utf-8", errors="replace", capture_output=capture)
        if proc.returncode != 0:
            raise DevError(f"dev_stack {args[0]} 失败（退出码 {proc.returncode}）" + (f"：\n{proc.stderr}" if capture else ""))
        return proc.stdout if capture else ""

    def report(self) -> dict[str, Any]:
        """dev_stack status --json 的最后一行（地址、工作区、策略文件、签名密钥的位置；不含密钥本身）。"""
        lines = [line for line in self.run("status", "--json", capture=True).splitlines() if line.strip()]
        report = json.loads(lines[-1]) if lines else {}
        if not report.get("task_api"):
            raise DevError(f"{self.root} 上还没有拉起 Runtime：先 up")
        return report


# --- coding-web ---


class Coding:
    def __init__(self, root: Path) -> None:
        self.root = root.resolve()
        self.state_file = self.root / "coding.json"
        self.state: dict[str, Any] = json.loads(self.state_file.read_text("utf-8")) if self.state_file.exists() else {}

    @property
    def binary(self) -> Path:
        return self.root / "bin" / f"coding-web{EXE}"

    @property
    def log(self) -> Path:
        return self.root / "logs" / "coding.log"

    @property
    def port(self) -> int:
        return int(self.state.get("port") or DEFAULT_PORT)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def save(self) -> None:
        self.root.mkdir(parents=True, exist_ok=True)
        self.state_file.write_text(json.dumps(self.state, ensure_ascii=False, indent=2), "utf-8")

    def build(self) -> None:
        go = shutil.which("go")
        if go is None:
            raise DevError("PATH 上没有 go")
        proc = subprocess.run([go, "build", "-o", str(self.binary), "./cmd/coding-web"], cwd=REPO_ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace")
        if proc.returncode != 0:
            raise DevError(f"编译 coding-web 失败：\n{proc.stderr}")

    def env(self, report: dict[str, Any]) -> dict[str, str]:
        root = Path(report.get("root") or self.root)
        key_file = Path(report.get("signing_key_file") or root / "shared" / "signing.key")
        if not key_file.exists():
            raise DevError(f"找不到 Runtime 的开发签名密钥 {key_file}")
        sandbox = report.get("sandbox") or "host"
        env = {
            "CODING_ADDR": f"127.0.0.1:{self.port}",
            "CODING_TASK_API": report["task_api"],
            "CODING_WORKSPACE_ROOT": report["workspace_root"],
            "CODING_DATA_DIR": str(self.root / "coding"),
            "CODING_DEV_LOGIN": "1",
            "CODING_TENANT_POLICIES_FILE": report.get("policies_file") or str(root / "tenant_policies.json"),
            "CODING_PUBLIC_URL": self.url,
            "AR_DEV_SIGNING_KEY": key_file.read_text("utf-8").strip(),
            # 只用来在界面上说明 shell_run 在哪里跑；镜像与 Worker 的默认 AR_TOOL_SHELL_IMAGE 相同。
            "CODING_SANDBOX": sandbox,
        }
        if sandbox == "docker":
            env["CODING_SANDBOX_IMAGE"] = os.environ.get("AR_TOOL_SHELL_IMAGE") or "python:3.13-slim"
        for path in OAUTH_ENVS:
            if path.exists():
                values = read_env_file(path)
                env.update({k: values[k] for k in OAUTH_NAMES if values.get(k)})
        return env

    def start(self, report: dict[str, Any]) -> None:
        if alive(self.state.get("pid")):
            return
        if not self.binary.exists():
            self.build()
        (self.root / "coding").mkdir(parents=True, exist_ok=True)
        pid = spawn([str(self.binary)], {**os.environ, **self.env(report)}, self.log)
        self.state.update({"pid": pid, "log": str(self.log), "port": self.port})
        self.save()
        wait_until(lambda: http_ok(self.url + "/healthz"), "coding-web 就绪", 30)

    def kill(self) -> None:
        kill(self.state.get("pid"))


# --- 命令 ---


def cmd_up(args: argparse.Namespace, rest: list[str]) -> int:
    runtime, coding = Runtime(args.runtime, args.root), Coding(args.root)
    if alive(coding.state.get("pid")):
        raise DevError(f"{coding.root} 上的 coding-web 还在运行：先 down")
    coding.state["port"] = args.port
    coding.build()
    runtime.run("up", *rest)
    try:
        coding.start(runtime.report())
    except DevError:
        print(f"coding-web 没起来，日志 {coding.log}", file=sys.stderr)
        raise
    print(f"  Coding     {coding.url}  （网页，开发登录）")
    return 0


def cmd_status(args: argparse.Namespace, _: list[str]) -> int:
    runtime, coding = Runtime(args.runtime, args.root), Coding(args.root)
    runtime.run("status")
    pid = coding.state.get("pid")
    print(f"  coding-web    {'运行中' if alive(pid) else '已停止'}  pid {pid}  {coding.url}  日志 {coding.log}")
    return 0


def cmd_start(args: argparse.Namespace, _: list[str]) -> int:
    coding = Coding(args.root)
    report = Runtime(args.runtime, args.root).report()
    if not alive(coding.state.get("pid")):
        coding.build()  # 改了代码之后 kill + start 就生效
    coding.start(report)
    print(f"  Coding     {coding.url}")
    return 0


def cmd_kill(args: argparse.Namespace, _: list[str]) -> int:
    Coding(args.root).kill()
    return 0


def cmd_down(args: argparse.Namespace, _: list[str]) -> int:
    Coding(args.root).kill()
    Runtime(args.runtime, args.root).run("down")
    return 0


def cmd_env(args: argparse.Namespace, _: list[str]) -> int:
    env = Coding(args.root).env(Runtime(args.runtime, args.root).report())
    for k in env:
        if k == "AR_DEV_SIGNING_KEY" or k.endswith("_CLIENT_SECRET"):
            env[k] = "***"
    for k, v in sorted(env.items()):
        print(f"{k}={v}")
    return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="python scripts/dev.py", description="本地拉起 coding agent（Runtime + coding-web）")
    p.add_argument("command", choices=("up", "status", "start", "kill", "down", "env"))
    p.add_argument("--runtime", type=Path, default=Path(os.environ.get("AR_RUNTIME_DIR") or DEFAULT_RUNTIME), help="Runtime 仓库的检出（默认 ../agent-runtime）")
    p.add_argument("--root", type=Path, default=DEFAULT_ROOT, help=f"运行目录（默认 {DEFAULT_ROOT}）")
    p.add_argument("--port", type=int, default=DEFAULT_PORT, help=f"coding-web 的端口（默认 {DEFAULT_PORT}；OAuth 回调地址要固定）")
    args, rest = p.parse_known_args(argv)
    if rest and args.command != "up":
        p.error(f"{args.command} 不认识这些选项：{' '.join(rest)}")
    func = {"up": cmd_up, "status": cmd_status, "start": cmd_start, "kill": cmd_kill, "down": cmd_down, "env": cmd_env}[args.command]
    try:
        return func(args, rest)
    except DevError as err:
        print(f"dev {args.command}：{err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
