# Code UI design system

## Direction

A quiet coding workspace with a readable conversation at its center. Preserve the existing neutral light/dark surfaces and terracotta accent. The primary reference is the user's Claude conversation screenshot: public progress paragraphs alternate with compact groups of activity. This is a conversation, with inspectable operations embedded in it.

## Content hierarchy

1. The user's request is a contained message aligned to the end.
2. Public assistant progress is ordinary text at the main reading level, in event order.
3. Consecutive tool calls form a group with one muted summary. A new public paragraph starts a new group. Late results update the original call.
4. Each tool occupies one row: state icon, short action, truncated target, optional duration, disclosure indicator. Full commands and error details are inside its disclosure.
5. The final answer and delivered artifacts follow the activity. Diagnostics remain one separate, collapsed entry with a small call/event summary. Expanded diagnostics show three inline metrics and the latest six compact event rows; earlier rows and per-event descriptions disclose on demand. Raw JSON has one final disclosure and a copy action.

Never render private reasoning, planner instructions, or internal stage events as public assistant prose. Never manufacture intermediate messages or tool output. Histories without progress text still show their real calls.

## Palette and typography

Use application semantic tokens: `--page`, `--surface`, `--surface-2`, `--text`, `--text-2`, `--muted`, `--line`, `--accent`, `--err`. HeroUI variables map to these in `code-ui.css`. No new brand color. Failure uses the error color; a completed operation does not need a brightly colored badge.

Use the existing system sans stack, 14px / 1.8 for conversation, 12px for tool rows and summaries, 11px for metadata. Commands use the existing mono stack. Numbers use tabular figures. Full timestamps belong in details.

## Layout and surfaces

The conversation has an 800px maximum outer width, with 24px desktop and 16px mobile padding. Keep one document scroll area. Tool groups have one shared 1px border and 12px outer radius; inner rows only have separators. No individual tool cards, fixed-height log viewport, or decorative timeline rails.

Group rows are about 38px on desktop and at least 44px on mobile. Commands are single-line in the trigger and wrap safely in details. Groups initially show up to eight rows, with an explicit show-more control. Current running activity remains visible even when the group is collapsed. Failure counts remain visible on the summary without automatically changing the user's expansion choice.

Paragraphs have 14–22px separation. The composer shares the reading axis and uses a single 22px-radius surface. Its full-width textarea occupies the upper tier; a real Pro Toolbar inside the same shell places model/settings/usage controls before a 36px circular send action. The enabled send button uses neutral foreground/background contrast. Focus subtly strengthens the neutral border without an accent halo. New-session prompts and active-session prompts use the same editor, with a slightly taller initial writing area. On narrow containers, the settings label becomes an accessible icon while the model name remains visible.

## Components and ownership

Use actual `@heroui-pro/react` components, not names copied onto native controls:

- `ChatMessage`, `Markdown`, `CodeBlock`: public text and answers.
- `ChainOfThought`, `ChatTool`: disclosures and tool rows; the name of the library component does not authorize rendering hidden reasoning.
- `ChatAttachment`: delivered files.
- `PromptInput.TextArea`, `PromptInput.Send`, `PromptInput.Action`: React-owned editor and actions.
- `Button`: additional disclosure actions.
- `Sidebar` (with `Dropdown`, `Avatar`, `Kbd`): application navigation, the workspace and session tree, session actions and the account menu.

`ui/chat/conversation.tsx` owns the complete activity stream and expansion state under one React root per task. The application controller supplies serializable data. It must not replace individual React tool rows on SSE updates. The composer uses a subscription model and retains the same textarea while the task phase changes. Approval/status controls currently bridge into the React composer; application routing and settings remain in the existing controller. `ui/chat/sidebar.tsx` owns the sidebar's presentation under one React root per signed-in session; the controller supplies its data and performs every action.

Use pinned official components and their precompiled Tailwind v4 CSS. Keep app-specific styles limited to the visual hierarchy above. Preserve same-origin assets and the existing CSP.

## Accessibility and responsive behavior

Use the components' keyboard-accessible disclosure controls, accurate `aria-expanded`, named icon actions, and visible focus indicators. Preserve IME composition and Shift+Enter. Enter follows the application's send/steer action; Ctrl/Meta+Enter retains interruption behavior. Respect reduced-motion preferences. At small widths hide auxiliary duration text, keep full details accessible, and never let a command widen the page.

## Verification

Use real Chrome tests for both themes and 390 / 900 / 1440px layouts. Check grouping and late events, SSE and history replay, expansion retention, drafts and selection, Chinese IME, sending, steering, approvals, attachments, safe Markdown, and strict CSP. Review the actual rendered app as well as DOM geometry.

## Motion

Use restrained 120ms press/hover feedback, 180ms fades and 220ms disclosure movement, with `cubic-bezier(.2,.7,.2,1)`. New public paragraphs rise 5px as they arrive. A tool status icon settles once when a running call completes; historical successes do not pulse and token updates must not replay entrance motion. Keep Pro's measured-height disclosure mechanism. Native details gain progressive `::details-content` / intrinsic-size transitions, with immediate native disclosure behavior on older engines. Input focus uses a quiet neutral border transition; enabled send/attachment actions move at most 1–2px. Do not animate sidebar navigation, reorder content, or delay task actions. Reduced-motion disables all decoration, including the existing loader/cursor animations, while leaving textual status visible.

## Sources and delegated work

The composer starts with a quiet plus action and places selected source chips above the full-width editor. Previews stay 32×32px; long names truncate without hiding upload state. Upload errors offer retry/remove, and only ready attachments can be submitted. Dragging files shows a temporary in-shell target; normal text paste remains native.

Use one shared inspector surface for subagents and sources, separated by one thin rule. Counts come from real task events; failed or cancelled work never counts as completed. Sources show three compact rows initially (48px each, 28px previews), a view-all disclosure, and explicit add/reuse controls. Keep the same light/dark semantic tokens and mobile reachability.
