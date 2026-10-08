package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"queuedesk/internal/booking"
)

type testRepo struct{ booking.Repository }

func (testRepo) Reserve(context.Context, int64, string, string, time.Duration) (booking.Reservation, error) {
	return booking.Reservation{}, booking.ErrSoldOut
}
func (testRepo) ListEvents(context.Context, int, int) ([]booking.Event, error) {
	return []booking.Event{}, nil
}

func TestHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := API{Service: booking.NewService(testRepo{}, time.Minute), AdminToken: "test-token", Ready: func(context.Context) error { return nil }}
	r := a.Router()
	for _, tc := range []struct {
		name, method, path, body, key string
		status                        int
	}{
		{"health", http.MethodGet, "/health/ready", "", "", 200},
		{"admin protection", http.MethodPost, "/api/v1/events", `{}`, "", 401},
		{"bad pagination", http.MethodGet, "/api/v1/events?limit=-1", "", "", 400},
		{"empty list", http.MethodGet, "/api/v1/events", "", "", 200},
		{"invalid uuid", http.MethodGet, "/api/v1/reservations/nope", "", "", 400},
		{"missing key", http.MethodPost, "/api/v1/reservations", `{"event_id":1,"name":"Анна"}`, "", 400},
		{"sold out", http.MethodPost, "/api/v1/reservations", `{"event_id":1,"name":"Анна"}`, "request-123", 409},
		{"invalid json", http.MethodPost, "/api/v1/reservations", `{`, "request-123", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", tc.key)
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("status=%d, body=%s", out.Code, out.Body.String())
			}
		})
	}
}
