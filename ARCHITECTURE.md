# Bot architecture

The Bot has four independent durable concerns:

1. A conversation job decides what work belongs to one inbound event and owns
   its lease, waits, retry, and terminal state.
2. A capability execution records one actual read or independently reversible
   mutation. It is the source of truth for result evidence and, for Agent
   mutations, confirmation and user-visible receipts.
3. An Agent checkpoint and physical-model-attempt counter make an interrupted
   tool loop resumable without granting a fresh lease or retry budget.
4. The outbox delivers already-finished output. Delivery failure can resend a
   message, but can never rerun a capability.

Protocol adapters, routing, orchestration, business capabilities, and delivery
depend inward in that order. A feature package never imports NapCat or QQ Bot.

## Complete durable state machine

```mermaid
stateDiagram-v2
    direction LR

    state "Conversation job" as Job {
        state "queued" as JQueued
        state "running" as JRunning
        state "waiting_confirmation" as JConfirm
        state "waiting_auth" as JAuth
        state "retry_wait" as JRetry
        state "completed" as JCompleted
        state "failed" as JFailed
        state "expired" as JExpired
        state "cancelled" as JCancelled

        [*] --> JQueued: accepted inbound event
        JQueued --> JRunning: FIFO claim + new lease
        JRetry --> JRunning: retry due + new lease

        JRunning --> JConfirm: Agent checkpoint + pending non-read
        note right of JConfirm
          Direct commands never enter this state.
          Agent reads never enter this state.
        end note
        JConfirm --> JQueued: approve or deny exactly one operation\nrevision++

        JRunning --> JAuth: operation proved that login is required
        JAuth --> JQueued: exact required scopes are current\nrevision++

        JRunning --> JRetry: durable run, transcript, checkpoint,\nor output persistence failed; live lease recovery
        JRunning --> JCompleted: final outbox + any Agent receipt states\ncommitted atomically
        JRunning --> JFailed: irrecoverable contract/run failure

        JQueued --> JExpired: TTL elapsed
        JRetry --> JExpired: TTL elapsed
        JConfirm --> JExpired: TTL elapsed
        JAuth --> JExpired: TTL elapsed
        JRunning --> JExpired: TTL elapsed

        JQueued --> JCancelled: explicit cancellation
        JRetry --> JCancelled: explicit cancellation
        JConfirm --> JCancelled: explicit cancellation
        JAuth --> JCancelled: explicit cancellation
        JRunning --> JCancelled: explicit cancellation wins lease CAS

        JCompleted --> [*]
        JFailed --> [*]
        JExpired --> [*]
        JCancelled --> [*]
    }

    state "Capability execution" as Capability {
        state "awaiting_confirmation" as CAwaiting
        state "approved" as CApproved
        state "running" as CRunning
        state "waiting_auth" as CAuth
        state "succeeded" as CSucceeded
        state "failed" as CFailed
        state "unknown" as CUnknown
        state "denied" as CDenied
        state "cancelled" as CCancelled
        state "expired" as CExpired

        [*] --> CAwaiting: Agent write/destructive preflight
        [*] --> CApproved: direct command write/destructive prepared
        [*] --> CRunning: direct command read or Agent read

        CAwaiting --> CApproved: real user confirms
        CAwaiting --> CDenied: real user denies
        CApproved --> CRunning: current job lease claims direct\nor confirmed operation

        CRunning --> CAuth: no effect occurred; login required
        CAuth --> CRunning: current job lease reclaims after auth

        CRunning --> CSucceeded: exact domain result
        CRunning --> CFailed: definite domain failure\nor owning job fails during a read
        CRunning --> CUnknown: mutation may have reached external service\nor owning job stops after it began; never auto-retry
        CRunning --> CRunning: recovered read may run again safely

        CAwaiting --> CFailed: owning job fails
        CApproved --> CFailed: owning job fails before execution
        CAuth --> CFailed: owning job fails
        CAwaiting --> CCancelled: owning job cancelled
        CApproved --> CCancelled: owning job cancelled before execution
        CAuth --> CCancelled: owning job cancelled
        CRunning --> CCancelled: owning job cancelled during a read
        CAwaiting --> CExpired: owning job expired
        CApproved --> CExpired: owning job expired before execution
        CAuth --> CExpired: owning job expired
        CRunning --> CExpired: owning job expired during a read

        CSucceeded --> [*]
        CFailed --> [*]
        CUnknown --> [*]
        CDenied --> [*]
        CCancelled --> [*]
        CExpired --> [*]
    }

    state "Agent checkpoint" as Checkpoint {
        state "none" as KNone
        state "stored" as KStored
        state "deleted" as KDeleted
        [*] --> KNone
        KNone --> KStored: Eino interrupt writes opaque checkpoint\nwith job revision + lease
        KStored --> KStored: resumed revision atomically adopts checkpoint
        KStored --> KDeleted: exact terminalizing lease acknowledged\nmatching job revision
        KStored --> KDeleted: owning job cancelled or expired\ninside the terminal transaction
        KNone --> KNone: ordinary run without interrupt
        KDeleted --> [*]
    }

    state "Physical provider attempts" as Provider {
        state "reserved" as PReserved
        state "in_flight" as PInFlight
        state "backoff" as PBackoff
        state "accepted" as PAccepted
        state "rejected" as PRejected
        state "exhausted" as PExhausted
        [*] --> PReserved: atomically reserve attempt in SQLite\nreservation failure sends no HTTP request
        PReserved --> PInFlight: send HTTP request
        PInFlight --> PAccepted: successful provider response
        PInFlight --> PBackoff: 408 / 429 / 5xx / retryable transport /\nprovider-side cancel while caller remains live; attempts below 5
        PBackoff --> PReserved: bounded Retry-After / jitter delay
        PInFlight --> PRejected: permanent provider response
        PInFlight --> PExhausted: fifth retryable attempt failed
        PAccepted --> [*]
        PRejected --> [*]
        PExhausted --> [*]
    }

    state "Delivery outbox" as Outbox {
        state "pending" as OPending
        state "delivering" as ODelivering
        state "retry_wait" as ORetry
        state "accepted" as OAccepted
        state "rejected" as ORejected
        state "unknown" as OUnknown
        state "expired" as OExpired
        [*] --> OPending: response persisted
        OPending --> ODelivering: delivery worker claim
        ORetry --> ODelivering: retry due
        ODelivering --> OAccepted: platform accepted
        ODelivering --> ORetry: definite retryable failure\nattempts below 5
        ODelivering --> ORejected: permanent failure or budget exhausted
        ODelivering --> OUnknown: platform outcome ambiguous\nnever auto-retry
        ODelivering --> OUnknown: worker died after send began
        OPending --> OExpired: message TTL elapsed
        ORetry --> OExpired: message TTL elapsed
        OAccepted --> [*]
        ORejected --> [*]
        OUnknown --> [*]
        OExpired --> [*]
    }

    JConfirm --> CAwaiting: one pending Agent operation is shown
    JAuth --> CAuth: current configured scopes verified
    JRunning --> PReserved: model request
    JRunning --> OPending: host confirmation, auth, or final output
    JCompleted --> OPending: final output already committed
    JExpired --> CExpired: pending operations terminalized
    JCancelled --> CCancelled: pending operations terminalized
```

