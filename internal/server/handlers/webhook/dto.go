package webhook

import "github.com/android-sms-gateway/client-go/smsgateway"

// Request is the ingest request body: the SMSGate app webhook envelope. Only
// the sms:received event is accepted; the envelope identifiers (id, webhookId,
// deviceId) are logged and any other unknown fields are ignored. The payload
// fields follow the app's Gson wire format: messageId, sender, recipient,
// simNumber, phoneNumber (deprecated alias of sender), message, receivedAt.
type Request struct {
	ID        string `json:"id"`
	WebhookID string `json:"webhookId"`
	DeviceID  string `json:"deviceId"`
	Event     string `json:"event"`

	Payload smsgateway.SmsReceivedPayload `json:"payload" validate:"required"`
}

// ForwardResponse is the ingest response body.
type ForwardResponse struct {
	Status string `json:"status"`
}
