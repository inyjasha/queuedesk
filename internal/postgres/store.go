package postgres

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"queuedesk/internal/booking"
)

//go:embed schema.sql
var schema string

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Несколько экземпляров приложения не должны одновременно менять схему.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(763214)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateEvent(ctx context.Context, e booking.Event) (booking.Event, error) {
	err := s.Pool.QueryRow(ctx, "INSERT INTO events(title,starts_at,capacity) VALUES($1,$2,$3) RETURNING id", e.Title, e.StartsAt, e.Capacity).Scan(&e.ID)
	return e, err
}

func (s *Store) ListEvents(ctx context.Context, limit, offset int) ([]booking.Event, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id,title,starts_at,capacity FROM events ORDER BY starts_at,id LIMIT $1 OFFSET $2", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]booking.Event, 0)
	for rows.Next() {
		var e booking.Event
		if err := rows.Scan(&e.ID, &e.Title, &e.StartsAt, &e.Capacity); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

const columns = "id::text,event_id,name,status,expires_at,created_at"
const readColumns = "id::text,event_id,name,CASE WHEN status='pending' AND expires_at<=clock_timestamp() THEN 'expired' ELSE status END,expires_at,created_at"

func scan(row pgx.Row) (booking.Reservation, error) {
	var r booking.Reservation
	err := row.Scan(&r.ID, &r.EventID, &r.Name, &r.Status, &r.ExpiresAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = booking.ErrNotFound
	}
	return r, err
}

func (s *Store) Reserve(ctx context.Context, eventID int64, name, key string, ttl time.Duration) (booking.Reservation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return booking.Reservation{}, err
	}
	defer tx.Rollback(ctx)
	// Сначала сериализуем одинаковые ключи, затем брони одного мероприятия.
	// Коллизия хеша лишь добавит ожидание: уникальность проверяется полным ключом.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return booking.Reservation{}, err
	}
	r, err := scan(tx.QueryRow(ctx, "SELECT "+readColumns+" FROM reservations WHERE idempotency_key=$1", key))
	if err == nil {
		if r.EventID != eventID || r.Name != name {
			return booking.Reservation{}, booking.ErrConflict
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(err, booking.ErrNotFound) {
		return booking.Reservation{}, err
	}
	var capacity int
	var startsAt time.Time
	err = tx.QueryRow(ctx, "SELECT capacity,starts_at FROM events WHERE id=$1 FOR UPDATE", eventID).Scan(&capacity, &startsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return booking.Reservation{}, booking.ErrNotFound
	}
	if err != nil {
		return booking.Reservation{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return booking.Reservation{}, err
	}
	if !startsAt.After(now) {
		return booking.Reservation{}, booking.ErrConflict
	}
	var occupied int
	err = tx.QueryRow(ctx, "SELECT count(*) FROM reservations WHERE event_id=$1 AND (status='confirmed' OR (status='pending' AND expires_at>$2))", eventID, now).Scan(&occupied)
	if err != nil {
		return booking.Reservation{}, err
	}
	if occupied >= capacity {
		return booking.Reservation{}, booking.ErrSoldOut
	}
	id, err := newID()
	if err != nil {
		return booking.Reservation{}, err
	}
	expires := now.Add(ttl)
	if startsAt.Before(expires) {
		expires = startsAt
	}
	r, err = scan(tx.QueryRow(ctx, "INSERT INTO reservations(id,event_id,name,idempotency_key,status,expires_at) VALUES($1,$2,$3,$4,'pending',$5) RETURNING "+columns, id, eventID, name, key, expires))
	if err != nil {
		return booking.Reservation{}, err
	}
	return r, tx.Commit(ctx)
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (s *Store) GetReservation(ctx context.Context, id string) (booking.Reservation, error) {
	return scan(s.Pool.QueryRow(ctx, "SELECT "+readColumns+" FROM reservations WHERE id=$1", id))
}

func (s *Store) ChangeStatus(ctx context.Context, id, status string) (booking.Reservation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return booking.Reservation{}, err
	}
	defer tx.Rollback(ctx)
	var eventID int64
	err = tx.QueryRow(ctx, "SELECT event_id FROM reservations WHERE id=$1", id).Scan(&eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return booking.Reservation{}, booking.ErrNotFound
	}
	if err != nil {
		return booking.Reservation{}, err
	}
	// Подтверждение и новая бронь используют одну блокировку, иначе возможна перепродажа.
	if _, err = tx.Exec(ctx, "SELECT id FROM events WHERE id=$1 FOR UPDATE", eventID); err != nil {
		return booking.Reservation{}, err
	}
	r, err := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM reservations WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return booking.Reservation{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return booking.Reservation{}, err
	}
	if r.Status == "pending" && !r.ExpiresAt.After(now) {
		return booking.Reservation{}, booking.ErrConflict
	}
	if r.Status == status {
		return r, tx.Commit(ctx)
	}
	if (status == "confirmed" && r.Status != "pending") || (status == "cancelled" && r.Status != "pending" && r.Status != "confirmed") {
		return booking.Reservation{}, booking.ErrConflict
	}
	r, err = scan(tx.QueryRow(ctx, "UPDATE reservations SET status=$2 WHERE id=$1 RETURNING "+columns, id, status))
	if err != nil {
		return booking.Reservation{}, err
	}
	return r, tx.Commit(ctx)
}

func (s *Store) Expire(ctx context.Context) (int64, error) {
	result, err := s.Pool.Exec(ctx, "UPDATE reservations SET status='expired' WHERE status='pending' AND expires_at<=clock_timestamp()")
	return result.RowsAffected(), err
}
