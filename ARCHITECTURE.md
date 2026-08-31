# Bot architecture

The bot separates protocol transport, application orchestration, business
features, and durable delivery. Dependencies point inward; feature packages do
not import NapCat or QQ Bot implementations.

## Message flow

1. A platform adapter translates a protocol event into `message.Inbound`.
2. `routing.Decide` classifies the conversation surface and activation before
   persistence. Ambient shared-chat text is discarded here.
3. `botapp` persists the structured route and invocation, then executes that
   exact decision from the durable job. It does not reparse or fall back.
4. All replies become `message.Outbound` records in the SQLite outbox. The
   delivery worker owns retry, expiry, and recovery.
5. A platform delivery adapter is selected by an exact platform key and is the
   only component that converts `message.Conversation` into a protocol target.

## Shared-conversation routing

- A strict public command or a high-confidence public natural query activates
  Presto without an @. Matching consumes the whole supported grammar; keyword
  containment is never an activation rule.
- A mention, verified reply to an accepted Bot message, or platform
  interaction activates broader Agent handling.
- User-private capabilities never execute in groups or channels. An explicit
  private command receives a private-chat redirect; unaddressed private natural
  language is ignored.
- Replies may carry a structured public `ResponseContext`. A verified reply
  can apply a narrow deterministic follow-up such as changing only a bus date.
- NapCat receives ambient group traffic and therefore uses the full gate. QQ's
  official group event source normally supplies only addressed messages, but
  both transports use the same router.

## Command contract

`internal/commands` owns one capability descriptor registry. Each descriptor
defines its stable ID, user-facing forms, argument validation, policy,
executor, result exposure, and help metadata.

- `ParseCommand` has three outcomes: valid capability, invalid arguments, or
  unknown text. Invalid known commands return usage and never fall through to
  the LLM.
- Help and Agent calls share `CapabilityUsageExample`. Its display command and
  normalized `capability`/`arguments` pair cannot drift independently.
- Direct commands, natural-language routes, group routes, and Agent calls all
  pass through the descriptor's normalizer and validator.
- Capability policy separates `public` from `user_private`; read-only personal
  data is never treated as group-safe.
- Agent capability results use one envelope: `ok`, `status`, safe `text`, and
  optional executable `suggestedCalls`. Authentication and private host-only
  values are delivered by the Coordinator, not exposed to the model.
- MCP tools supplement local capabilities. Failure to initialize MCP never
  removes the host capability tool.

`message.Actor` identifies the user who caused an event. A
`message.Conversation` is a delivery address. They must not be treated as the
same identity, especially in groups.

## State ownership

- Login owns `pending`, `approved`, `expired`, `denied`, `invalid`, and
  `superseded`. Delivery failures never change login state.
- A pending confirmation belongs to one actor's conversation lane and is
  consumed, cancelled, or expired independently of other group members.
- Agent runs own `started`, `completed`, `failed`, `ignored`, and
  `interrupted`. Startup closes runs left open by a previous process.
- Delivery owns `pending`, `delivering`, `accepted`, `retry_wait`, `rejected`,
  `unknown`, and `expired`. `unknown` is terminal for automatic processing so
  an uncertain platform result cannot produce a duplicate message.
- Feedback owns `open` and `resolved`. Admin notification delivery is a
  separate outbox record.

Preferences, credentials, profiles, and notification switches are current
configuration or data, not state machines.

## Delivery invariants

- Durable messages require a deterministic deduplication key.
- A business record and its durable messages are created in one transaction
  when losing the message would make the operation incomplete.
- Platform adapters return an accepted receipt, a retryable failure, a
  permanent rejection, or an uncertain result. They do not write interactions
  or outbox records.
- There is no single-platform fallback. An unregistered target platform is
  rejected instead of being guessed.
- Rendering occurs before a durable message is enqueued. Delivery adapters
  receive immutable URL or byte attachments and only upload/reference them.

## Module responsibilities

- `internal/message`: protocol-neutral value types only.
- `internal/routing`: pure activation, privacy, and command/Agent selection.
- `internal/botapp`: inbound use-case orchestration.
- `internal/commands`, `internal/agent`: user-facing business behavior.
- `internal/auth`, `internal/feedback`, `internal/notify`: scoped feature
  services and their own state transitions.
- `internal/delivery`: routing and durable-delivery state machine.
- `internal/napcat`, `internal/qqbot`: protocol parsing, acknowledgements, and
  platform I/O.
- `internal/store`: SQLite persistence and transactional outbox operations.
- `cmd/life-ustc-bot`: composition and process lifecycle only.
