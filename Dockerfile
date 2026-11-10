FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /webhook-relay .

FROM alpine:3.22

COPY --from=build /webhook-relay /webhook-relay
EXPOSE 8080
CMD ["/webhook-relay"]
