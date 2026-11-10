package main

import (
	"testing"
	"time"
)

func TestSign(t *testing.T) {
	got := sign("secret", 1700000000, []byte(`{"id":1}`))
	want := "sha256=3dd1b9aef568d75f6790a84bd2e5dfa1f44409eef3cbdbd3f10b837376100c11"

	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestGenerateSecretIsRandom(t *testing.T) {
	first, _ := generateSecret()
	second, _ := generateSecret()

	if len(first) != 64 {
		t.Errorf("secret length is %d, want 64", len(first))
	}
	if first == second {
		t.Error("two secrets are equal")
	}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{10, time.Hour},
		{100, time.Hour},
	}

	for _, tt := range tests {
		got := backoff(10*time.Second, tt.attempts)
		if got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.attempts, got, tt.want)
		}
	}
}
