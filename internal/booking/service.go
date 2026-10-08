package booking

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Repository описывает только операции, которые нужны бизнес-логике.
type Repository interface {
	CreateEvent(context.Context, Event) (Event, error)
	ListEvents(context.Context, int, int) ([]Event, error)
	Reserve(context.Context, int64, string, string, time.Duration) (Reservation, error)
	GetReservation(context.Context, string) (Reservation, error)
	ChangeStatus(context.Context, string, string) (Reservation, error)
	Expire(context.Context) (int64, error)
}

type Service struct {
	repo Repository
	ttl  time.Duration
}

func NewService(repo Repository, ttl time.Duration) *Service { return &Service{repo: repo, ttl: ttl} }

func (s *Service) CreateEvent(ctx context.Context, e Event) (Event, error) {
	e.Title = strings.TrimSpace(e.Title)
	if e.Title == "" || len(e.Title) > 200 || e.Capacity < 1 || e.Capacity > 100000 || !e.StartsAt.After(time.Now()) {
		return Event{}, fmt.Errorf("%w: нужны название до 200 байт, будущая дата и от 1 до 100000 мест", ErrInvalid)
	}
	return s.repo.CreateEvent(ctx, e)
}

func (s *Service) ListEvents(ctx context.Context, limit, offset int) ([]Event, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, ErrInvalid
	}
	return s.repo.ListEvents(ctx, limit, offset)
}

func (s *Service) Reserve(ctx context.Context, eventID int64, name, key string) (Reservation, error) {
	name = strings.TrimSpace(name)
	if eventID < 1 || name == "" || len(name) > 100 || len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
		return Reservation{}, fmt.Errorf("%w: нужны event_id, имя до 100 байт и Idempotency-Key длиной 8–128 байт", ErrInvalid)
	}
	return s.repo.Reserve(ctx, eventID, name, key, s.ttl)
}

func (s *Service) GetReservation(ctx context.Context, id string) (Reservation, error) {
	return s.repo.GetReservation(ctx, id)
}
func (s *Service) ChangeStatus(ctx context.Context, id, status string) (Reservation, error) {
	if status != "confirmed" && status != "cancelled" {
		return Reservation{}, ErrInvalid
	}
	return s.repo.ChangeStatus(ctx, id, status)
}
func (s *Service) Expire(ctx context.Context) (int64, error) { return s.repo.Expire(ctx) }
