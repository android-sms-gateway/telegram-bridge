# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### New Features

#### SMSGate webhook forwarding

- **Signed webhook ingest** — `POST /api/v1/webhook` accepts SMSGate app
  webhooks and forwards the received SMS to a Telegram chat. Requests must carry
  `X-Signature` (hex HMAC-SHA256 of the raw body followed by `X-Timestamp`) and
  a `X-Timestamp` within 5 minutes; invalid or stale requests get `401`.
- **Telegram delivery** — accepted `sms:received` payloads are sent to the chat
  in `bridge.chat_id` as `New message from <sender>:` with the body in a code
  block. Sender and message are HTML-escaped, and messages longer than
  Telegram's 4096-unit limit are split across several messages.
- **Configuration** — `TELEGRAM__TOKEN` / `TELEGRAM__PROXY_URL` for the bot,
  `BRIDGE__CHAT_ID` (`bridge.chat_id`) for the forward target, and
  `SMSGATE__WEBHOOK__SIGNING_KEY` for payload verification. An empty signing key
  fails closed and rejects every request.

#### Service scaffold

- Fiber HTTP server with health/version endpoints, OpenAPI docs at
  `/api/v1/docs`, and a Telegram bot with a `/start` handler.
