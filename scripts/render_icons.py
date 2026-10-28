"""从 internal/web/static/favicon.svg 生成位图图标（只用标准库，渲染借本机的 Chrome 或 Edge 的无头模式）。

    python scripts/render_icons.py [--browser <chrome.exe 或 msedge.exe 的路径>]

生成（都放在 internal/web/static，提交进仓库）：
- favicon.ico：16、32、48 三种尺寸（PNG 封进 ICO）。不认 SVG 图标的地方（Safari 的旧版本、书签、搜索结果）请求 /favicon.ico。
- apple-touch-icon.png：180×180，铺满不留圆角（iOS 自己切圆角，透明的角会变黑）。
改了标志就重跑一遍。
"""
from __future__ import annotations

import argparse
import os
import shutil
import struct
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
STATIC = ROOT / "internal" / "web" / "static"
CANDIDATES = [
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    "google-chrome", "chromium", "chromium-browser", "microsoft-edge",
]


def browser(explicit: str) -> str:
    for c in ([explicit] if explicit else CANDIDATES):
        found = c if Path(c).is_file() else shutil.which(c)
        if found:
            return found
    sys.exit("找不到 Chrome 或 Edge：用 --browser 指定")


def render(exe: str, svg: str, size: int, out: Path, work: Path) -> bytes:
    page = work / f"icon-{size}.html"
    page.write_text(
        "<!doctype html><html><head><style>html,body{margin:0;padding:0;background:transparent}"
        f"svg{{display:block;width:{size}px;height:{size}px}}</style></head><body>{svg}</body></html>",
        "utf-8",
    )
    shot = work / f"icon-{size}.png"
    subprocess.run(
        [exe, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--force-device-scale-factor=1",
         "--default-background-color=00000000", f"--user-data-dir={work / 'profile'}",
         f"--window-size={size},{size}", f"--screenshot={shot}", page.as_uri()],
        check=True, capture_output=True, timeout=120,
    )
    data = shot.read_bytes()
    w, h = struct.unpack(">II", data[16:24])
    if (w, h) != (size, size):
        sys.exit(f"渲染出来是 {w}×{h}，不是 {size}×{size}")
    return data


def ico(pngs: dict[int, bytes]) -> bytes:
    """ICO 容器，每个尺寸放一张 PNG（Windows Vista 起与各家浏览器都认）。"""
    head = struct.pack("<HHH", 0, 1, len(pngs))
    offset = 6 + 16 * len(pngs)
    entries, blobs = b"", b""
    for size, data in sorted(pngs.items()):
        entries += struct.pack("<BBBBHHII", size % 256, size % 256, 0, 0, 1, 32, len(data), offset + len(blobs))
        blobs += data
    return head + entries + blobs


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--browser", default=os.environ.get("CODING_BROWSER", ""))
    args = parser.parse_args()
    exe = browser(args.browser)
    svg = (STATIC / "favicon.svg").read_text("utf-8").strip()
    # 苹果的图标铺满：去掉圆角。
    full_bleed = svg.replace('rx="8"', 'rx="0"')
    if full_bleed == svg:
        sys.exit('favicon.svg 里没有 rx="8" 的底色，改了标志的写法就要跟着改这里')
    with tempfile.TemporaryDirectory() as tmp:
        work = Path(tmp)
        (STATIC / "favicon.ico").write_bytes(ico({s: render(exe, svg, s, STATIC, work) for s in (16, 32, 48)}))
        (STATIC / "apple-touch-icon.png").write_bytes(render(exe, full_bleed, 180, STATIC, work))
    for name in ("favicon.ico", "apple-touch-icon.png"):
        print(f"{name}  {(STATIC / name).stat().st_size} B")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
