# 3-Node Cluster Example

This example demonstrates how to run a 3-node Gravity AI cluster using Docker Compose.

## Prerequisites

- Docker and Docker Compose
- A Gemini API Key (used by `node2`)
- An Ollama instance running on the host with `llama3:8b` pulled — `node1`
  and `node3` connect to it via `host.docker.internal:11434`. To use Gemini
  on those nodes instead, swap the commented-out env block in
  `docker-compose.yml`.

## Setup

1.  **Set your API Key**:
    ```bash
    export GEMINI_API_KEY="your_api_key_here"
    ```

    And make sure Ollama is running locally with the model pulled:
    ```bash
    ollama pull llama3:8b
    ollama serve
    ```

2.  **Build and Run**:
    ```bash
    docker-compose up --build
    ```

    Node 1 will bootstrap the cluster. Node 2 and Node 3 will automatically join Node 1.

3.  **Install CLI**:
    Build the gravity agent CLI on your host machine:
    ```bash
    # From the project root
    go build -o gravity-ai ./cmd/gravity-ai
    ```

4.  **Submit a Task**:
    The prompt for this example lives in `prompt.md` — a Go-output question
    designed to be deterministic but easy for a single LLM to get wrong, so
    you can watch the cluster brainstorm, propose, and vote on it.

    Pipe it into the `gravity-ai` CLI:

    ```bash
    cat examples/3-nodes/prompt.md | ./gravity-ai submit -u http://localhost:8080
    ```

    Or via the helper script (also reads stdin):
    ```bash
    cd examples/3-nodes
    cat prompt.md | ./submit_task.sh
    ```

5.  **Check Status**:
    Check the status of the cluster nodes:
    ```bash
    ./gravity-ai status -u http://localhost:8080
    ```

6.  **View Logs**:
    Watch the docker logs to see the nodes brainstorming, proposing, voting, and reaching consensus.
    ```bash
    docker-compose logs -f
    ```

## Architecture

- **Node 1**: Bootstraps the cluster. Exposes port 8080.
- **Node 2**: Joins Node 1. Exposes port 8081.
- **Node 3**: Joins Node 1. Exposes port 8082.
- **Network**: All nodes communicate inside the docker network `default` on port 8000 (gRPC/Raft) and 8080 (HTTP).

## Configuration

The configuration is handled via environment variables in
`docker-compose.yml` (prefix `GRAVITY_`). The container entrypoint runs
`./gravity-ai agent` directly; flags are read from those env vars by the
config loader (see `config/config.go`).
