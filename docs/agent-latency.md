# Agent latency investigation (2026-09-15)

The Bot currently uses non-streaming model generation. Eino's `RunnerConfig`
leaves `EnableStreaming` false, `adk.GetMessage` consumes a complete message,
and the usage transport buffers the complete JSON body before passing it to the
model adapter. Streaming tool middleware is not evidence of model streaming.
The coordinator commits complete responses through the durable Outbox.

The configured production model endpoint supports SSE and returns usage with
`stream_options.include_usage`. A synthetic probe used the same 126-token input
in non-streaming / streaming / streaming / non-streaming order. It sent no Bot
messages and performed no business operations.

| Trial | Mode | First visible text | Total | Cached input | Output tokens |
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

End-to-end streaming needs more than enabling the runner: incremental SSE usage
capture (including the final usage event), explicit handling of partial-stream
errors without replaying emitted output, cancellation and checkpoint tests, and
durable paragraph delivery through the Outbox. The final receipt must follow
all completed operations. Tools must receive complete validated arguments, and
confirmation must still gate destructive execution. Until that complete path
exists, the Bot keeps non-streaming behavior rather than reporting buffered SSE
as a user-visible speed improvement.

Independent reads could overlap when tool latency is significant. This requires
separating read concurrency from ordered writes and confirmation queues; turning
off Eino's sequential execution globally would also parallelize mutations. Use
production stage measurements before making that tradeoff. Fewer unnecessary
model/tool round trips and cache reuse can reduce total time; no mandatory tool
sequence or model-quality reduction is introduced by this investigation.

References: [Eino Runner and streaming configuration](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/),
[Kimi streaming and usage protocol](https://platform.kimi.com/docs/api/chat),
[Kimi automatic context caching](https://www.kimi.com/help/kimi-api/api-troubleshooting).
