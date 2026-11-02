// 叠加的细滚动条。原生滚动条在 Windows 上占 15～17px 宽，把内容挤开，各栏的宽度还随“有没有滚动条”变。主要指针不是手指的设备上
// （鼠标、触控板）把原生的藏起来（style.css 的 .osb-on），在页面最上层画一根不占空间的滑块：指针在哪个可滚动的区域上就画哪个的，
// 进入区域、滚动、靠近边缘时出现，停一会儿淡出；滑块能拖。滚轮、触控板、键盘的滚动照常是浏览器自己的。
// 触屏设备的原生滚动条本来就是叠加的，不动。弹窗（<dialog> 的顶层）里的区域，滑块放进那个弹窗，才能画在它上面。

const IDLE_MS = 1000; // 没有动静多久后淡出
const MIN_THUMB = 28; // 滑块最短（像素）
const TRACK = 12; // 轨道宽：滑块的可点范围，看得见的部分是 6px（hot 时 8px）
const EDGE = 18; // 指针离边缘多近算“靠近边缘”
const INPUT_MS = 600; // 滚轮、按键之后多久内的滚动算用户滚的

const SCROLLS = new Set(["auto", "scroll", "overlay"]);

function scrolls(el, axis) {
  if (axis === "y" ? el.scrollHeight <= el.clientHeight + 1 : el.scrollWidth <= el.clientWidth + 1) return false;
  const st = getComputedStyle(el);
  return SCROLLS.has(axis === "y" ? st.overflowY : st.overflowX);
}

// 从 el 往上找最近的、在这个方向上能滚动的区域（不含 html、body：页面本身不滚）。
function scrollerAt(el, axis) {
  for (let n = el instanceof Element ? el : el && el.parentElement; n && n !== document.body && n !== document.documentElement; n = n.parentElement) {
    if (n.classList && n.classList.contains("osb")) return null;
    if (scrolls(n, axis)) return n;
  }
  return null;
}

// el 真正看得见的那块：视口与各层会裁剪的祖先的交集。
function clipRect(el) {
  let r = { left: 0, top: 0, right: innerWidth, bottom: innerHeight };
  for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
    const st = getComputedStyle(p);
    if (st.overflowX === "visible" && st.overflowY === "visible") continue;
    const b = p.getBoundingClientRect();
    const l = b.left + p.clientLeft;
    const t = b.top + p.clientTop;
    r = { left: Math.max(r.left, l), top: Math.max(r.top, t), right: Math.min(r.right, l + p.clientWidth), bottom: Math.min(r.bottom, t + p.clientHeight) };
    if (st.position === "fixed") break;
  }
  return r;
}

function makeBar(axis) {
  const node = document.createElement("div");
  node.className = "osb " + axis;
  node.setAttribute("aria-hidden", "true");
  const thumb = document.createElement("div");
  thumb.className = "osb-thumb";
  node.append(thumb);
  return { axis, node, thumb, el: null, shown: false, hot: false, drag: null, timer: 0, len: 0, room: 0 };
}

