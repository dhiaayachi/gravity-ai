# Gravity AI

## Why
Gravity AI is a consensus platform for AI agents.
A possible use case is a critical task where answer accuracy and 
consensus is important, and we would like a community of diverse agents 
to agree on a given answer/solution.

## Usage

### Build CLI
```bash
go build -o gravity-ai ./cmd/agent
```

### Run Agent
```bash
./gravity-ai agent --config config.yaml
```

### Client Commands
```bash
# Check status
./gravity-ai status

# Submit Task
./gravity-ai submit -c "Hello World"
```

## Architecture

Each agent will include an agent loop and a raft instance. 
Raft will be responsible for persisting data and leader election.

### Operation

#### Bootstrap
When an agents cluster is bootstrapped, agents will:
1. Bootstrap raft
2. Elect a raft leader
3. persist an agent configuration that include the agent reputation (initialized to 0)
4. Each agent will maintain connectivity with its backend LLM and health check it


#### Leader election
Unlike normal raft leader election the election will take into account node reputation.
The idea is to have the healthy node with the highest reputation elected as leader.
So in addition to normal raft criteria to vote for a node, the node that is receiving the vote
need to have a higher reputation than the node giving it.

#### Node Health
A node is considered healthy if:
1. it's reachable from a majority of nodes
2. Its backend LLM is healthy

#### Task execution
When a task is admitted, it's always forwarded to a leader node. 
The leader node will be the task facilitator. 
It will first admit the task by persisting in the raft log (as `admitted`) perform the 3 following rounds:

##### Brainstorm:
The task will be sent to each node to answer it. The answer is sent to the leader.
The leader need to ensure that a quorum of the agents answer the task.

##### Proposal
The leader create a single answer based on all the answers from the different agents.

##### Vote
The answer is sent to all the nodes to vote on it. The agents can either vote `Accepted` or `Rejected`.

If a majority of agents accept the proposal the answer is accepted, the leader send the final answer to the user 
and mark the task as `done` by persisting it in raft. The leader reputation is incremented by 1.

if a majority of agents reject the proposal the leader score is decremented by 1 and a leader election is triggered 
to allow a new leader to step in. The new leader will restart the task execution from the beginning.


