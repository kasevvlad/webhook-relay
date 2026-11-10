package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("set WEBHOOK_SECRET to the secret you got when creating the subscription")
	}

	http.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		timestamp, err := strconv.ParseInt(r.Header.Get("X-Webhook-Timestamp"), 10, 64)
		if err != nil || time.Since(time.Unix(timestamp, 0)) > 5*time.Minute {
			log.Println("rejected: old or missing timestamp")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
		mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		if !hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Webhook-Signature"))) {
			log.Println("rejected: wrong signature")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		log.Printf("got event %s: %s", r.Header.Get("X-Webhook-Id"), body)
		w.WriteHeader(http.StatusOK)
	})

	log.Println("receiver is listening on :9000")
	log.Fatal(http.ListenAndServe(":9000", nil))
}
