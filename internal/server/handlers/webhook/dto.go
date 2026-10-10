package webhook

import "github.com/android-sms-gateway/client-go/smsgateway"

// Request is the ingest request body: the SMSGate app webhook envelope. Only
// the sms:received payload is interpreted; the envelope fields (id, webhookId,
// event, deviceId) and any unknown fields are ignored. The payload fields
// follow the app's Gson wire format: messageId, sender, recipient, simNumber,
// phoneNumber (deprecated alias of sender), message, receivedAt.
type Request struct {
	Payload smsgateway.SmsReceivedPayload `json:"payload" validate:"required"`
}

// ForwardResponse is the ingest response body.
type ForwardResponse struct {
	Status string `json:"status"`
}
