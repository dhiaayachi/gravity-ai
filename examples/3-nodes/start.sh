#!/bin/sh

# Default values
: "${AGENT_ID:=agent-1}"
: "${BIND_ADDR:=127.0.0.1:8000}"
: "${HTTP_ADDR:=:8080}"
: "${DATA_DIR:=./data}"
: "${BOOTSTRAP:=false}"
: "${LLM_PROVIDER:=mock}"
: "${API_KEY:=}"
: "${PEERS:=}"
: "${CLEAN:=false}"
: "${LLM_MODEL:=}"

if [ "$CLEAN" = "true" ]; then
  echo "Cleaning data directory..."
  rm -rf "$DATA_DIR"/*
fi

echo "Starting Agent $AGENT_ID..."

# Start in foreground (or background but wait for it)
./gravity-agent \
  -id="$AGENT_ID" \
  -addr="$BIND_ADDR" \
  -http="$HTTP_ADDR" \
  -data-dir="$DATA_DIR" \
  -bootstrap="$BOOTSTRAP" \
  -peers="$PEERS" \
  -llm-provider="$LLM_PROVIDER" \
  -api-key="$API_KEY" \
  -model="$LLM_MODEL"
