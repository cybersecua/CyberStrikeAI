# Telegram Bot Feature Parity — Design

Date: 2026-04-26
Status: Approved for planning
Author: badb@cybersec.org
Branch target: `main`

## Goal

Bring the Telegram bot path to feature and observability parity with the main UI: honor the `claude-cli` provider toggle, integrate with the debug-capture subsystem, surface streaming progress to the user, persist per-user session state across restarts, support per-chat agent-mode override, and make bot conversations visible / filterable in the operator UI.

## Non-goals (deferred to follow-up sub-projects)

- **Sub-project C (deferred)**: file/voice/image attachments. Bot remains text-only in v1.
- **Sub-project D (deferred)**: per-user role assignment, allowlist defaults, challenge-on-first-use, `DefaultRole`/`AllowedRoles` per-platform config. Bot user governance stays as-is for now.
- Adding DingTalk/Lark/WeCom platforms. The schema supports `platform` as an arbitrary string so future work doesn't need a migration, but no code lands.

## Threat model context

CyberStrikeAI is operator-only (per `feedback_csai_threat_model.md`). The Telegram bot is exposed only to the operator's authorized chat IDs; there is no public-facing risk surface. Bot users are operators; privileged tools through the bot are intentional. None of the design choices here introduce new restrictions on tool capability.

## Scope locked during brainstorming

| Question | Decision |
|---|---|
| Q1 — agent-mode default + override | Inherit from `MultiAgent.RobotUseMultiAgent` global config at conversation start; user overrides per-chat with `mode <single\|multi>` slash command; `mode default` reverts to global |
| Q2 — streaming progress verbosity | Per major event (iteration boundary, `tool_call`, `tool_result`, `response_start`) edited into placeholder, throttled to ≥3 s between Telegram edits |
| Q3 — bot-conversation visibility in UI | Filter dropdown ("All / Web / Telegram") + per-card platform badge in conversation list |
| 4i — concurrent messages from same user | Per-conversation `tasks.StartTask` lock; second message gets synchronous "⚠️ Another task is running for this chat. Say `stop` to cancel." (mirrors web UI semantics) |
| Architecture approach | A — minimal-touch: lift missing pieces into existing handlers, no broad refactor of `AgentLoopStream` / `ProcessMessageForRobot` |

## Architecture

Three concentric layers; new code lands at the highlighted points only.

```
                     ┌────────────────────┐
Telegram update ───→ │ telegram.go        │
                     │ (long-poll loop)   │
                     └─────┬──────────────┘
                           │ via StreamingMessageHandler ← NEW
                           ▼
                     ┌──────────────────────────────┐
                     │ RobotHandler.HandleMessage   │
                     │   Stream                     │ ← NEW: implements
                     │  • load/save bot_sessions    │   StreamingMessageHandler
                     │  • dispatch commands incl.   │   from DB (Gap 5)
                     │    new `mode <single|multi>` │
                     │  • call ProcessMessageFor    │   Adds `mode` cmd (Gap 6)
                     │    Robot w/ progressFn       │
                     └─────┬────────────────────────┘
                           │
                           ▼
                     ┌──────────────────────────────┐
                     │ AgentHandler.ProcessMessage  │
                     │   ForRobot                   │
                     │  • sink.StartSession         │ ← NEW (Gap 2)
                     │  • EffectiveProvider branch  │ ← NEW (Gap 1)
                     │  • debug.WithCapture(ctx,…)  │ ← NEW
                     │  • single-vs-multi from      │
                     │    bot_sessions.current_mode │
                     │    || global default         │
                     │  • defer sink.EndSession     │ ← NEW
                     └──────────────────────────────┘
```

## Components

### Database schema

Two additions to the existing `initTables` migration block in `internal/database/database.go`.

New table:

```sql
CREATE TABLE IF NOT EXISTS bot_sessions (
    platform        TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    conversation_id TEXT,
    current_mode    TEXT,                  -- NULL/"" = inherit global; "single" | "multi"
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (platform, user_id),
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL
);
```

New column on `conversations`:

```sql
ALTER TABLE conversations ADD COLUMN platform TEXT;  -- NULL for web origin, "telegram" for bot
```

SQLite has no `ADD COLUMN IF NOT EXISTS`, so probe `PRAGMA table_info(conversations)` first and `ALTER` only if absent. Idempotent on repeat boots.

### `internal/database/bot_sessions.go` (new file)

