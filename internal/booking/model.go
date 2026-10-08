package booking

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("не найдено")
	ErrSoldOut  = errors.New("свободных мест нет")
	ErrConflict = errors.New("операция несовместима с текущим состоянием")
	ErrInvalid  = errors.New("некорректные данные")
)

type Event struct {
	ID       int64     `json:"id"`
	Title    string    `json:"title"`
	StartsAt time.Time `json:"starts_at"`
	Capacity int       `json:"capacity"`
}

type Reservation struct {
	ID        string    `json:"id"`
	EventID   int64     `json:"event_id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}
