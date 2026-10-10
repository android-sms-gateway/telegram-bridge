<a id="readme-top"></a>

<!-- PROJECT SHIELDS -->
[![Contributors][contributors-shield]][contributors-url]
[![Forks][forks-shield]][forks-url]
[![Stargazers][stars-shield]][stars-url]
[![Issues][issues-shield]][issues-url]
[![Apache License][license-shield]][license-url]
[![Go][go-shield]][go-url]

<!-- PROJECT LOGO -->
<br />
<div align="center">
  <a href="https://github.com/android-sms-gateway/telegram-bridge">
    <img src="https://raw.githubusercontent.com/golang-samples/gopher-vector/master/gopher.png" alt="Logo" width="120" height="120">
  </a>

<h3 align="center">telegram-bridge</h3>

  <p align="center">
    Forward SMS messages received through SMSGate webhooks to a Telegram chat.
    <br />
    <a href="https://github.com/android-sms-gateway/telegram-bridge"><strong>Explore the docs »</strong></a>
    <br />
    <br />
    <a href="https://github.com/android-sms-gateway/telegram-bridge/issues">Report Bug</a>
    ·
    <a href="https://github.com/android-sms-gateway/telegram-bridge/issues">Request Feature</a>
  </p>
</div>

<!-- TABLE OF CONTENTS -->
- [About The Project](#about-the-project)
  - [Built With](#built-with)
- [Features](#features)
- [Getting Started](#getting-started)
  - [Prerequisites](#prerequisites)
  - [Installation](#installation)
- [Usage](#usage)
  - [Sending a webhook](#sending-a-webhook)
- [Configuration](#configuration)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)
- [Contact](#contact)
- [Acknowledgments](#acknowledgments)

<!-- ABOUT THE PROJECT -->
## About The Project

`telegram-bridge` is a small service that connects the SMSGate Android app to Telegram. It receives signed webhooks from SMSGate, verifies them, and forwards each received SMS to a Telegram chat you choose.

It is useful when you want incoming SMS delivered to Telegram without polling a mailbox or writing custom integration code: point SMSGate webhooks at this service and let it relay the messages.

What it does:

* Accepts SMSGate webhook deliveries over HTTP.
* Rejects any delivery that is not correctly signed.
* Sends the sender number and message text to a Telegram chat.
* Splits long messages so they fit Telegram's limits.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

### Built With

* [![Go][go-shield]][go-url]
* [![Fiber][fiber-shield]][fiber-url]
* [![Fx][fx-shield]][fx-url]
* [![CLI][cli-shield]][cli-url]
* [![Swagger][swagger-shield]][swagger-url]
* [![Telegram][telegram-shield]][telegram-url]

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- FEATURES -->
## Features

* **Webhook ingest** — receives SMSGate app webhooks at `POST /api/v1/webhook`.
* **Signature verification** — authenticates every request with an HMAC-SHA256 signature and rejects requests whose timestamp falls outside a 5-minute window. The window narrows but does not eliminate replay; delivery is at-least-once. An empty signing key rejects everything.
* **Telegram delivery** — forwards the sender number and message text to a configured Telegram chat, escaping the content and splitting messages that exceed Telegram's length limit.
* **Webhook event filtering** — accepts only the `sms:received` event; any other event type or a payload without an `event` field is rejected with 400.
* **Operations endpoints** — liveness, readiness and startup health checks, Prometheus metrics, and a Swagger UI for the API.
* **Flexible configuration** — configure everything through environment variables or an optional YAML file.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- GETTING STARTED -->
## Getting Started

### Prerequisites

* Go 1.25+
  ```sh
  go version
  ```
* `swag` CLI for regenerating OpenAPI docs (optional)
  ```sh
  go install github.com/swaggo/swag/cmd/swag@latest
  ```
* `golangci-lint` (optional, for linting)
  ```sh
  golangci-lint version
  ```
* A Telegram bot token from [@BotFather](https://t.me/BotFather) and the chat id that should receive the messages.
* The SMSGate app configured to send webhooks to this service, with a signing key set under **Settings → Webhooks → Signing Key**.

### Installation

1. Clone the repository.
   ```sh
   git clone https://github.com/android-sms-gateway/telegram-bridge.git
   cd telegram-bridge
   ```
2. Download dependencies.
   ```sh
   make deps
   ```
3. Create your configuration file and fill in the values.
   ```sh
   cp .env.example .env
   ```
4. (Optional) Generate the OpenAPI docs.
   ```sh
   make gen
   ```
5. Build the binary.
   ```sh
   make build
   ```

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- USAGE -->
## Usage

Run the service (the default command is `serve`):

```sh
go run .
```

Or run the built binary:

```sh
./bin/telegram-bridge
```

With live reload during development (requires `air`):

```sh
make air
```

CLI commands:

```sh
go run . --help      # list commands
go run . serve       # run the server explicitly
go run . --version   # print the version
```

The server listens on `127.0.0.1:3000` by default and exposes:

* Health checks: `GET /health`, `/health/live`, `/health/ready`, `/health/startup`
* Metrics: `GET /metrics`
* API docs: `GET /api/v1/docs`
* Webhook ingest: `POST /api/v1/webhook`

### Sending a webhook

SMSGate signs each delivery. A request carries the hex HMAC-SHA256 signature of the raw body followed by the timestamp in the `X-Signature` header, and the Unix-seconds timestamp in the `X-Timestamp` header:

```sh
curl -X POST http://127.0.0.1:3000/api/v1/webhook \
  -H "Content-Type: application/json" \
  -H "X-Signature: <hex-signature>" \
  -H "X-Timestamp: <unix-seconds>" \
  -d '{
    "event": "sms:received",
    "payload": {
      "sender": "6505551212",
      "message": "Android is always a sweet treat!"
    }
  }'
```

On success the service replies with `{"status":"ok"}` after forwarding the message to Telegram. See `requests.http` for a ready-to-run example, including how to compute the signature.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- CONFIGURATION -->
## Configuration

Configuration is read from environment variables, an optional `.env` file, and optionally a YAML file. Later sources override earlier ones. See `.env.example` for the full, documented list.

The settings you will most likely change:

| Variable                        | Purpose                                                              | Default          |
| ------------------------------- | -------------------------------------------------------------------- | ---------------- |
| `TELEGRAM__TOKEN`               | Telegram bot token from @BotFather. Required.                        | *(empty)*        |
| `BRIDGE__CHAT_ID`               | Telegram chat id that receives forwarded messages.                   | `0`              |
| `SMSGATE__WEBHOOK__SIGNING_KEY` | Signing key shared with the SMSGate app. Empty rejects all webhooks. | *(empty)*        |
| `TELEGRAM__PROXY_URL`           | SOCKS5 proxy for Telegram API traffic (`socks5://` or `socks5h://`). | *(empty)*        |
| `HTTP__ADDRESS`                 | Address the HTTP server binds to.                                    | `127.0.0.1:3000` |
| `HTTP__OPENAPI__ENABLED`        | Serve the Swagger UI and API document under `/api/v1/docs`.          | `true`           |
| `CONFIG_PATH`                   | Path to an optional YAML configuration file.                         | *(empty)*        |

Notes:

* Keys use `__` to separate nesting levels, and key matching is case-insensitive.
* `TELEGRAM__TOKEN` and `SMSGATE__WEBHOOK__SIGNING_KEY` are secrets — keep them out of version control.
* `CONFIG_PATH`, `DEBUG` and `TZ` are read directly from the process environment, so set them in your shell rather than in `.env`.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- ROADMAP -->
## Roadmap

- [x] Receive and verify SMSGate webhooks
- [x] Forward received SMS to Telegram
- [ ] Reply to forwarded messages from Telegram (send SMS back through SMSGate)
- [ ] Persist bridge state in an embedded database
- [ ] CI release pipeline hardening

See the [open issues](https://github.com/android-sms-gateway/telegram-bridge/issues) for a full list of proposed features and known issues.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- CONTRIBUTING -->
## Contributing

Contributions are what make the open source community such an amazing place to learn, inspire, and create. Any contributions you make are **greatly appreciated**.

If you have a suggestion that would make this better, please fork the repo and create a pull request. You can also open an issue with the tag "enhancement". Don't forget to give the project a star! Thanks again!

1. Fork the Project
2. Create your Feature Branch (`git checkout -b feature/AmazingFeature`)
3. Commit your Changes (`git commit -m 'Add some AmazingFeature'`)
4. Push to the Branch (`git push origin feature/AmazingFeature`)
5. Open a Pull Request

Before submitting, run the local checks:

```sh
make fmt
make lint
make test
```

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- LICENSE -->
## License

Distributed under the Apache 2.0 License. See `LICENSE` for more information.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- CONTACT -->
## Contact

Maintainer: [@capcom6](https://github.com/capcom6)

Project Link: [https://github.com/android-sms-gateway/telegram-bridge](https://github.com/android-sms-gateway/telegram-bridge)

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- ACKNOWLEDGMENTS -->
## Acknowledgments

* [Best README Template](https://github.com/othneildrew/Best-README-Template)
* [Go Fiber](https://github.com/gofiber/fiber)
* [Uber Fx](https://github.com/uber-go/fx)
* [Telego](https://github.com/mymmrac/telego)
* [Shields.io](https://shields.io)

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- MARKDOWN LINKS & IMAGES -->
[contributors-shield]: https://img.shields.io/github/contributors/android-sms-gateway/telegram-bridge.svg?style=for-the-badge
[contributors-url]: https://github.com/android-sms-gateway/telegram-bridge/graphs/contributors
[forks-shield]: https://img.shields.io/github/forks/android-sms-gateway/telegram-bridge.svg?style=for-the-badge
[forks-url]: https://github.com/android-sms-gateway/telegram-bridge/network/members
[stars-shield]: https://img.shields.io/github/stars/android-sms-gateway/telegram-bridge.svg?style=for-the-badge
[stars-url]: https://github.com/android-sms-gateway/telegram-bridge/stargazers
[issues-shield]: https://img.shields.io/github/issues/android-sms-gateway/telegram-bridge.svg?style=for-the-badge
[issues-url]: https://github.com/android-sms-gateway/telegram-bridge/issues
[license-shield]: https://img.shields.io/github/license/android-sms-gateway/telegram-bridge.svg?style=for-the-badge
[license-url]: https://github.com/android-sms-gateway/telegram-bridge/blob/master/LICENSE
[go-shield]: https://img.shields.io/badge/go-1.25%2B-00ADD8?style=for-the-badge&logo=go
[go-url]: https://go.dev/
[fiber-shield]: https://img.shields.io/badge/Fiber-v2-00b894?style=for-the-badge
[fiber-url]: https://github.com/gofiber/fiber
[fx-shield]: https://img.shields.io/badge/Uber%20Fx-DI-6f42c1?style=for-the-badge
[fx-url]: https://github.com/uber-go/fx
[cli-shield]: https://img.shields.io/badge/urfave%2Fcli-v3-00b4d8?style=for-the-badge
[cli-url]: https://github.com/urfave/cli
[swagger-shield]: https://img.shields.io/badge/OpenAPI-Swagger-85EA2D?style=for-the-badge
[swagger-url]: https://github.com/swaggo/swag
[telegram-shield]: https://img.shields.io/badge/Telegram-Bot-26A5E4?style=for-the-badge&logo=telegram
[telegram-url]: https://core.telegram.org/bots
