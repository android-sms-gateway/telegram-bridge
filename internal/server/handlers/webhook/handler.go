package webhook

import (
	"github.com/android-sms-gateway/client-go/smsgateway"
	"github.com/android-sms-gateway/telegram-bridge/internal/server/middleware/signature"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/fiberfx/handler"
	"github.com/go-core-fx/fiberfx/validation"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
)

const (
	statusOK = "ok"
)

// Handler exposes the SMSGate webhook ingest endpoint over HTTP.
type Handler struct {
	handler.Base

	webhookSvc *webhook.Service

	verifySignature fiber.Handler
}

// New creates a new webhook HTTP handler.
func New(webhookSvc *webhook.Service, validator *validator.Validate) handler.Handler {
	return &Handler{
		Base: handler.Base{
			Validator: validator,
		},
		webhookSvc: webhookSvc,

		verifySignature: signature.New(webhookSvc),
	}
}

// Register registers all webhook HTTP routes.
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/webhook", h.verifySignature)

	g.Post("/", validation.DecorateWithBodyEx(h.Validator, h.post))
}

//	@Summary		Receive SMSGate webhook
//	@Description	Verifies the payload signature and forwards the message to Telegram. Only the sms:received event is accepted.
//	@Tags			webhook
//	@Accept			json
//	@Produce		json
//	@Param			X-Signature	header		string	true	"Hex HMAC-SHA256 signature of the raw body followed by the timestamp"
//	@Param			X-Timestamp	header		string	true	"Unix timestamp (seconds) used in the signature; must be within 5 minutes of server time"
//	@Param			request		body		Request	true	"SMSGate webhook payload"
//	@Success		200			{object}	ForwardResponse
//	@Failure		400			{object}	fiberfx.ErrorResponse
//	@Failure		401			{object}	fiberfx.ErrorResponse
//	@Failure		500			{object}	fiberfx.ErrorResponse
//	@Router			/webhook [post]
//
// Receive SMSGate webhook.
func (h *Handler) post(c *fiber.Ctx, req *Request) error {
	// The app sends the event type in the envelope. Only sms:received carries a
	// received SMS, so any other event is rejected instead of forwarded. An
	// empty event is accepted: payloads that omit the envelope are still
	// forwarded on their payload content alone.
	if req.Event != "" && req.Event != smsgateway.WebhookEventSmsReceived {
		return fiber.NewError(fiber.StatusBadRequest, "unsupported webhook event")
	}

	// The app marks phoneNumber deprecated in favor of sender; both carry the
	// external sender number for sms:received events. Older app versions may
	// only send phoneNumber.
	phoneNumber := req.Payload.Sender
	if phoneNumber == "" {
		phoneNumber = req.Payload.PhoneNumber
	}

	if phoneNumber == "" || req.Payload.Message == "" {
		return fiber.NewError(fiber.StatusBadRequest, "sender and message are required")
	}

	if err := h.webhookSvc.Forward(c.Context(), phoneNumber, req.Payload.Message); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to forward message")
	}

	return c.JSON(ForwardResponse{Status: statusOK})
}
