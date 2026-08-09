# Bot architecture

The bot separates protocol transport, application orchestration, business
features, and durable delivery. Dependencies point inward; feature packages do
not import NapCat or QQ Bot implementations.

## Message flow

1. A platform adapter translates a protocol event into `message.Inbound`.
2. `botapp` selects commands or the agent and records the interaction once.
3. Interactive replies use `delivery.Service.DeliverNow` because the user is
   waiting and the source event supplies the reply context.
4. Background business features create `message.Outbound` records in the
   SQLite outbox. The delivery worker owns retry, expiry, and recovery.
5. A platform delivery adapter is selected by an exact platform key and is the
   only component that converts `message.Conversation` into a protocol target.

`message.Actor` identifies the user who caused an event. A
`message.Conversation` is a delivery address. They must not be treated as the
same identity, especially in groups.

## State ownership

- Login owns `pending`, `approved`, `expired`, `denied`, `invalid`, and
  `superseded`. Delivery failures never change login state.
- A pending confirmation belongs to one conversation and is consumed,
  cancelled, or expired independently of other conversations for the user.
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
- `internal/botapp`: inbound use-case orchestration.
- `internal/commands`, `internal/agent`: user-facing business behavior.
- `internal/auth`, `internal/feedback`, `internal/notify`: scoped feature
  services and their own state transitions.
- `internal/delivery`: routing and durable-delivery state machine.
- `internal/napcat`, `internal/qqbot`: protocol parsing, acknowledgements, and
  platform I/O.
- `internal/store`: SQLite persistence and transactional outbox operations.
- `cmd/life-ustc-bot`: composition and process lifecycle only.
