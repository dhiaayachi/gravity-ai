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

3.  **Install CLI**:
    Build the gravity agent CLI on your host machine:
    ```bash
    # From the project root
    go build -o gravity-agent ./cmd/agent
    ```

4.  **Submit a Task**:
    Use the `gravity-agent` CLI to submit a task to the leader (Node 1):
    
    ```bash
    ./gravity-agent submit -u http://localhost:8080 -c "Write a haiku about space"
    ```
    
    Or use the helper script (which uses the CLI):
    ```bash
    cd examples/3-nodes
    ./submit_task.sh -c "Write a haiku about space"
    ```

5.  **Check Status**:
    Check the status of the cluster nodes:
    ```bash
    ./gravity-agent status -u http://localhost:8080
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

The configuration is handled via environment variables in `docker-compose.yml`, mapped to command-line flags in `start.sh`.
