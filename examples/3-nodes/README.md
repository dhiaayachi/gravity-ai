# 3-Node Cluster Example

This example demonstrates how to run a 3-node Gravity AI cluster using Docker Compose.

## Prerequisites

- Docker and Docker Compose
- A Gemini API Key

## Setup

1.  **Set your API Key**:
    ```bash
    export GEMINI_API_KEY="your_api_key_here"
    ```

2.  **Build and Run**:
    ```bash
    docker-compose up --build
    ```

    Node 1 will bootstrap the cluster. Node 2 and Node 3 will automatically join Node 1.

3.  **Submit a Task**:
    You can submit a task via the HTTP API on Node 1 (mapped to port 8081 locally) using the helper script:
    
    ```bash
    ./submit_task.sh -u http://localhost:8081 -c "Write a haiku about space"
    ```
    
    Or directly via `curl` (Ollama compatible):
    ```bash
    curl -X POST http://localhost:8081/api/generate -d '{
      "model": "gravity",
      "prompt": "Write a haiku about space"
    }'
    ```
    
    This will return a JSON response with the result.

4.  **View Logs**:
    Watch the docker logs to see the nodes brainstorming, proposing, voting, and reaching consensus.
    ```bash
    docker-compose logs -f
    ```

## Architecture

- **Node 1**: Bootstraps the cluster. Exposes port 8081.
- **Node 2**: Joins Node 1. Exposes port 8082.
- **Node 3**: Joins Node 1. Exposes port 8083.
- **Network**: All nodes communicate inside the docker network `default` on port 8000 (Raft) and 8080 (HTTP).

## Configuration

The configuration is handled via environment variables in `docker-compose.yml`, mapped to command-line flags in `start.sh`.