export function installScrollbars() {
  // 主要的指针是手指（手机、平板）时不动：那里的原生滚动条本来就叠在内容上。
  if (!window.matchMedia || matchMedia("(pointer: coarse)").matches) return null;
  document.documentElement.classList.add("osb-on");
  const bars = [makeBar("y"), makeBar("x")];
  const hovered = { y: null, x: null };
  let lastInput = 0;
  let raf = 0;
  let pending = null;

  const host = (el) => (el && el.closest("dialog[open]")) || document.body;

  function target(bar, el) {
    if (bar.el === el) return;
    bar.el = el;
    const parent = host(el);
    if (bar.node.parentElement !== parent) parent.append(bar.node);
  }

  function show(bar) {
    if (!bar.el) return;
    if (!bar.shown) { bar.shown = true; place(bar); bar.node.classList.add("show"); }
    clearTimeout(bar.timer);
    if (!bar.hot && !bar.drag) bar.timer = setTimeout(() => hide(bar), IDLE_MS);
    loop();
  }

  function hide(bar) {
    if (bar.hot || bar.drag) return;
    clearTimeout(bar.timer);
    bar.shown = false;
    bar.node.classList.remove("show");
  }

  function place(bar) {
    const el = bar.el;
    const y = bar.axis === "y";
    if (!el || !el.isConnected || !scrolls(el, bar.axis)) { bar.hot = false; bar.drag = null; hide(bar); return; }
    const r = el.getBoundingClientRect();
    const left = r.left + el.clientLeft;
    const top = r.top + el.clientTop;
    const view = y ? el.clientHeight : el.clientWidth;
    const full = y ? el.scrollHeight : el.scrollWidth;
    const tx = y ? left + el.clientWidth - TRACK : left;
    const ty = y ? top : top + el.clientHeight - TRACK;
    const tw = y ? TRACK : el.clientWidth;
    const th = y ? el.clientHeight : TRACK;
    const length = y ? th : tw;
    const len = Math.min(length, Math.max(MIN_THUMB, (length * view) / full));
    const max = full - view;
    const at = y ? el.scrollTop : Math.abs(el.scrollLeft);
    const pos = (length - len) * Math.min(1, Math.max(0, max > 0 ? at / max : 0));
    bar.len = len;
    bar.room = length - len;
    const s = bar.node.style;
    s.width = tw + "px";
    s.height = th + "px";
    s.transform = `translate(${tx}px, ${ty}px)`;
    // 弹窗或带 transform 的祖先会改变 fixed 的参照：量一下实际落在哪，差多少补多少。
    const got = bar.node.getBoundingClientRect();
    if (Math.abs(got.left - tx) > 0.5 || Math.abs(got.top - ty) > 0.5) s.transform = `translate(${2 * tx - got.left}px, ${2 * ty - got.top}px)`;
    const t = bar.thumb.style;
    if (y) { t.top = pos + "px"; t.height = len + "px"; } else { t.left = pos + "px"; t.width = len + "px"; }
    // 区域被外面裁掉一部分（例如线程里滚出去一半的代码块），滑块也只画看得见的那段。
    const c = clipRect(el);
    const inset = [Math.max(0, c.top - ty), Math.max(0, tx + tw - c.right), Math.max(0, ty + th - c.bottom), Math.max(0, c.left - tx)];
    if (inset[0] + inset[2] >= th || inset[1] + inset[3] >= tw) { bar.node.style.clipPath = "inset(50%)"; return; }
    s.clipPath = inset.some((v) => v > 0) ? `inset(${inset.map((v) => v + "px").join(" ")})` : "";
  }

  // 显示着的时候每帧对一下位置：内容在长（流式输出）、布局在变（拖动分栏、开关侧栏）都跟得上；都淡出了就停。
  function loop() {
    if (raf) return;
    const tick = () => {
      raf = 0;
      let any = false;
      for (const bar of bars) if (bar.shown || bar.drag) { place(bar); any = true; }
      if (any) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
  }

  function near(el, axis, e) {
    const r = el.getBoundingClientRect();
    return axis === "y" ? r.left + el.clientLeft + el.clientWidth - e.clientX < EDGE : r.top + el.clientTop + el.clientHeight - e.clientY < EDGE;
  }

  function onMove(e) {
    if (e.pointerType === "touch") return;
    const queued = pending;
    pending = e;
    if (queued) return; // 一帧只看最后一次移动
    requestAnimationFrame(() => {
      const ev = pending;
      pending = null;
      if (!ev || bars.some((b) => b.drag)) return;
      if (ev.target instanceof Element && ev.target.closest(".osb")) return;
      for (const bar of bars) {
        const el = scrollerAt(ev.target, bar.axis);
        if (el !== hovered[bar.axis]) {
          hovered[bar.axis] = el;
          if (el) { target(bar, el); show(bar); } else hide(bar);
        } else if (el && near(el, bar.axis, ev)) {
          target(bar, el);
          show(bar);
        }
      }
    });
  }

  function onScroll(e) {
    const el = e.target === document ? null : e.target;
    if (!(el instanceof Element) || el === document.body || el === document.documentElement) return;
    const user = Date.now() - lastInput < INPUT_MS;
    for (const bar of bars) {
      if (bar.drag) continue;
      if (el === hovered[bar.axis] || el === bar.el || (user && scrolls(el, bar.axis))) {
        if (!scrolls(el, bar.axis)) continue;
        target(bar, el);
        show(bar);
      }
    }
  }

  for (const bar of bars) {
    const { thumb, node } = bar;
    thumb.addEventListener("pointerenter", () => { bar.hot = true; node.classList.add("hot"); clearTimeout(bar.timer); });
    thumb.addEventListener("pointerleave", () => { bar.hot = false; node.classList.remove("hot"); if (!bar.drag) show(bar); });
    thumb.addEventListener("pointerdown", (e) => {
      if (e.button !== 0 || !bar.el) return;
      e.preventDefault();
      e.stopPropagation();
      thumb.setPointerCapture(e.pointerId);
      const y = bar.axis === "y";
      const el = bar.el;
      const max = y ? el.scrollHeight - el.clientHeight : el.scrollWidth - el.clientWidth;
      bar.drag = { id: e.pointerId, from: y ? e.clientY : e.clientX, start: y ? el.scrollTop : el.scrollLeft, ratio: bar.room > 0 ? max / bar.room : 0 };
      node.classList.add("drag");
      document.documentElement.classList.add("osb-dragging");
      clearTimeout(bar.timer);
      loop();
    });
    thumb.addEventListener("pointermove", (e) => {
      const d = bar.drag;
      if (!d || e.pointerId !== d.id || !bar.el) return;
      const delta = ((bar.axis === "y" ? e.clientY : e.clientX) - d.from) * d.ratio;
      if (bar.axis === "y") bar.el.scrollTop = d.start + delta; else bar.el.scrollLeft = d.start + delta;
    });
    const end = (e) => {
      if (!bar.drag || e.pointerId !== bar.drag.id) return;
      bar.drag = null;
      node.classList.remove("drag");
      document.documentElement.classList.remove("osb-dragging");
      show(bar);
    };
    thumb.addEventListener("pointerup", end);
    thumb.addEventListener("pointercancel", end);
  }

  const input = () => { lastInput = Date.now(); };
  addEventListener("pointermove", onMove, { passive: true });
  addEventListener("wheel", input, { passive: true, capture: true });
  addEventListener("keydown", input, { capture: true });
  document.addEventListener("scroll", onScroll, { passive: true, capture: true });
  document.documentElement.addEventListener("pointerleave", () => {
    hovered.x = hovered.y = null;
    for (const bar of bars) hide(bar);
  });
  addEventListener("blur", () => { for (const bar of bars) hide(bar); });
  return { bars, hovered };
}
