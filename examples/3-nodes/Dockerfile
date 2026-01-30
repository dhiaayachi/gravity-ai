FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o gravity-ai ./cmd/gravity-ai

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/gravity-ai .

CMD ["./gravity-ai", "agent"]
