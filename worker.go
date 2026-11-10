package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

type Worker struct {
	db          *sql.DB
	client      *http.Client
	maxAttempts int
	retryDelay  time.Duration
}

type pendingDelivery struct {
	ID        int64
	Attempts  int
	URL       string
	Secret    string
	EventID   int64
	EventType string
	Payload   json.RawMessage
}

func NewWorker(db *sql.DB, maxAttempts int) *Worker {
	return &Worker{
		db:          db,
		client:      &http.Client{Timeout: 10 * time.Second},
		maxAttempts: maxAttempts,
		retryDelay:  10 * time.Second,
	}
}

func (w *Worker) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		found, err := w.processOne()
		if err != nil {
			log.Println("worker error:", err)
		}

		if found && err == nil {
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (w *Worker) processOne() (bool, error) {
	ctx := context.Background()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var d pendingDelivery
	err = tx.QueryRowContext(ctx, `
		SELECT d.id, d.attempts, s.url, s.secret, e.id, e.type, e.payload
		FROM deliveries d
		JOIN subscriptions s ON s.id = d.subscription_id
		JOIN events e ON e.id = d.event_id
		WHERE d.status = 'pending' AND d.next_attempt_at <= now()
		ORDER BY d.next_attempt_at
		LIMIT 1
		FOR UPDATE OF d SKIP LOCKED
	`).Scan(&d.ID, &d.Attempts, &d.URL, &d.Secret, &d.EventID, &d.EventType, &d.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	statusCode, sendErr := w.send(ctx, d)
	attempts := d.Attempts + 1

	if sendErr == nil {
		_, err = tx.ExecContext(ctx, `
			UPDATE deliveries
			SET status = 'delivered', attempts = $2, last_status_code = $3, last_error = NULL, delivered_at = now()
			WHERE id = $1
		`, d.ID, attempts, statusCode)
	} else if attempts >= w.maxAttempts {
		log.Printf("delivery %d is dead after %d attempts: %v", d.ID, attempts, sendErr)
		_, err = tx.ExecContext(ctx, `
			UPDATE deliveries
			SET status = 'dead', attempts = $2, last_status_code = $3, last_error = $4
			WHERE id = $1
		`, d.ID, attempts, nullableCode(statusCode), sendErr.Error())
	} else {
		nextAttempt := time.Now().Add(backoff(w.retryDelay, attempts))
		_, err = tx.ExecContext(ctx, `
			UPDATE deliveries
			SET attempts = $2, last_status_code = $3, last_error = $4, next_attempt_at = $5
			WHERE id = $1
		`, d.ID, attempts, nullableCode(statusCode), sendErr.Error(), nextAttempt)
	}
	if err != nil {
		return true, err
	}

	return true, tx.Commit()
}

func (w *Worker) send(ctx context.Context, d pendingDelivery) (int, error) {
	body, err := json.Marshal(map[string]any{
		"id":      d.EventID,
		"type":    d.EventType,
		"payload": d.Payload,
	})
	if err != nil {
		return 0, err
	}

	timestamp := time.Now().Unix()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Id", strconv.FormatInt(d.EventID, 10))
	req.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-Webhook-Signature", sign(d.Secret, timestamp, body))

	resp, err := w.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return resp.StatusCode, nil
}

func backoff(base time.Duration, attempts int) time.Duration {
	if attempts > 20 {
		return time.Hour
	}

	delay := base * time.Duration(1<<(attempts-1))
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func nullableCode(code int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(code), Valid: code != 0}
}