The same names appearing in different composite blocks are separate states.
For example, an outbox `unknown` never changes a completed conversation job,
and a capability `unknown` never authorizes an automatic mutation retry.

Every arrow out of `running` is fenced by the current, unexpired conversation
job lease. A terminal transition and the terminalization check for all of its
capability executions happen in one SQLite transaction. `completed` refuses to
commit while a current operation is nonterminal; failure, expiry, and
cancellation atomically close the entire operation batch.

## Inbound and routing flow

1. A platform adapter translates a protocol event into `message.Inbound`.
2. `routing.Decide` classifies activation and privacy before persistence.
   Ambient shared-chat text and retired `/life...` paths are discarded here,
   before natural-language matching or Agent fallback.
3. `botapp` stores the exact route and normalized invocation. Execution uses
   that stored decision; it does not reparse or fall through to another route.
4. One actor/conversation lane is processed FIFO. A waiting confirmation or
   login therefore cannot be overtaken by a later event from the same lane.
5. Responses are transactionally written to the outbox. Platform I/O happens
   later and has no path back to capability execution.

In shared conversations, only strict public commands, high-confidence public
natural routes, mentions, or verified replies activate the Bot. Personal
capabilities are unavailable even if a model asks for them. An addressed
natural-language request whose registry match requires personal data receives
a deterministic private-chat redirect without a provider call. `message.Actor`
identifies who caused an event; `message.Conversation` is only the delivery
address, and the two are never substituted for each other.

