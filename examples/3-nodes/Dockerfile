FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o gravity-agent ./cmd/agent

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/gravity-agent .
COPY --from=builder /app/examples/3-nodes/start.sh .

RUN chmod +x start.sh && apk add --no-cache curl

ENTRYPOINT ["./start.sh"]
