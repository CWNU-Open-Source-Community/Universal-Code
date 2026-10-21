# Saved reply updates

`GET /api/v1/threads/reply-updates` uses the existing read bearer and GET-only
HTTP boundary. It observes active and archived conversations without changing
their lifecycle. Deleted conversations and copied branch history are excluded.

The response uses the normal `api.v1` envelope. Its `data` has this shape:

```json
{
  "version": "thread_reply_updates.v1",
  "data_store_id": "ds1_…",
  "cursor": "opaque-cursor",
  "has_more": false,
  "items": [
    {
      "thread_id": "thread-…",
      "message_id": "event-…",
      "thread_status": "active",
      "run_ordinal": 2,
      "sequence": 18,
      "created_at": "2026-10-22T10:00:00Z"
    }
  ]
}
```

- Omit `cursor` on first use. The server returns the current position and an
  empty `items` array, so enabling observation does not mark all history unread.
  An explicitly empty cursor is invalid.
- Supply the returned `data.cursor` on every later poll. This is not a
  `page.next_cursor`. An idle poll can return the same cursor.
- `limit` defaults to 50 and accepts 1–100. It bounds **source events scanned**,
  including filtered events. The store reads one extra event to detect more work.
  `items: []` with `has_more: true` is a valid page: continue from its cursor.
- Each request captures the current highest saved event position before scanning.
  `has_more: false` means it has covered all source events through that position.
  Events appended after that capture remain available to the next poll.
- Items are ordered by global event save position. Appending events or creating
  a successor Run does not shift a continuation. `(run_ordinal, sequence)` gives
  the item's position in its Thread transcript; `created_at` is not a cursor.
- Each item identifies a new saved public assistant message or public progress
  update accepted by the existing Go transcript projection. User input, queued
  or cancelled input, tool output, internal events and provisional streaming
  bytes cannot produce items. Neither message bodies nor raw event data appear
  in the response.
- Equal text saved as separate messages retains separate identities. This feed
  does not reproduce the conversation renderer's text merging. Replaying a
  cursor preserves message IDs, so consumers can apply results idempotently.
- Persist the returned identities/read-state changes before persisting their
  cursor. A failed request returns no partial items or advanced cursor; retry the
  previous cursor. Do not discard an unread marker merely because a poll fails.
- Cursors belong to one `data_store_id`. Another database, a replacement, or a
  copied database at a different path rejects the old cursor. Keep local state
  under the existing connection/data-store namespace; do not apply an in-flight
  response after switching that namespace. A new namespace starts with its own
  baseline. Switching back can resume the original namespace's saved cursor.

The OpenAPI document and generated TypeScript schema define the exact field
bounds. This endpoint supplies observations; the frontend owns the local read
position and explicit manual-unread preference.

## Local unread markers

The sidebar marks conversations with new saved replies and offers **Mark as
read** / **Mark as unread** in each conversation's menu. The cursor, pending
message identities, and manual preference are saved together in the existing
recovery namespace for the browser origin, API URL, and data-store identity.
Polling follows empty continuation pages and retries the unchanged checkpoint
after a request failure. A failed local write leaves the previous complete
checkpoint available after reload.

Automatic reading requires the conversation's latest view, a visible and focused
window, and a scroll position at the bottom. Saved-message history, inspectors,
an open narrow-screen sidebar, and covered conversation content do not acknowledge
replies. Visible saved assistant identities are acknowledged directly. On entering
the latest view, the client first catches up the reply feed, freezes the pending
identities, and requests a fresh transcript head. Only a successful read rendered
in the same reading context acknowledges that frozen range, including older
pending messages outside the latest window. Replies arriving during the request
remain pending until observed or included in a later confirmed range.

A manual unread marker survives polling, automatic reading, and reload. Explicitly
opening the conversation from the sidebar, including clicking the current row
again, clears that manual preference. **Mark as read** clears both the manual
marker and currently known pending replies.