## Capability authorization and confirmation contract

`internal/commands` owns one descriptor registry. A descriptor defines the
stable ID, accepted forms, normalization, validation, effect, privacy scope,
executor, result presentation, receipt, and help data. Direct commands and
Agent calls use the same descriptor and executor; the calling host owns the
authorization decision.

The rule is entirely origin-and-effect based:

- A command explicitly typed by the user executes immediately, whether it is
  a read, write, or destructive action. Its durable execution row prevents an
  output retry from replaying the operation, but it never waits for another
  confirmation and never emits an operation receipt.
- An Agent read executes immediately. The host does not ask the model or user
  for permission.
- An Agent write or destructive operation is preflighted after the model calls
  the capability and before any side effect. A grouped request is stored
  atomically as independent operations.
- A missing login never starts an adjacent write on behalf of an Agent read.
  The read returns a literal instruction to send the direct `登录` command and
  retry. An explicit Agent login call is itself a write and follows the same
  confirmation gate.
- Exactly one independently reversible operation is shown and decided at a
  time. Approval only changes `awaiting_confirmation` to `approved`; the
  current conversation-job lease must still claim it before execution.
- A stale worker cannot use a newer lease. A running mutation from an older
  lease becomes `unknown`; a running read may be repeated.
- Capability finalization, auth deferral, and Agent receipt updates require
  the same live job lease that claimed the operation. An elapsed job TTL
  fences the worker immediately; it does not wait for the periodic expiry
  sweep.
- Confirmation prompts, affirmative user replies, and receipt lines are not
  model-visible conversation events. Approval resumes the interrupted tool
  with only its literal domain result. A denial is fed back as a typed tool
  denial because that is relevant evidence for the model's next response.

Capability results have a typed host status, but the model receives the
descriptor's literal result text. There is no generic `{ok,status,text}`
wrapper. Authentication is also host-owned: the model neither handles a code
nor turns a login message into evidence of a successful operation.

## Tool discovery

The model sees stable meta-tools rather than the entire command and MCP
catalog:

- `search_bot_commands` searches descriptor-backed documentation and returns
  exact capability IDs, arguments, examples, effect, and scope. Confirmation
  mechanics are intentionally absent.
- `invoke_bot_capability` executes one normalized descriptor through the host
  state machine.
- `search_campus_tools` and `call_campus_tool` lazily initialize MCP only when
  supplementary campus data is needed. Only exact tool names in the host-owned
  read allowlist are exposed; remote MCP annotations cannot grant access.

Search ignores generic request verbs, politeness, and standalone numbers. A
fuzzy Chinese match needs domain-bearing evidence in the capability ID, title,
accepted forms, or multiple documentation fragments; words such as “查询”,
“列出”, and “列表” cannot make an unrelated command look relevant. A valid empty Bot search
result is evidence that the registry has no match and allows the model to try
MCP. A malformed search result is not evidence and cannot unlock invocation.

An explicit request for the complete command/tool/capability inventory bypasses
the model. The host constructs the response from the current descriptor
registry, its fixed meta-tools, and the intersection of the live remote MCP
catalog with the host read allowlist. This keeps the answer complete and
truthful even when a model would otherwise browse one tool family at a time.
The shortcut requires explicit inventory wording and refuses mutation wording,
so it cannot preempt the normal confirmation path for an operation.

The compact system instruction tells the model to search before invoking and
to preserve all user constraints. Mutation improvisation through MCP is not
possible.

For a personal-data request, a verification follow-up, or an explicit public
Bot capability request, the runtime also enforces the sequence instead of
relying on the instruction alone. The first provider request is offered only
`search_bot_commands`; after a nonempty result, the next request is offered
only `invoke_bot_capability`. A capability is accepted only when its ID is the
returned descriptor ID or one of that descriptor's executable examples.
Provisional assistant text is neither persisted nor eligible for delivery. If
the provider returns prose instead of
the sole offered tool, the host makes one additional tool-only semantic
attempt. If that still produces no evidence, the deterministic failure shown
to the user is also persisted as the assistant turn. A successful relevant
result unlocks the final answer; a relevant failure, unknown outcome, or denial
is returned to the user as its literal tool result rather than allowing later
model prose to turn it into a success claim.

