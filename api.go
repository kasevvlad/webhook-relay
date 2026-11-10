package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type API struct {
	db *sql.DB
}

type Subscription struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	EventType string    `json:"event_type"`
	Secret    string    `json:"secret,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Delivery struct {
	ID             int64      `json:"id"`
	EventID        int64      `json:"event_id"`
	SubscriptionID int64      `json:"subscription_id"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  time.Time  `json:"next_attempt_at"`
	LastStatusCode *int       `json:"last_status_code"`
	LastError      *string    `json:"last_error"`
	DeliveredAt    *time.Time `json:"delivered_at"`
}

func NewRouter(db *sql.DB) http.Handler {
	api := &API{db: db}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /subscriptions", api.createSubscription)
	mux.HandleFunc("GET /subscriptions", api.listSubscriptions)
	mux.HandleFunc("DELETE /subscriptions/{id}", api.deleteSubscription)
	mux.HandleFunc("POST /events", api.createEvent)
	mux.HandleFunc("GET /deliveries", api.listDeliveries)
	mux.HandleFunc("POST /deliveries/{id}/retry", api.retryDelivery)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return mux
}

func (a *API) createSubscription(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL       string `json:"url"`
		EventType string `json:"event_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	parsed, err := url.Parse(input.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "url must be a valid http or https url")
		return
	}
	if input.EventType == "" {
		writeError(w, http.StatusBadRequest, "event_type is required, use * for all events")
		return
	}

	secret, err := generateSecret()
	if err != nil {
		serverError(w, err)
		return
	}

	sub := Subscription{URL: input.URL, EventType: input.EventType, Secret: secret}
	err = a.db.QueryRowContext(r.Context(),
		`INSERT INTO subscriptions (url, event_type, secret) VALUES ($1, $2, $3) RETURNING id, created_at`,
		sub.URL, sub.EventType, sub.Secret,
	).Scan(&sub.ID, &sub.CreatedAt)
	if err != nil {
		serverError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, sub)
}

func (a *API) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, url, event_type, created_at FROM subscriptions ORDER BY id`)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	subs := []Subscription{}
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.URL, &sub.EventType, &sub.CreatedAt); err != nil {
			serverError(w, err)
			return
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, subs)
}

func (a *API) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}

	result, err := a.db.ExecContext(r.Context(), `DELETE FROM subscriptions WHERE id = $1`, id)
	if err != nil {
		serverError(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) createEvent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if input.Type == "" || input.Type == "*" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage("{}")
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()

	var eventID int64
	err = tx.QueryRowContext(r.Context(),
		`INSERT INTO events (type, payload) VALUES ($1, $2) RETURNING id`,
		input.Type, string(input.Payload),
	).Scan(&eventID)
	if err != nil {
		serverError(w, err)
		return
	}

	result, err := tx.ExecContext(r.Context(), `
		INSERT INTO deliveries (event_id, subscription_id)
		SELECT $1, id FROM subscriptions WHERE event_type = $2 OR event_type = '*'
	`, eventID, input.Type)
	if err != nil {
		serverError(w, err)
		return
	}

	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}

	count, _ := result.RowsAffected()
	writeJSON(w, http.StatusAccepted, map[string]int64{"id": eventID, "deliveries": count})
}

func (a *API) listDeliveries(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")

	rows, err := a.db.QueryContext(r.Context(), `
		SELECT id, event_id, subscription_id, status, attempts, next_attempt_at, last_status_code, last_error, delivered_at
		FROM deliveries
		WHERE $1 = '' OR status = $1
		ORDER BY id DESC
		LIMIT 100
	`, status)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	deliveries := []Delivery{}
	for rows.Next() {
		var d Delivery
		err := rows.Scan(&d.ID, &d.EventID, &d.SubscriptionID, &d.Status, &d.Attempts, &d.NextAttemptAt, &d.LastStatusCode, &d.LastError, &d.DeliveredAt)
		if err != nil {
			serverError(w, err)
			return
		}
		deliveries = append(deliveries, d)
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, deliveries)
}

func (a *API) retryDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "delivery not found")
		return
	}

	result, err := a.db.ExecContext(r.Context(), `
		UPDATE deliveries
		SET status = 'pending', attempts = 0, next_attempt_at = now()
		WHERE id = $1 AND status = 'dead'
	`, id)
	if err != nil {
		serverError(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		writeError(w, http.StatusNotFound, "dead delivery not found")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func serverError(w http.ResponseWriter, err error) {
	log.Println("error:", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
