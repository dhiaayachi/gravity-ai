#!/bin/bash

# Default values
NODE_URL="http://localhost:8080"
CONTENT="Explain quantum gravity"

# Help message
function show_help {
    echo "Usage: ./submit_task.sh [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -u, --url URL       Node URL (default: http://localhost:8080)"
    echo "  -c, --content TEXT  Task content (default: 'Explain quantum gravity')"
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

echo "Submitting task to $NODE_URL..."
echo "Content: $CONTENT"

# URL encode content
ENCODED_CONTENT=$(echo "$CONTENT" | jq -sRr @uri)

# Submit task
RESPONSE=$(curl -s -X GET "$NODE_URL/submit?content=$ENCODED_CONTENT")

if [ $? -eq 0 ]; then
    echo ""
    echo "Task Submitted Successfully!"
    echo "Task ID: $RESPONSE"
else
    echo ""
    echo "Failed to submit task."
    exit 1
fi
