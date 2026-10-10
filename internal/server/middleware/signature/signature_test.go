package signature_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/android-sms-gateway/telegram-bridge/internal/server/middleware/signature"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/fiberfx"
	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

const (
	testSigningKey = "test-signing-key"
	testTimestamp  = "1700000000"
	testBody       = `{"payload":{"sender":"6505551212","message":"hello"}}`
)

// sign computes the SMSGate payload signature: hex HMAC-SHA256 over the raw
// request body followed by the fixed test timestamp string. Cases that send a
// different X-Timestamp header are mismatched on purpose.
func sign(key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(testBody))
	mac.Write([]byte(testTimestamp))

	return hex.EncodeToString(mac.Sum(nil))
}

// counterValue reads the current value of bridge_webhook_total for one result
// label from the registry the metrics were registered with. The webhook package
// exposes its counters only through its own export_test.go, so the value is
// gathered from the registry instead.
func counterValue(t *testing.T, reg *prometheus.Registry, result string) float64 {
	t.Helper()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	for _, family := range families {
		if family.GetName() != "bridge_webhook_total" {
			continue
		}

		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "result" && label.GetValue() == result {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}

	return 0
}

func TestSignatureMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		signingKey   string
		signature    string
		timestamp    string
		body         string
		wantStatus   int
		wantRejected float64
	}{
		{
			name:         "valid signature passes",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey),
			timestamp:    testTimestamp,
			body:         testBody,
			wantStatus:   fiber.StatusOK,
			wantRejected: 0,
		},
		{
			name:         "missing signature header",
			signingKey:   testSigningKey,
			signature:    "",
			timestamp:    testTimestamp,
			body:         testBody,
			wantStatus:   fiber.StatusUnauthorized,
			wantRejected: 1,
		},
		{
			name:         "missing timestamp header",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey),
			timestamp:    "",
			body:         testBody,
			wantStatus:   fiber.StatusUnauthorized,
			wantRejected: 1,
		},
		{
			name:         "tampered body",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey),
			timestamp:    testTimestamp,
			body:         strings.Replace(testBody, "hello", "evil", 1),
			wantStatus:   fiber.StatusUnauthorized,
			wantRejected: 1,
		},
		{
			name:         "malformed timestamp",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey),
			timestamp:    "not-a-unix-timestamp",
			body:         testBody,
			wantStatus:   fiber.StatusUnauthorized,
			wantRejected: 1,
		},
		{
			name:         "empty configured signing key denies every request",
			signingKey:   "",
			signature:    sign(""),
			timestamp:    testTimestamp,
			body:         testBody,
			wantStatus:   fiber.StatusUnauthorized,
			wantRejected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewRegistry()
			metrics := webhook.NewMetricsWithRegisterer(reg)
			service := webhook.New(
				webhook.Config{SigningKey: tt.signingKey},
				nil,
				metrics,
				zap.NewNop(),
			)

			app := fiber.New(fiber.Config{ErrorHandler: fiberfx.NewJSONErrorHandler(zap.NewNop())})
			app.Post("/webhook", signature.New(service), func(c *fiber.Ctx) error {
				if string(c.Body()) != tt.body {
					t.Errorf("raw body seen by handler = %q, want %q", string(c.Body()), tt.body)
				}

				return c.SendStatus(fiber.StatusOK)
			})

			req := httptest.NewRequest(fiber.MethodPost, "/webhook", strings.NewReader(tt.body))
			req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			if tt.signature != "" {
				req.Header.Set("X-Signature", tt.signature)
			}

			if tt.timestamp != "" {
				req.Header.Set("X-Timestamp", tt.timestamp)
			}

			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("app.Test() error = %v", err)
			}

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			if got := counterValue(t, reg, "rejected"); got != tt.wantRejected {
				t.Fatalf("rejected counter = %v, want %v", got, tt.wantRejected)
			}
		})
	}
}
