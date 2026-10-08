package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"queuedesk/internal/booking"
)

type countingRepo struct {
	booking.Repository
	calls atomic.Int32
}

func (r *countingRepo) ListEvents(context.Context, int, int) ([]booking.Event, error) {
	r.calls.Add(1)
	return []booking.Event{}, nil
}

func TestRedisCacheAndLimiter(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("нужен TEST_REDIS_ADDR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	repo := &countingRepo{}
	a := API{Service: booking.NewService(repo, time.Minute), Redis: rdb, AdminToken: "test", Ready: func(context.Context) error { return nil }}
	router := a.Router()
	offset := time.Now().Unix()
	key := fmt.Sprintf("events:99:%d", offset)
	rateKey := "rate:192.0.2.200"
	if err := rdb.Del(ctx, key, rateKey).Err(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rdb.Del(cleanup, key, rateKey)
	}()
	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/events?limit=99&offset=%d", offset), nil)
		req.RemoteAddr = "192.0.2.200:1234"
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	for i := 0; i < 2; i++ {
		if out := get(); out.Code != 200 {
			t.Fatalf("GET: %d %s", out.Code, out.Body.String())
		}
	}
	if repo.calls.Load() != 1 {
		t.Fatalf("кеш не использован, запросов к хранилищу: %d", repo.calls.Load())
	}
	if ttl := rdb.TTL(ctx, key).Val(); ttl <= 0 || ttl > 15*time.Second {
		t.Fatalf("TTL кеша: %v", ttl)
	}
	if err := rdb.Set(ctx, rateKey, 199, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if out := get(); out.Code != 200 {
		t.Fatalf("запрос 200: %d", out.Code)
	}
	if out := get(); out.Code != 429 || out.Header().Get("Retry-After") == "" {
		t.Fatalf("запрос 201: %d", out.Code)
	}
}
