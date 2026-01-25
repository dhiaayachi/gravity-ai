#!/bin/bash

# Default values
NODE_URL="http://localhost:8080"
CONTENT=""

# Check for jq
if ! command -v jq &> /dev/null; then
    echo "Error: jq is required but not installed."
    exit 1
fi

# Help message
function show_help {
    echo "Usage: ./submit_task.sh [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -u, --url URL       Node URL (default: http://localhost:8080)"
    echo "  -c, --content TEXT  Task content (default: read from stdin)"
    echo "  -h, --help          Show this help message"
}

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        -u|--url) NODE_URL="$2"; shift ;;
        -c|--content) CONTENT="$2"; shift ;;
        -h|--help) show_help; exit 0 ;;
        *) echo "Unknown parameter passed: $1"; show_help; exit 1 ;;
    esac
    shift
done

# If content is empty, try to read from stdin
if [ -z "$CONTENT" ]; then
    # Check if a pipe or redirection is present
    if [ ! -t 0 ]; then
        CONTENT=$(cat)
    else
        # No content provided and no stdin
        echo "Error: No content provided via -c or stdin."
        show_help
        exit 1
    fi
fi


echo "Submitting task to $NODE_URL (Ollama API)..."

# Normalize content to handle escape sequences (like \n) if passed as string literals
# This helps with shells that pass multiline strings with literal escapes
CONTENT=$(printf "%b" "$CONTENT")

echo "Prompt: $CONTENT"

# Construct JSON payload using jq for safety
PAYLOAD=$(jq -n --arg model "gravity" --arg prompt "$CONTENT" '{model: $model, prompt: $prompt}')

# Submit task
# Use -sS to show errors but keep progress silent, and --fail to exit on HTTP error
RESPONSE=$(curl -sS --fail -X POST "$NODE_URL/api/generate" \
     -H "Content-Type: application/json" \
     -d "$PAYLOAD")

if [ $? -eq 0 ]; then
    echo ""
    echo "Response Received:"
    echo "$RESPONSE" | jq
else
    echo ""
    echo "Failed to submit task. (Check if node is running at $NODE_URL)"
    exit 1
fi
