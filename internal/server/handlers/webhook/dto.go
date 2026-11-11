package webhook

import (
	"github.com/android-sms-gateway/client-go/smsgateway"
	"github.com/gofiber/fiber/v2"
)

// Request is the ingest request body: the SMSGate app webhook envelope. Only
// the sms:received event is accepted; the envelope identifiers (id, webhookId,
// deviceId) are logged and any other unknown fields are ignored. The payload
// fields follow the app's Gson wire format: messageId, sender, recipient,
// simNumber, phoneNumber (deprecated alias of sender), message, receivedAt.
//
// id is required: it identifies the delivery and is the de-duplication key, so
// a payload without it is rejected instead of being forwarded twice.
type Request struct {
	ID        string `json:"id"        validate:"required,max=64"`
	WebhookID string `json:"webhookId"`
	DeviceID  string `json:"deviceId"`
	Event     string `json:"event"`

	Payload smsgateway.SmsReceivedPayload `json:"payload" validate:"required"`
}

func (r Request) Validate() error {
	// The app sends the event type in the envelope. Only sms:received carries a
	// received SMS, so the event must match exactly: any other value and a
	// payload without the envelope event are rejected instead of forwarded.
	if r.Event != smsgateway.WebhookEventSmsReceived {
		return fiber.NewError(fiber.StatusBadRequest, "unsupported webhook event")
	}

	if (r.Payload.PhoneNumber == "" && r.Payload.Sender == "") || r.Payload.Message == "" {
		return fiber.NewError(fiber.StatusBadRequest, "sender and message are required")
	}

	return nil
}

// ForwardResponse is the ingest response body.
type ForwardResponse struct {
	Status string `json:"status"`
}
