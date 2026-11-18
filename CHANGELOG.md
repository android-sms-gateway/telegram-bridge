# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### New Features

#### Telegram replies

- **Telegram replies** — replying with text to a forwarded message queues an SMS
  to the original sender through the SMSGate server API. Replies work on any part
  of a split message and are acknowledged in chat with the recipient and the
  SMSGate message id.
- **Operator allowlist** — `BOT__ALLOWED_USER_IDS` lists the Telegram user ids
  allowed to reply. An empty list is fail-closed: replies are filtered out before
  they reach the bridge, and a warning naming the key is logged at startup.
  `/start` still answers everyone.
- **SMSGate API configuration** — `SMSGATE__BASE_URL`, `SMSGATE__USERNAME` and
  `SMSGATE__PASSWORD` set the server API and credentials used by the reply path.
  Forwarding keeps working without them; only replies fail.

#### Reply mapping storage

- **Durable mappings** — each delivered Telegram message is stored in an embedded
  bbolt database at `STORAGE__URL` with a 7-day lifetime, so replies survive
  restarts. The parent directory of the database file must exist before startup.
- **Configuration** — `STORAGE__URL` selects the database file (default
  `bolt://data/telegram-bridge.db`), and `TELEGRAM__TIMEOUT` bounds bot API
  requests (default `30s`, must exceed the 8s long-poll hold).

## [0.0.3] - 2026-11-13

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
