# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### New Features

#### Webhook event de-duplication

- **Event de-duplication** — `sms:received` events are de-duplicated by envelope
  ID per webhook: a redelivered event is acknowledged and dropped for 24 hours
  after first delivery. Events without an envelope ID are never suppressed, and
  if the cache backend is unavailable events are forwarded as usual.
- **Configuration** — `CACHE__URL` selects the cache backend that holds the
  de-duplication markers (`memory://` keeps state in-process) and
  `CACHE__SWEEP_INTERVAL` controls how often expired entries are evicted.

## [0.0.2] - 2026-11-06

### Improvements

- Webhook ingest logs the envelope identifiers of every signature-verified
  delivery, so Telegram forwards can be correlated with received events.

## [0.0.1] - 2026-11-01

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