```go
type BotSession struct {
    Platform       string
    UserID         string
    ConversationID string
    CurrentMode    string  // "" | "single" | "multi"
    UpdatedAt      int64
}

func (d *DB) GetBotSession(platform, userID string) (*BotSession, error)         // nil + nil err on miss
func (d *DB) UpsertBotSession(platform, userID, conversationID, mode string) error
func (d *DB) ClearBotSession(platform, userID string) error                       // wipes whole row (mode reset)
func (d *DB) SetBotMode(platform, userID, mode string) error                      // for `mode` command
```

`ClearBotSession` wipes the entire row (including any prior `current_mode` override). `clear` is "I want a fresh start"; mode override is also reset to global default. Reduces user surprise.

### Conversation API extensions (`internal/database/conversations.go`)

```go
func (d *DB) CreateConversationWithPlatform(title, platform string) (*Conversation, error)
// existing CreateConversation keeps working (platform = NULL)

func (d *DB) ListConversations(platform string) (...)
// platform = "" → no filter; otherwise WHERE platform = ?
```

### `internal/handler/robot.go` rewrites

`RobotHandler` removes the `sessions map[string]string` field. Reads bot session from DB at the start of every `HandleMessageStream`. (Telegram update throughput is bounded by the agent loop's latency; per-message DB read overhead is negligible.)

Implements `StreamingMessageHandler`:

```go
func (h *RobotHandler) HandleMessageStream(
    platform, userID, text string,
    progressFn func(eventType, message string),
) (string, error)
```

Existing `HandleMessage` (sync, no progress) becomes a thin wrapper that builds a no-op `progressFn` and calls `HandleMessageStream`.

The handler:
1. Resolves session via `GetBotSession`; creates a new conversation + `UpsertBotSession` if absent.
2. Dispatches slash commands. New: `mode <single|multi|default>`.
3. Computes effective mode: `session.CurrentMode || h.config.MultiAgent.RobotUseMultiAgent` global.
4. Acquires `tasks.StartTask(conversationID, ...)` lock. If `ErrTaskAlreadyRunning`, replies "⚠️ Another task is running for this chat. Say `stop` to cancel." and returns.
5. Calls `ProcessMessageForRobot(ctx, text, conversationID, forceMode, progressFn, ...)`.
6. Returns final response string.

### `mode` slash command

```
mode               → reply current effective mode + override status
mode single        → SetBotMode(..., "single") → reply "✅ This chat is now single-agent."
mode multi         → if !MultiAgent.Enabled: reply "Multi-agent disabled in config; ask operator."
                     else: SetBotMode(..., "multi") → reply "✅ This chat is now multi-agent."
mode default       → SetBotMode(..., "") → reply "↩️ Reverted to global default ({single|multi})."
mode <other>       → reply "Unknown mode '<other>'. Use: mode single | mode multi | mode default"
```

### `ProcessMessageForRobot` modifications (`internal/handler/agent.go`)

Adds two parameters:

- `forceMode string` — `""` to use global logic; `"single"` or `"multi"` to force.
- `progressFn func(eventType, message string)` — for streaming progress to bot. Nil = silent.

Body adds, in order:

1. Compute effective mode:
   ```go
   var useMulti bool
   switch forceMode {
   case "multi":
       useMulti = true
   case "single":
       useMulti = false
   default:  // "" or unrecognized — fall back to global
       useMulti = h.config.MultiAgent.Enabled && h.config.MultiAgent.RobotUseMultiAgent
   }
   ```
   Caller (the `mode` command dispatcher) is responsible for validating the user-typed value before calling; this fallback is defensive against future drift.
2. `taskStatus := "completed"`
3. `h.debugSink.StartSession(conversationID)`
4. `defer func() { h.debugSink.EndSession(conversationID, taskStatus) }()`
5. `assistantMessageID` is created early (existing logic) and passed via `debug.WithCapture`:
   ```go
   ctx = debug.WithCapture(ctx, conversationID, assistantMessageID, 0, 0,
       map[bool]string{true: "cyberstrike-orchestrator", false: "single-agent"}[useMulti])
   ```
6. Branch on provider:
   - `EffectiveProvider() == "claude-cli" && h.claudeAdapter != nil` → `claudeAdapter.RunPrompt(...)` (mirror `AgentLoopStream:1263-1297` pattern).
   - Else if `useMulti` → `multiagent.RunOrchestrator(...)` with `progressFn` adapted via the wrapper.
   - Else → `h.agent.AgentLoopWithProgress(...)`.
7. On error: `taskStatus = "failed"` (read at defer-exec time per the named-return / closure pattern).

The `progressFn` wrapper inside the existing progress callback decides which events to surface to Telegram (per Q2):
- `iteration` → `"🤔 Round {n}: thinking…"`
- `tool_call` → `"🔧 Round {n}: calling {tool}…"`
- `tool_result` (success) → `"✅ Round {n}: {tool} done"`
- `tool_result` (error) → `"❌ Round {n}: {tool} failed"`
- `response_start` → `"✍️ Drafting answer…"`
- All other events (thinking_stream_delta, tool_result_delta, response_delta, sub-agent internal) → dropped.

### Telegram client integration (`internal/robot/telegram.go`)

The type-assert at L365 already exists:
```go
if streamer, ok := b.h.(StreamingMessageHandler); ok { ... }
```

Once `RobotHandler` implements the interface, this branch fires. The `progressFn` closure at L328-346 receives the events; throttler (existing in the closure or to be added if missing) edits the placeholder message at most every 3 s, last-write-wins on intermediate updates that arrive within the window.

### Conversation-list filter UI

`internal/handler/conversation.go` (or wherever `ListConversations` lives):
- Accept `?platform=` query param.
- SQL: `WHERE platform = ?` when non-empty; no filter otherwise.

`web/static/js/chat.js` conversation-list rendering:
- Filter dropdown above the list: "All / Web / Telegram" (i18n keys `conversations.filterAll`, `conversations.filterWeb`, `conversations.filterTelegram`).
- Per-card badge: small `🤖 Telegram` pill when `conversations.platform` is non-NULL. Renders independent of filter selection — "All" view still shows the badge so a glance distinguishes origin.

## Data flow

### Boot

`database.Init()` creates `bot_sessions` table; probes `conversations` schema and adds `platform` column if absent. Bot polls Telegram updates. `RobotHandler` constructed with `db` reference.

### First message from user 12345 (no existing session)

```
Telegram update  →  telegram.go:handleUpdate
                    │
                    ▼ (auth check passes)
                    streamer, _ := b.h.(StreamingMessageHandler)
                    streamer.HandleMessageStream("telegram", "12345", "scan example.com", progressFn)
                    │
                    ▼
RobotHandler.HandleMessageStream:
  • db.GetBotSession("telegram", "12345") → nil
  • db.CreateConversationWithPlatform("scan example.com", "telegram") → conv-abc
  • db.UpsertBotSession("telegram", "12345", "conv-abc", "")
  • text not a slash command → tasks.StartTask(conv-abc, ...) (acquires lock)
  • call ProcessMessageForRobot(ctx, text, conv-abc, forceMode="", progressFn)
  │
  ▼
ProcessMessageForRobot:
  • effective mode = "" || RobotUseMultiAgent → "single"
  • taskStatus := "completed"
  • h.debugSink.StartSession("conv-abc")
  • defer h.debugSink.EndSession("conv-abc", taskStatus)
  • ctx = debug.WithCapture(ctx, "conv-abc", assistantMsgID, 0, 0, "single-agent")
  • EffectiveProvider() == "openai" → AgentLoopWithProgress
  • result.Response → returned string
  • return final text + nil err

RobotHandler returns to telegram.go.
telegram.go does final editMessageText replacing the placeholder.

Side effects:
  - 1 debug_sessions row (outcome=completed)
  - 1 conversations row (platform="telegram")
  - N messages rows
  - debug_llm_calls populated, debug_events populated
  - bot_sessions row keyed (telegram, 12345) → conv-abc, mode=""
```

### Subsequent message (session exists)

```
RobotHandler.HandleMessageStream:
  • db.GetBotSession → {conv-abc, mode=""}
  • Continue same conversation thread.
  • New debug_session row (one per turn — matches web UI per-turn semantics).
```

### `mode multi` command

```
RobotHandler.HandleMessageStream:
  • text matches "mode " → command branch
  • parse arg: "multi"
  • config.MultiAgent.Enabled? if no → reply "Multi-agent is disabled in config; ask operator." Stop.
  • db.SetBotMode("telegram", "12345", "multi")
  • reply "✅ This chat is now multi-agent."
  • return (no agent loop invoked)
```

Next non-command message uses multi-agent path.

### `mode default` command

```
  • db.SetBotMode("telegram", "12345", "")
  • reply "↩️ Reverted to global default: single."
```

### `clear` command

```
  • db.ClearBotSession("telegram", "12345")    # wipes whole row including mode
  • reply "🗑️ Session cleared. Next message starts a new conversation."
```

### Streaming progress for one agent turn

While `ProcessMessageForRobot` is running, the agent's progress callback fires for many events. The bot-side wrapper in `progressFn` filters per the rule list above; throttler in `telegram.go` ensures ≥3 s between `editMessageText` calls. Last-write-wins; intermediate updates dropped if they arrive within the window. The placeholder message is mutated in place; a final replacement at the end shows the full answer.

### Provider routing (claude-cli mode)

```
ProcessMessageForRobot:
  • h.debugSink.StartSession(conversationID)
  • Build PromptOptions (intersect roleTools with MCPToolNames)
  • h.claudeAdapter.RunPrompt(ctx, ...) — single blocking call
  • progressFn fires once: "🤔 Running through Claude CLI…" then final answer
  • defer EndSession with outcome derived from RunPrompt's err
```

Claude CLI mode produces fewer per-event progress signals — its tool dispatch is opaque to us.

### Server restart mid-conversation

```
T1: User sends message → bot_sessions row created.
T2: Server restarts.
T3: User sends another message:
    RobotHandler.HandleMessageStream:
      • db.GetBotSession → {conv-abc, mode="multi"} — survived restart.
      • Continue same conversation, same mode.

Compare current behavior: T2 wipes RAM map; T3 creates fresh
conversation, user has no way to recover the old context.
This is the gap-5 fix.
```

### Conversation deleted from UI while user has active bot session

```
Operator deletes conv-abc via web UI.
FK ON DELETE SET NULL → bot_sessions.conversation_id becomes NULL,
  row stays, mode override preserved.
User's next message:
  • GetBotSession → {ConversationID="", Mode="multi"}
  • ConversationID empty → create new conversation, UpsertBotSession.
  • mode="multi" PRESERVED — operator-visible deletion doesn't reset
    user's mode override, only the conversation thread.
```

### Concurrent messages from same user

```
User fires message A; RobotHandler acquires tasks.StartTask(conv-abc).
User fires message B before A completes.
RobotHandler for B:
  • tasks.StartTask(conv-abc) → returns ErrTaskAlreadyRunning
  • reply "⚠️ Another task is running for this chat. Say `stop` to cancel."
  • return without calling ProcessMessageForRobot.

`stop` command (existing) cancels message A's context.
After A finishes (or is cancelled), B can be retried by the user.
```

### UI list with platform filter

```
GET /api/conversations?platform=telegram
  → SQL: SELECT … FROM conversations WHERE platform = 'telegram' ORDER BY …

Frontend: dropdown selects filter, fetches with ?platform= param.
Per-card badge always renders if conversations.platform is non-NULL,
regardless of filter — so "All" view still shows which conversations
came from where.
```

## Error handling

### DB write failures (debug bookends, bot_sessions writes)

Logged at `warn`, swallowed. Bot still replies. Same policy as web UI's debug failures.

### Telegram edit-rate-limit hit

Throttler holds back updates that arrive within the 3 s window. Last-write-wins. No retry queue — progress is ephemeral; missing one intermediate "🔧 calling nmap" message is acceptable. The final reply is unconditional.

### Claude CLI not installed but `provider: claude-cli`

`ProcessMessageForRobot`'s claude-cli branch errors via `RunPrompt`. Bot replies with the error string + a hint: "Claude CLI returned an error. Run prereq check from Settings → Provider." `taskStatus = "failed"` for the debug session.

### `bot_sessions.conversation_id` FK violation

Resolved at schema level via `ON DELETE SET NULL`. Documented in the data-flow section.

### Mode-command on disabled multi-agent

`mode multi` when `MultiAgent.Enabled = false`: bot replies "Multi-agent is disabled in this deployment. Ask operator to enable." Does NOT update `bot_sessions.current_mode`.

### Unrecognized mode arg

`mode foobar` → bot replies "Unknown mode 'foobar'. Use: mode single | mode multi | mode default". No DB write.

### `StreamingMessageHandler` type-assert fail

If the handler doesn't implement the streaming interface (future refactor regression), the existing fallback at `telegram.go:365` runs the synchronous path. Bot still works, just without progress edits.

### Server killed mid-message

`ProcessMessageForRobot` was running; placeholder "🤔 thinking" stuck on Telegram, no answer ever sent. On restart:
- `debug_sessions` row is orphan (NULL `ended_at`); `SweepOrphans` (PR #19 boot helper) marks it `outcome="interrupted"`.
- `bot_sessions` row keeps `conversation_id` linkage; user's NEXT message picks up the same conversation thread.
- The placeholder message is dangling in Telegram; user can `clear` and retry.

### Concurrent messages from same user

Per 4i: `tasks.StartTask` lock keyed on `conversationID`. Second message reply: "⚠️ Another task is running for this chat. Say `stop` to cancel."

## Testing

### Database layer (`internal/database/bot_sessions_test.go`)

- `TestBotSession_RoundTrip` — Upsert + Get round-trips fields.
- `TestBotSession_GetMissingReturnsNil` — non-existent (platform, user_id) returns `(nil, nil)`.
- `TestBotSession_SetMode_PreservesConversationID` — mode update doesn't clobber other fields.
- `TestBotSession_Clear_WipesRow` — after Clear, Get returns nil.
- `TestBotSession_FKCascade_OnConversationDelete` — delete parent conversation; row survives, `conversation_id` is NULL.
- `TestConversation_PlatformColumn_Filter` — list with platform filter returns only matching; empty filter returns all.

### Robot handler (`internal/handler/robot_test.go`)

- `TestRobotHandler_FirstMessage_CreatesSessionAndConversation`
- `TestRobotHandler_SubsequentMessage_ReusesConversation`
- `TestRobotHandler_ModeCommand_SetsRow`
- `TestRobotHandler_ModeCommand_RejectsWhenMultiDisabled`
- `TestRobotHandler_ModeCommand_DefaultRevertsOverride`
- `TestRobotHandler_ClearCommand_WipesSession`
- `TestRobotHandler_ConcurrentMessage_ReturnsLockedReply`
- `TestRobotHandler_ImplementsStreamingMessageHandler` — interface assertion.

### `ProcessMessageForRobot` integration (`internal/handler/agent_test.go`)

- `TestProcessMessageForRobot_StartEndSession_BookendsFire`
- `TestProcessMessageForRobot_ClaudeCLIRouting`
- `TestProcessMessageForRobot_ForceModeOverride`
- `TestProcessMessageForRobot_ProgressFnFiresOnMajorEvents`

### Conversation list filter (`internal/handler/conversation_test.go`)

- `TestListConversations_PlatformFilter`
- `TestListConversations_NoFilter_ReturnsAll`

### Telegram client throttler (`internal/robot/telegram_test.go`)

If the throttler is non-trivial: mock-clock test asserting only N edits in T seconds. If trivial wall-clock: skip and verify manually.

### Manual smoke checklist

1. Boot fresh; first message creates `bot_sessions` row + conversation.
2. `mode multi` switches; next message uses orchestrator; `mode single` switches back.
3. Server restart preserves conversation + mode.
4. Conversation deleted from UI; user's next message creates new conversation, mode override preserved.
5. Two messages back-to-back; second gets the locked reply.
6. UI conversation list dropdown filters correctly; per-card 🤖 badge visible.
7. Bot conversation appears in Settings → Debug; viewer + raw + ShareGPT export work.
8. Provider toggle to Claude CLI; bot's next message routes through `claudeAdapter.RunPrompt`.
9. Long agent run with multiple tool calls; placeholder edits at each major event with ≥3 s spacing.

### Out of scope for v1

- E2E browser test for the conversation-list filter — manual smoke covers it.
- Multi-platform integration (DingTalk/Lark/WeCom): table schema supports it via the `platform` string column, but no platform code lands.

## Implementation order (sketch; detailed plan follows in writing-plans)

1. DB migrations — `bot_sessions` table + `conversations.platform` column + idempotent probe.
2. `internal/database/bot_sessions.go` — CRUD methods + tests.
3. `ProcessMessageForRobot` modifications — bookends + `WithCapture` + claude-cli branch + force-mode param + progressFn.
4. `RobotHandler` rewrites — implement `StreamingMessageHandler`, drop in-memory map, wire `bot_sessions` reads, add `mode` command, add `tasks.StartTask` lock.
5. Telegram client wiring — confirm the existing `progressFn` closure at `telegram.go:328-346` is invoked once `RobotHandler` implements `StreamingMessageHandler`. Add the 3 s throttler if the closure doesn't already enforce it (read first; the closure may already have wall-clock throttling — verify before adding code).
6. Conversation list filter — backend `?platform=` param + frontend dropdown + per-card badge + i18n.
7. Toolchain gate + manual smoke.

Each step is committable on its own. Steps 1–2 are independent of each other and of the rest; steps 3–5 have ordering dependencies (3 must land before 4's `forceMode`/`progressFn` plumbing makes sense); step 6 is independent of 3–5 and could ship in parallel.
