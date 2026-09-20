// Package feishu provides integration with the Feishu Open Platform using
// long-connection (WebSocket) delivery. Webhook delivery mode was removed in favour of long connection.
//
// WSClient owns one outbound connection per bound app: it discovers the
// gateway via POST /callback/ws/endpoint, speaks PBBP2 frames (codec.go), and
// hands complete im.message.receive_v1 events to the Hub's handler. Events
// split across several frames share a message_id and are reassembled in
// memory (fragment.go) — the handler runs once on the joined payload, and a
// single response frame echoing the terminal fragment's metadata plus biz_rt
// is written back. Connection metrics (state, last error with app_secret
// redacted, connected_at, reconnects) are exposed through LiveStatus for the
// REST binding endpoint and the Web UI; nothing here ever logs the secret.
package feishu
