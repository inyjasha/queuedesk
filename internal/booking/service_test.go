package booking

import (
	"context"
	"errors"
	"testing"
	"time"
)

type spyRepo struct {
	Repository
	called bool
	event  Event
	name   string
}

func (r *spyRepo) CreateEvent(_ context.Context, e Event) (Event, error) {
	r.called = true
	r.event = e
	e.ID = 1
	return e, nil
}
func (r *spyRepo) Reserve(_ context.Context, _ int64, name, _ string, _ time.Duration) (Reservation, error) {
	r.called = true
	r.name = name
	return Reservation{ID: "test"}, nil
}

func TestCreateEventValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    Event
	}{
		{"empty title", Event{Title: "  ", Capacity: 1, StartsAt: time.Now().Add(time.Hour)}},
		{"zero capacity", Event{Title: "Go", StartsAt: time.Now().Add(time.Hour)}},
		{"past date", Event{Title: "Go", Capacity: 1, StartsAt: time.Now().Add(-time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &spyRepo{}
			s := NewService(repo, time.Minute)
			_, err := s.CreateEvent(context.Background(), tc.e)
			if !errors.Is(err, ErrInvalid) || repo.called {
				t.Fatalf("err=%v, called=%v", err, repo.called)
			}
		})
	}
}

func TestReserveValidationAndTrimming(t *testing.T) {
	repo := &spyRepo{}
	s := NewService(repo, time.Minute)
	if _, err := s.Reserve(context.Background(), 1, "Анна", "short"); !errors.Is(err, ErrInvalid) || repo.called {
		t.Fatal("короткий ключ должен быть отклонён до обращения к БД")
	}
	if _, err := s.Reserve(context.Background(), 1, " Анна ", "request-123"); err != nil || repo.name != "Анна" {
		t.Fatalf("name=%q, err=%v", repo.name, err)
	}
}
