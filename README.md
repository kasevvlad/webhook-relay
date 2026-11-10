# webhook-relay

[![tests](https://github.com/kasevvlad/webhook-relay/actions/workflows/tests.yml/badge.svg)](https://github.com/kasevvlad/webhook-relay/actions/workflows/tests.yml)

A small webhook delivery service written in Go. My learning project for goroutines, `database/sql` and building HTTP services with the standard library.

Your app sends an event to the service, the service delivers it to every subscriber with an HMAC signature, retries failed deliveries with exponential backoff, and keeps the ones that never succeeded so they can be retried by hand.

- PostgreSQL is the queue: workers take deliveries with `FOR UPDATE SKIP LOCKED`, so several workers never send the same delivery twice
- Retries: 10s, 20s, 40s, 80s... up to `MAX_ATTEMPTS`, then the delivery is marked `dead`
- Every request is signed: `X-Webhook-Signature: sha256=HMAC(secret, timestamp + "." + body)`
- Graceful shutdown: on `SIGTERM` the server stops accepting requests and workers finish what they are sending

How it works inside is described (in Russian) in [docs/how-it-works.md](docs/how-it-works.md), known limitations in [docs/limitations.md](docs/limitations.md).

## Running

```bash
docker compose up -d --build
```

The API is on `localhost:8080`.

```bash
# subscribe, save the secret from the response
curl -X POST localhost:8080/subscriptions \
  -d '{"url": "http://host.docker.internal:9000/webhook", "event_type": "order.created"}'

# run the example receiver that checks signatures
WEBHOOK_SECRET=<secret> go run ./examples/receiver

# send an event
curl -X POST localhost:8080/events \
  -d '{"type": "order.created", "payload": {"order_id": 42}}'

# see what happened
curl localhost:8080/deliveries
```

## API

| Method | Path | |
|---|---|---|
| `POST` | `/subscriptions` | `{"url", "event_type"}`, `event_type` can be `*`. Returns the secret once |
| `GET` | `/subscriptions` | List subscriptions |
| `DELETE` | `/subscriptions/{id}` | Delete a subscription and its deliveries |
| `POST` | `/events` | `{"type", "payload"}`, creates a delivery for every matching subscription |
| `GET` | `/deliveries?status=dead` | Last 100 deliveries, optionally filtered by status |
| `POST` | `/deliveries/{id}/retry` | Send a dead delivery again |
| `GET` | `/health` | Health check |

## Configuration

| Variable | Default |
|---|---|
| `ADDR` | `:8080` |
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable` |
| `WORKERS` | `4` |
| `MAX_ATTEMPTS` | `5` |

## Tests

```bash
make test               # unit tests
make test-integration   # starts Postgres in Docker and runs everything
```
