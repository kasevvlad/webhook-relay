TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/webhooks_test?sslmode=disable

up:
	docker compose up -d --build

down:
	docker compose down

test:
	go test ./...

test-integration:
	docker compose up -d postgres
	docker compose exec postgres sh -c 'until pg_isready -U postgres; do sleep 1; done'
	docker compose exec postgres psql -U postgres -tc "SELECT 1 FROM pg_database WHERE datname = 'webhooks_test'" | grep -q 1 || docker compose exec postgres createdb -U postgres webhooks_test
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -count=1 -v ./...

.PHONY: up down test test-integration
