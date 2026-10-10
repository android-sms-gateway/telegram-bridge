// Package signature provides the Fiber middleware that authenticates SMSGate
// webhook requests before they reach a route handler.
//
// The middleware itself holds no cryptography and no metrics: the HMAC
// comparison and the rejection counter both live in the webhook service, which
// the middleware delegates to.
package signature

import (
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/gofiber/fiber/v2"
)

const (
	// Names of the headers carrying the SMSGate payload signature (see the
	// payload signing section of the docs.sms-gate.app webhooks guide).
	headerSignature = "X-Signature"
	headerTimestamp = "X-Timestamp"
)

// New returns a Fiber middleware that rejects every request whose SMSGate
// payload signature does not authenticate the body it carries, with 401.
//
// Verification runs before the route handler parses the body, which is safe:
// Fiber serves c.Body() from the fasthttp request buffer, and that buffer
// stays valid and unmodified for the whole request (c.BodyParser re-reads the
// same buffer rather than consuming it). Requests rejected here never reach
// the handler, so no body parsing or metric side effect happens for them.
func New(service *webhook.Service) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := service.Verify(c.Body(), c.Get(headerSignature), c.Get(headerTimestamp)); err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, err.Error())
		}

		return c.Next()
	}
}
