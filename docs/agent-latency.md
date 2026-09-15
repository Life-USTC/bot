# Agent latency investigation (2026-09-15)

The Bot enables Eino model streaming (`RunnerConfig.EnableStreaming`). The
transport passes SSE bytes incrementally to the model SDK and observes the final
usage event, including provider cache fields. `adk.GetMessage` assembles each
complete message before its tool arguments are executed or its answer is saved.
Interrupted streams fail visibly and are not automatically replayed after body
consumption begins. Caller cancellation and run completion release stream work;
there is no whole-run timeout or generation cap.

Streaming reception does not mean token-by-token QQ delivery. The coordinator
commits complete responses through the durable Outbox. Text, images and the final
operation receipt are grouped for delivery where the platform supports it;
destructive-operation confirmations remain individual messages. This prioritizes
one useful reply over a stream of partial messages.

The configured production model endpoint supports SSE and returns usage with
`stream_options.include_usage`. A synthetic probe used the same 126-token input
in non-streaming / streaming / streaming / non-streaming order. It sent no Bot
messages and performed no business operations.

| Trial | Mode | First text received by probe | Total | Cached input | Output tokens |
| --- | --- | --- | --- | --- | --- |
| 1 | JSON | on completion | 22.198 s | not reported | 234 |
| 2 | SSE | 7.264 s | 12.078 s | 126 | 225 |
| 3 | SSE | 8.501 s | 13.522 s | 126 | 263 |
| 4 | JSON | on completion | 13.382 s | 126 | 234 |

This is a small diagnostic sample, not a production benchmark. Output lengths,
reasoning and cache state vary. The warm JSON request and SSE requests have
similar total latency; SSE exposes text earlier. Comparing the cold first JSON
request only with the warm SSE requests would incorrectly attribute all of the
difference to streaming.

## Production sample

The latest 20 recorded runs at the time of inspection matched 20 completion
log entries (13 completed, 3 failed, 4 interrupted). This sample spans multiple
recent deployments and includes summary work and interrupted runs; it is not a
clean post-change benchmark or a user-perceived queue/delivery latency measure.

| Metric | Aggregate |
| --- | --- |
| Logged run duration | 1,398.231 s |
| Model requests, including retries | 1,283.704 s (91.8%) |
| Tool stage | 112.776 s (8.1%) |
| Tool setup | 0.014 s |
| History loading | 0.284 s |
| Physical model attempts / tool calls | 66 / 30 |
| Prompt / reported cached tokens | 2,383,682 / 1,325,568 (55.61%) |
| Largest estimated context | 88,301 tokens |

The run counters are in `agent_runs`; stages are completion-log fields, not
SQLite columns. Capability `started_at` to `finished_at` totals 84.109 s for
30 complete records; this is narrower than the tool stage. Waiting for human
confirmation between runs and time before the run starts are not included.
The main measured bottleneck is model processing/transport, so global parallel
tool execution is not justified by this sample.

## Measurement and next improvements

The existing `model_request` stage includes complete physical attempts and
retry waits. `model_response_headers` now measures each transport's wait for
headers; `model_response_body` measures consuming and closing its successful
body. These nested stage totals must not be added to `model_request`. In JSON
mode headers can arrive after generation, so header timing is not time to first
token and does not isolate inference from network latency. Usage and cache-hit
tokens remain the provider's reported values.

Preserve static instructions/tool definitions and append stable history. Do not
refresh timestamps in the prompt prefix, repeatedly trim history, remove tool
documentation, or impose output/runtime limits to make a latency chart look
better. Semantic compaction remains triggered near context capacity.

`model_first_text` measures the time from a physical attempt's start to its
first nonempty assistant text delta. For multiple model calls the log contains
that duration's sum, not a user-facing time-to-first-reply measurement. It excludes
reasoning-only and tool-only deltas. `model_request` for SSE includes reading and
closing the body, rather than stopping at response headers. Stream usage events
are counted once. Tests cover incremental delivery, complete fragmented tool
arguments, truncated streams, body closure, receipts and checkpoint resumption.

Independent reads could overlap when tool latency is significant. This requires
separating read concurrency from ordered writes and confirmation queues; turning
off Eino's sequential execution globally would also parallelize mutations. Use
production stage measurements before making that tradeoff. Fewer unnecessary
model/tool round trips and cache reuse can reduce total time; no mandatory tool
sequence or model-quality reduction is introduced by this investigation.

References: [Eino Runner and streaming configuration](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/),
[Kimi streaming and usage protocol](https://platform.kimi.com/docs/api/chat),
[Kimi automatic context caching](https://www.kimi.com/help/kimi-api/api-troubleshooting).
