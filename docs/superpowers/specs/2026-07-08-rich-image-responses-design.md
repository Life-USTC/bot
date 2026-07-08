# Rich Image Responses Design

## Goal

Add an MVP for picture-based bot replies for dense structured outputs while preserving the current text behavior as a fallback.

The first supported reply families are:

- Schedule replies: `schedule`, `today schedule`, `tomorrow schedule`, and date-specific schedule aliases.
- Todo list replies: `todo`, `td`, and list/filter variants.
- Dashboard/deadline replies: `overview` and `upcoming deadlines`.

Mutation confirmations, login messages, help text, search results, and agent free-form answers remain text-only in this MVP.

## Assumptions

- The image is an enhancement, not the source of truth. The text reply remains available for logging and fallback.
- NapCat can send image segments through OneBot 11 using a URL or file-like image segment.
- QQ official bot supports rich media for C2C and group messages through the current `/v2/users/{user_id}/files` and `/v2/groups/{group_id}/files` upload endpoints, then sends `msg_type: 7` with `media.file_info`.
- The first renderer should be deterministic and testable without a browser runtime.
- Generated images must not be persisted indefinitely. Short-lived in-memory media is enough for QQ/NapCat to fetch or upload.

## Architecture

Introduce a rich response path without breaking the existing string-only command API.

- Keep `commands.Handler.Handle(ctx, input) (string, bool)` as the compatibility wrapper used by existing tests and callers.
- Add a richer command result type internally, for example `commands.Response`, with:
  - `Text string`
  - `Image *responses.Image`
  - `Kind string`
- Add `commands.Handler.HandleResponse(ctx, input) (Response, bool)` and implement `Handle` by returning `response.Text`.
- Selected commands construct a `Response` with the same text currently returned plus an image candidate built from structured reply data.
- Platform senders choose the best available representation:
  - If an image is present and platform media send succeeds, send the image.
  - If rendering, media serving, upload, or send fails, send `Text`.
  - Record the text reply in interaction storage, with media send errors recorded only when both image and text fallback fail.

This keeps command behavior stable while allowing media-aware transports to evolve.

## Renderer

Use a small Go PNG renderer for the MVP.

Inputs are already-available structured rows:

- Schedule: title, date/day label, course, time range, place.
- Todo: title, due time, priority, completion state when available.
- Dashboard/deadline: section title plus normalized item rows for todos, homeworks, exams, and counts.

Rendering rules:

- Mobile-first PNG card, fixed max width around 900 px.
- White or near-white background, dark text, restrained accent colors.
- Wrap long Chinese and English text by display width.
- Use a readable font from the container or a bundled fallback if needed.
- Cap height by item count using the existing `listDisplayLimit`; include the existing "more" line visually.

The renderer returns PNG bytes and dimensions. Unit tests assert that non-empty PNG bytes are produced and that deterministic inputs produce stable dimensions.

## Media Serving

Add an internal short-lived media store in the bot process:

- Store generated PNG bytes under a random unguessable ID.
- TTL defaults to a few minutes, long enough for QQ official upload and NapCat fetch.
- Expose read-only HTTP route for media, bound to the same bot HTTP listener or a small dedicated listener.
- Public URL comes from config, for example `BOT_PUBLIC_BASE_URL=https://bot.tiankaima.cn`.

Caddy can route `/media/*` to the bot. The IDs are random and expire quickly, so no user-auth layer is required for the MVP. The media route returns `404` for missing or expired IDs and `image/png` for valid IDs.

## Platform Sending

### NapCat

For reverse WebSocket replies, send an image message segment:

```json
{
  "type": "image",
  "data": {
    "file": "https://bot.tiankaima.cn/media/<id>.png"
  }
}
```

For non-reverse NapCat HTTP fallback, use the same segment payload in `/send_private_msg` or `/send_group_msg`.

### QQ Official Bot

Extend the current QQ official bot sender to support rich media:

1. Create a media URL for the PNG.
2. Upload it to the target-specific files endpoint:
   - C2C: `/v2/users/{user_id}/files`
   - Group: `/v2/groups/{group_id}/files`
3. Use `file_type: 1` for images.
4. Send the returned `file_info` in a message with `msg_type: 7` and `media.file_info`.
5. Fall back to current text sending if any media step fails.

The existing text send path remains unchanged for text-only responses.

## Data Flow

1. Incoming message reaches NapCat or QQ official bot adapter.
2. Adapter calls `HandleResponse`.
3. Command handler returns text plus optional image intent.
4. Renderer generates PNG bytes for supported commands.
5. Media store registers the bytes and returns a public URL.
6. Adapter sends platform-specific media.
7. On any media failure, adapter sends text.
8. Store records the text form of the interaction, preserving current history and feedback context behavior.

## Error Handling

- Command/API failures still return existing text errors.
- Renderer failure logs a warning and falls back to text.
- Media registration failure logs a warning and falls back to text.
- NapCat image-send failure falls back to text.
- QQ official upload/send failure falls back to text.
- If both media and text send fail, record the outbound failure as today.

No user-visible "image failed" message is needed for the MVP because text fallback already answers the request.

## Testing

Unit tests:

- `commands.Handle` still returns exactly the existing text for supported commands.
- `commands.HandleResponse` includes image metadata only for selected read-only commands.
- Renderer creates valid PNG bytes for schedule, todo, and dashboard fixtures.
- Media store returns bytes before TTL and 404 behavior after expiry.
- NapCat sender emits image segment when response has media and active reverse WebSocket exists.
- NapCat sender falls back to text when image send fails.
- QQ official sender uploads to `/v2/users/{id}/files` or `/v2/groups/{id}/files` for image responses.
- QQ official sender sends `msg_type: 7` with `media.file_info` after upload.
- QQ official sender falls back to text when upload/send fails.

Integration/smoke:

- Local bot test can request `today schedule` and verify the media endpoint serves PNG.
- Production smoke can send one private NapCat schedule/todo request and verify logs show an image segment send or text fallback.

## Rollout

1. Implement behind a feature flag, default off: `BOT_ENABLE_IMAGE_RESPONSES=false`.
2. Enable in production after local tests and PR checks pass.
3. Start with private chats. Group enablement can follow after confirming image readability and rate behavior.
4. Keep text fallback permanently.

## Non-Goals

- No browser screenshot renderer in the MVP.
- No image support for free-form agent replies.
- No long-term media persistence.
- No Markdown retry path; prior testing showed NapCat markdown send is unreliable in this environment.
