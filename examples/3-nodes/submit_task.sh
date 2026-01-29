#!/bin/bash

# Default values
NODE_URL="http://localhost:8080"
CONTENT=""

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

# Locate gravity-agent binary
# Try current directory, then parent's parent (project root)
GRAVITY_BIN=""
if [ -f "./gravity-agent" ]; then
    GRAVITY_BIN="./gravity-agent"
elif [ -f "../../gravity-agent" ]; then
    GRAVITY_BIN="../../gravity-agent"
else
    # Try finding in PATH
    if command -v gravity-agent &> /dev/null; then
        GRAVITY_BIN="gravity-agent"
    fi
fi

if [ -z "$GRAVITY_BIN" ]; then
    echo "Error: gravity-agent binary not found in current directory, project root, or PATH."
    echo "Please build it with: go build -o gravity-agent ./cmd/agent"
    exit 1
fi

echo "Submitting task to $NODE_URL using $GRAVITY_BIN..."

# Call the CLI
# Note: passing content as argument. If content is complex, stdin might be safer but CLI supports both.
# Let's use stdin to avoid shell escaping issues with complex prompts
echo "$CONTENT" | $GRAVITY_BIN submit --url "$NODE_URL" --content ""
