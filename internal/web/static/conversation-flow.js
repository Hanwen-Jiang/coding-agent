// Project persisted events into the public conversation. No reasoning, verdicts
// or pipeline state becomes assistant prose. Rebuildable from either SSE or a snapshot.
export function conversationFlow(events) {
  const parts = [], seen = new Set(), indexedCalls = new Map(), pending = new Map(), notes = new Set();
  let group = null;
  const ordered = [...events].sort((a, b) => Number(a.event_id || 0) - Number(b.event_id || 0));
  const note = (id, message, source) => {
    if (typeof message !== "string" || !message.trim() || notes.has(id)) return;
    notes.add(id);
    // Runtime limits public text by Unicode code points (Python str), not UTF-16
    // units. Preserve the same boundary so a streamed note survives replay intact.
    parts.push({ kind: "progress", id, message: [...message.trim()].slice(0, 2000).join(""), source });
    group = null;
  };
  for (const event of ordered) {
    if (event.event_id != null) {
      if (seen.has(String(event.event_id))) continue;
      seen.add(String(event.event_id));
    }
    const value = event.value || {};
    // Planner goals instruct execution; they are not messages addressed to the
    // user. The collapsible plan already exposes those details when requested.
    if (event.type !== "tool_status") continue;
    const stepKey = value.step != null
      ? `tool:${value.round_seq ?? ""}:${value.node_id ?? ""}:${value.step}`
      : `event:${event.event_id}`;
    if (value.state === "running") note(stepKey, value.progress_message, "assistant_text");
    const indexedKey = value.call_index != null ? `${stepKey}:call:${value.call_index}` : null;
    const signature = JSON.stringify([value.round_seq, value.node_id, value.step, value.tool, value.target]);
    const queue = pending.get(signature) || [];
    // One model response can invoke several tools, including the same tool and
    // target more than once. Step identifies a response, not a single call.
    let call = indexedKey ? indexedCalls.get(indexedKey)
      : value.state !== "running" ? queue.shift() : null;
    if (call) {
      // A late result belongs to the group where its call began, even when
      // another node has already emitted a new public progress message.
      if (call.event.value.state === "running" || value.state !== "running") call.event = event;
      continue;
    }
    const key = indexedKey || `${stepKey}:event:${event.event_id}`;
    if (!group) {
      group = { kind: "tools", id: key, calls: [] };
      parts.push(group);
    }
    call = { id: key, event };
    group.calls.push(call);
    if (indexedKey) indexedCalls.set(indexedKey, call);
    else if (value.state === "running") {
      queue.push(call);
      pending.set(signature, queue);
    }
  }
  return parts;
}

// Counts are calls, not inferred successes or invented file-change evidence.
export function toolGroupCounts(calls) {
  const counts = { read: 0, search: 0, command: 0, edit: 0, other: 0 };
  for (const { event } of calls) {
    const tool = event.value?.tool;
    const kind = tool === "repo_read" ? "read"
      : ["repo_list", "repo_grep"].includes(tool) ? "search"
      : tool === "shell_run" ? "command"
      : ["repo_edit", "apply_patch", "write_file"].includes(tool) ? "edit" : "other";
    counts[kind]++;
  }
  return counts;
}
