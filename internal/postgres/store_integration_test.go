package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"queuedesk/internal/booking"
)

func integrationStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("нужна отдельная тестовая БД в TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := &Store{Pool: pool}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func testEvent(t *testing.T, s *Store, ctx context.Context, capacity int) booking.Event {
	t.Helper()
	e, err := s.CreateEvent(ctx, booking.Event{Title: "Интеграционный тест", Capacity: capacity, StartsAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Pool.Exec(cleanup, "DELETE FROM reservations WHERE event_id=$1", e.ID)
		s.Pool.Exec(cleanup, "DELETE FROM events WHERE id=$1", e.ID)
	})
	return e
}

func TestConcurrentReservations(t *testing.T) {
	s, ctx := integrationStore(t)
	e := testEvent(t, s, ctx, 10)
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Reserve(ctx, e.ID, "Анна", fmt.Sprintf("event-%d-request-%d", e.ID, i), time.Minute)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, booking.ErrSoldOut) {
				t.Errorf("reserve: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if success.Load() != 10 {
		t.Fatalf("успешных броней: %d, нужно 10", success.Load())
	}
}

func TestConcurrentIdempotency(t *testing.T) {
	s, ctx := integrationStore(t)
	e := testEvent(t, s, ctx, 1)
	ids := make(chan string, 20)
	var wg sync.WaitGroup
	key := fmt.Sprintf("same-request-%d", e.ID)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Reserve(ctx, e.ID, "Анна", key, time.Minute)
			if err != nil {
				t.Errorf("reserve: %v", err)
				return
			}
			ids <- r.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("один ключ создал разные брони")
		}
	}
	if first == "" {
		t.Fatal("ни одна бронь не создана")
	}
	if _, err := s.Reserve(ctx, e.ID, "Другое имя", key, time.Minute); !errors.Is(err, booking.ErrConflict) {
		t.Fatalf("payload conflict: %v", err)
	}
}

func TestReservationLifecycle(t *testing.T) {
	s, ctx := integrationStore(t)
	e := testEvent(t, s, ctx, 1)
	r, err := s.Reserve(ctx, e.ID, "Анна", fmt.Sprintf("lifecycle-%d", e.ID), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeStatus(ctx, r.ID, "confirmed"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeStatus(ctx, r.ID, "confirmed"); err != nil {
		t.Fatal("повтор подтверждения должен работать", err)
	}
	if _, err = s.Reserve(ctx, e.ID, "Борис", fmt.Sprintf("blocked-%d", e.ID), time.Minute); !errors.Is(err, booking.ErrSoldOut) {
		t.Fatalf("confirmed seat: %v", err)
	}
	if _, err = s.ChangeStatus(ctx, r.ID, "cancelled"); err != nil {
		t.Fatal(err)
	}
	r, err = s.Reserve(ctx, e.ID, "Борис", fmt.Sprintf("after-cancel-%d", e.ID), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE reservations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeStatus(ctx, r.ID, "confirmed"); !errors.Is(err, booking.ErrConflict) {
		t.Fatalf("late confirmation: %v", err)
	}
	// Место освобождается по времени даже до очередного прохода фонового обработчика.
	if _, err = s.Reserve(ctx, e.ID, "Вера", fmt.Sprintf("after-expiry-%d", e.ID), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Expire(ctx); err != nil {
		t.Fatal(err)
	}
	expired, err := s.GetReservation(ctx, r.ID)
	if err != nil || expired.Status != "expired" {
		t.Fatalf("expired=%+v, err=%v", expired, err)
	}
}