Known supplementary campus domains use the same host-enforced evidence chain.
For a second-classroom lookup, completion of the required Bot search always
forces an MCP search, even if a loosely reformulated Bot search happened to
return unrelated documentation. The model is not offered Bot invocation for
that turn. A nonempty MCP search then forces `call_campus_tool`, and only a name
returned by that search can satisfy the turn. The model may format the literal
read result, but it cannot replace any required stage with an unsupported
factual answer.

## Exact conversation evidence

`conversation_events` stores only typed model-visible transcript events:

- `user`, including the exact timestamp-prefixed text and supported image
  parts sent to the provider;
- `assistant`, including exact tool call IDs, names, and arguments;
- `tool_result`, `tool_error`, and `tool_denial`, including the literal text
  returned to the model.

Old complete user turns may be dropped to fit the history window, but retained
events are never summarized, rewritten, or converted into another role. Host
approval messages and receipt lines are deliberately absent. No time-based
placeholder message exists. Private URLs may appear in direct-chat tool results
and this private database history; they are forbidden on shared surfaces and
redacted from process logs.

Persisted assistant image-output parts remain private evidence but are not
replayed because the configured OpenAI-compatible adapter cannot encode them
in assistant history. Exact assistant text and tool calls are preserved; when
both `Content` and distinct text parts exist, both are replayed as text parts
so the adapter cannot silently discard `Content`.

Every event emitted by an Agent run is appended only if its job ID, revision,
state, and lease still match the running coordinator claim in the same
transaction. A late provider response therefore cannot write transcript into a
resumed turn. Unsupported provisional assistant text from a grounded turn is
not an emitted event. Direct-command routes do not manufacture an Agent tool
exchange: their actual user-visible domain response is stored as an
`assistant` event; Agent routes preserve the real assistant/tool-call/tool-result
roles exactly.

## User-visible Agent receipts

The coordinator queues no time-based placeholder. A user-visible output exists
only when the job has a confirmation request, an authentication instruction,
or a real final result to persist.

Direct commands never show receipt lines: their command response is already
the complete user-visible result. For Agent turns, receipt lines are derived
only from actual capability-execution rows, never from model prose or intent.
Meta-tool searches produce no receipt. Agent reads show a receipt when a real
domain/MCP read ran, including an empty or failed read; duplicate identical
lines are collapsed. Examples:

```text
#已查询课程{数学分析（程艺，2026春）}
#待确认订阅课程{数学分析（程艺，2026春）}
#已订阅课程{数学分析（程艺，2026春）}
#订阅课程失败{数学分析（程艺，2026春）：操作结果未知，系统没有自动重试}
```

Receipt state is committed in the same transaction as the corresponding
outbox output and conversation-job transition. A database failure retries the
job without losing or duplicating the operation.

## Retry and delivery invariants

- Each logical provider request gets at most five physical HTTP attempts. One
  Agent job gets at most 65 physical attempts in total (12 tool calls plus a
  final model turn, each with that retry window) across authentication resumes,
  confirmation resumes, and process restarts. The Agent-run row and each
  attempt reservation must commit durably before network I/O; persistence
  failure retries the job without contacting the provider.
- The whole Agent run remains bounded by two minutes. Its conversation-job
  lease is three minutes, leaving one minute for final persistence and outbox
  commit before stale-lease recovery can begin. Retry-After and jittered
  exponential delays are capped by the remaining run deadline.
- If a provider transport reports `context.Canceled` while the caller context
  is still live, including while a successful-status response body is being
  read, it is treated as a retryable upstream interruption. A truncated body is
  retryable too. A genuine caller cancellation remains silent; exhausting
  upstream retries produces a user-visible failure with the durable run ID
  instead of dropping the reply.
- A deterministic outbox key makes output persistence idempotent. Business
  work and final output/receipt state commit together.
- A definite mutation transport timeout is `unknown`, not `failed`, because
  replay could duplicate an external effect. Reads remain safe to repeat.
