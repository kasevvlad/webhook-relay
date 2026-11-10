package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`TRUNCATE deliveries, events, subscriptions RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}

	return db
}

type receiver struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	statuses []int
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	body, _ := io.ReadAll(r.Body)
	rc.requests = append(rc.requests, r)
	rc.bodies = append(rc.bodies, body)

	status := http.StatusOK
	if len(rc.statuses) > 0 {
		status = rc.statuses[0]
		rc.statuses = rc.statuses[1:]
	}
	w.WriteHeader(status)
}

func (rc *receiver) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.requests)
}

func doRequest(t *testing.T, handler http.Handler, method string, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

func subscribe(t *testing.T, router http.Handler, url string, eventType string) Subscription {
	t.Helper()

	rec := doRequest(t, router, "POST", "/subscriptions", `{"url":"`+url+`","event_type":"`+eventType+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subscription: status %d, body %s", rec.Code, rec.Body.String())
	}

	var sub Subscription
	json.Unmarshal(rec.Body.Bytes(), &sub)
	return sub
}

func processAll(t *testing.T, worker *Worker) {
	t.Helper()

	for {
		found, err := worker.processOne()
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return
		}
	}
}

func deliveryStatus(t *testing.T, db *sql.DB, id int64) (string, int) {
	t.Helper()

	var status string
	var attempts int
	err := db.QueryRow(`SELECT status, attempts FROM deliveries WHERE id = $1`, id).Scan(&status, &attempts)
	if err != nil {
		t.Fatal(err)
	}
	return status, attempts
}

func TestEventIsDeliveredWithSignature(t *testing.T) {
	db := setupDB(t)
	router := NewRouter(db)
	rc := &receiver{}
	server := httptest.NewServer(rc)
	defer server.Close()

	sub := subscribe(t, router, server.URL, "order.created")
	subscribe(t, router, server.URL, "order.paid")

	rec := doRequest(t, router, "POST", "/events", `{"type":"order.created","payload":{"order_id":42}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create event: status %d", rec.Code)
	}
	if rec.Body.String() != "{\"deliveries\":1,\"id\":1}\n" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}

	processAll(t, NewWorker(db, 5))

	if rc.count() != 1 {
		t.Fatalf("receiver got %d requests, want 1", rc.count())
	}

	req := rc.requests[0]
	timestamp, _ := strconv.ParseInt(req.Header.Get("X-Webhook-Timestamp"), 10, 64)
	if req.Header.Get("X-Webhook-Signature") != sign(sub.Secret, timestamp, rc.bodies[0]) {
		t.Error("signature does not match")
	}

	var body map[string]any
	json.Unmarshal(rc.bodies[0], &body)
	if body["type"] != "order.created" {
		t.Errorf("type is %v", body["type"])
	}

	status, attempts := deliveryStatus(t, db, 1)
	if status != "delivered" || attempts != 1 {
		t.Errorf("delivery is %s after %d attempts", status, attempts)
	}
}

func TestFailedDeliveryIsRetried(t *testing.T) {
	db := setupDB(t)
	router := NewRouter(db)
	rc := &receiver{statuses: []int{500, 503}}
	server := httptest.NewServer(rc)
	defer server.Close()

	subscribe(t, router, server.URL, "*")
	doRequest(t, router, "POST", "/events", `{"type":"user.registered","payload":{}}`)

	worker := NewWorker(db, 5)
	worker.retryDelay = 10 * time.Millisecond

	for i := 0; i < 3; i++ {
		processAll(t, worker)
		time.Sleep(50 * time.Millisecond)
	}

	if rc.count() != 3 {
		t.Fatalf("receiver got %d requests, want 3", rc.count())
	}

	status, attempts := deliveryStatus(t, db, 1)
	if status != "delivered" || attempts != 3 {
		t.Errorf("delivery is %s after %d attempts", status, attempts)
	}
}

func TestDeliveryBecomesDeadAndCanBeRetried(t *testing.T) {
	db := setupDB(t)
	router := NewRouter(db)
	rc := &receiver{statuses: []int{500, 500}}
	server := httptest.NewServer(rc)
	defer server.Close()

	subscribe(t, router, server.URL, "*")
	doRequest(t, router, "POST", "/events", `{"type":"user.registered"}`)

	worker := NewWorker(db, 2)
	worker.retryDelay = 10 * time.Millisecond

	for i := 0; i < 3; i++ {
		processAll(t, worker)
		time.Sleep(50 * time.Millisecond)
	}

	status, attempts := deliveryStatus(t, db, 1)
	if status != "dead" || attempts != 2 {
		t.Fatalf("delivery is %s after %d attempts", status, attempts)
	}

	rec := doRequest(t, router, "GET", "/deliveries?status=dead", "")
	var dead []Delivery
	json.Unmarshal(rec.Body.Bytes(), &dead)
	if len(dead) != 1 || *dead[0].LastError != "unexpected status 500" {
		t.Fatalf("unexpected dead deliveries: %s", rec.Body.String())
	}

	rec = doRequest(t, router, "POST", "/deliveries/1/retry", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry: status %d", rec.Code)
	}

	processAll(t, worker)

	status, _ = deliveryStatus(t, db, 1)
	if status != "delivered" {
		t.Errorf("delivery is %s after manual retry", status)
	}
}

func TestTwoWorkersDoNotSendTheSameDelivery(t *testing.T) {
	db := setupDB(t)
	router := NewRouter(db)
	rc := &receiver{}
	server := httptest.NewServer(rc)
	defer server.Close()

	subscribe(t, router, server.URL, "*")
	for i := 0; i < 20; i++ {
		doRequest(t, router, "POST", "/events", `{"type":"ping"}`)
	}

	worker := NewWorker(db, 5)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				found, err := worker.processOne()
				if err != nil || !found {
					return
				}
			}
		}()
	}
	wg.Wait()

	if rc.count() != 20 {
		t.Errorf("receiver got %d requests, want 20", rc.count())
	}
}

func TestValidation(t *testing.T) {
	db := setupDB(t)
	router := NewRouter(db)

	tests := []struct {
		path string
		body string
	}{
		{"/subscriptions", `{"url":"ftp://example.com","event_type":"*"}`},
		{"/subscriptions", `{"url":"https://example.com"}`},
		{"/subscriptions", `not json`},
		{"/events", `{"payload":{}}`},
	}

	for _, tt := range tests {
		rec := doRequest(t, router, "POST", tt.path, tt.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s %s: status %d, want 400", tt.path, tt.body, rec.Code)
		}
	}
}
