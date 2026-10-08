package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"queuedesk/internal/booking"
)

func main() {
	base := flag.String("url", "http://localhost:8080", "адрес API")
	token := flag.String("token", os.Getenv("ADMIN_TOKEN"), "токен администратора (или ADMIN_TOKEN)")
	flag.Parse()
	if err := run(strings.TrimRight(*base, "/"), *token); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(base, token string) error {
	if token == "" {
		return fmt.Errorf("задай ADMIN_TOKEN или флаг -token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second}
	status, body, err := request(ctx, client, "POST", base+"/api/v1/events", booking.Event{Title: "Go meetup: проверка конкурентности", Capacity: 10, StartsAt: time.Now().Add(time.Hour)}, "", token)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("создание мероприятия: %d %s", status, body)
	}
	var event booking.Event
	if err = json.Unmarshal(body, &event); err != nil {
		return err
	}
	type result struct {
		status int
		body   []byte
		err    error
		key    string
	}
	results := make(chan result, 100)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("demo-%d-%d", event.ID, i)
			status, body, err := request(ctx, client, "POST", base+"/api/v1/reservations", map[string]any{"event_id": event.ID, "name": "Анна"}, key, "")
			results <- result{status, body, err, key}
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	var first result
	for r := range results {
		if r.err != nil {
			return r.err
		}
		switch r.status {
		case 201:
			success++
			first = r
		case 409:
			conflicts++
		default:
			return fmt.Errorf("неожиданный ответ: %d %s", r.status, r.body)
		}
	}
	fmt.Printf("Мероприятие #%d: 100 запросов, %d успешных броней, %d отказов.\n", event.ID, success, conflicts)
	if success != 10 || conflicts != 90 {
		return fmt.Errorf("ожидалось ровно 10 броней и 90 отказов")
	}
	status, body, err = request(ctx, client, "POST", base+"/api/v1/reservations", map[string]any{"event_id": event.ID, "name": "Анна"}, first.key, "")
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("повтор запроса: %d %s", status, body)
	}
	var original, repeated booking.Reservation
	if err = json.Unmarshal(first.body, &original); err != nil {
		return err
	}
	if err = json.Unmarshal(body, &repeated); err != nil {
		return err
	}
	if original.ID != repeated.ID {
		return fmt.Errorf("повтор запроса создал новую бронь")
	}
	fmt.Println("Повтор с тем же Idempotency-Key вернул прежнюю бронь. Проверка пройдена.")
	return nil
}

func request(ctx context.Context, client *http.Client, method, url string, payload any, key, token string) (int, []byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, body, err
}