- Message polling and stale-lease recovery have separate cadences. The
  coordinator claims ready work promptly, but runs the heavier recovery sweep
  once at startup and then once per second.
- The delivery worker likewise sweeps abandoned `delivering` rows at startup
  and every 30 seconds. Once a send has been in flight for five minutes, its
  outcome becomes `unknown` instead of remaining stuck or being replayed.
- The process uses one SQLite connection. This matches SQLite's single-writer
  model and prevents deferred read-to-write transactions from failing with an
  unrecoverable stale snapshot; `busy_timeout` remains enabled for coordination
  with deployment backup and other external processes.
- Delivery adapters report accepted, retryable, rejected, or unknown. They do
  not write business state or interactions.
- An unsupported target platform is rejected rather than guessed. Rendering
  finishes before enqueue; delivery receives immutable text/attachments.

## Schema release boundary

Schema version 2 is the only accepted persisted shape. A genuinely empty
SQLite database may be initialized directly at version 2; a nonempty
unversioned database, any other version, obsolete tables or columns, and
malformed indexes are rejected instead of migrated or repaired. Legacy
interaction replies are never imported into model history. Every required
column and declared index (including order, uniqueness, and the absence of a
narrowing partial predicate) is checked before the process becomes ready.
Deployment takes an immutable backup and restores it if the new container or
post-start schema audit fails.

## Module ownership

- `internal/message`: protocol-neutral message values.
- `internal/routing`: pure activation, privacy, and route selection.
- `internal/botapp`: durable conversation orchestration and output boundary.
- `internal/commands`: descriptor registry and domain capabilities.
- `internal/agent`: model loop, tool discovery, exact transcript, checkpoint,
  provider budgets, and tool evidence.
- `internal/auth`, `internal/feedback`, `internal/notify`: feature state owned
  independently of conversation and delivery state.
- `internal/delivery`: platform selection and outbox worker.
- `internal/napcat`, `internal/qqbot`: protocol parsing and platform I/O.
- `internal/store`: SQLite CAS transitions and transactional outbox writes.
- `cmd/life-ustc-bot`: dependency composition and process lifecycle only.

## Image rendering

Image responses are rendered by the `renderd` sidecar. The bot keeps domain
semantics in Go, sends a validated JSON envelope to
`BOT_RENDER_ENDPOINT`, and receives a PNG from the Rust/Typst renderer. The
Compose deployment starts `renderd` first and waits for its `/healthz` probe;
the bot does not silently retry with a legacy renderer when the endpoint is
unavailable, so an operational failure remains visible while the normal text
response is preserved.

All card families use the shared Typst style: a 390-point canvas with a minimum
844-point height, 20-point side margins, 34-point large titles, 17-point body
text, and 13-point captions. Grouped surfaces and restrained color establish
the hierarchy of a static iPhone information screen. Tables and paragraphs
wrap to their available width, and schedules use day sections so course names
stay readable on a phone. Typst measures the content to choose the page height,
preserving at least one screen and placing the footer at its bottom. Longer
content extends the page instead of shrinking the text. The renderer checks
the actual page dimensions before rasterization and rejects multi-page cards
instead of silently dropping content.

`renderd` rasterizes at 3x by default, producing images at least 1170×2532 pixels.
`RENDERD_SCALE` can be set between 1x and 4x, and a request may provide a
per-request scale. Its runtime image includes Fira Code and Noto CJK. The Typst
world exposes only embedded card templates; it cannot read files or access the
network during compilation.

For local development, start `renderd` and point the bot at
`http://127.0.0.1:9123/render` with `BOT_RENDER_ENDPOINT`, or render all card
examples with:

```sh
RENDERD_ADDR=127.0.0.1:9123 renderd/target/release/renderd
go run ./cmd/render-examples -endpoint http://127.0.0.1:9123/render -out /tmp/bot-examples
```

The seven fixtures use a fixed clock and sample data, including departure
highlights and the current schedule day. The command writes original PNGs and
a gallery with 390×844-pixel scrollable viewports, and exits nonzero on a render,
output-write, image-width, or minimum-height failure. CI renders every fixture against the release-built sidecar
and uploads the gallery and images as the `typst-phone-examples` artifact.
