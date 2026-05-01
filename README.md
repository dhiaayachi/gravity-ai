# Gravity AI

## Why
Gravity AI is a consensus platform for AI agents.
A possible use case is a critical task where answer accuracy and
consensus is important, and we would like a community of diverse agents
to agree on a given answer/solution.

## Usage

### Build CLI
```bash
go build -o gravity-ai ./cmd/gravity-ai
```

### Run Agent
```bash
./gravity-ai agent --config gravity.yaml
```

### Client Commands
```bash
# Check status
./gravity-ai status

# Submit Task
./gravity-ai submit -c "Hello World"
```

## Architecture

Each agent runs a Raft node alongside an event-driven engine.
Raft persists task state and elects the leader; the engine drives the
consensus protocol on top of Raft-committed events, so every node sees
every state transition through the same channel.

### Operation

#### Bootstrap
When an agents cluster is bootstrapped, agents will:
1. Bootstrap raft
2. Elect a raft leader
3. Initialize each agent's reputation to 100
4. Publish each agent's LLM provider/model metadata to the cluster via raft

#### Leader election
Two reputation-aware mechanisms run in parallel:

1. **Vote filtering.** During normal Raft elections, an agent rejects a
   `RequestVote` from any candidate whose reputation is lower than its own
   (`internal/raft/transport.go`). Standard Raft criteria apply on top of
   that.
2. **Post-task transfer.** After every task finalization, the current
   leader inspects peer reputations and voluntarily transfers leadership
   to any peer with strictly higher reputation
   (`Engine.checkForLeadershipTransfer`).

#### Task execution
When a task is admitted, it is forwarded to the leader. The leader is the
task facilitator and runs the following phases:

##### Brainstorm
The task is sent to each node to answer it; answers are forwarded back to
the leader.

##### Proposal
The leader aggregates the answers into a single proposal via `llm.Aggregate`.

##### Vote
The proposal is sent to all nodes. Each agent validates and votes
`Accepted` or `Rejected`, attaching reasoning and (for rejections) a
rebuttal.

If a quorum of agents accept, the task is marked `done` and the answer is
streamed back to the user.

If the proposal is rejected, the leader feeds the rejecters' reasoning
back into `llm.Revise`, produces a new proposal, and runs another vote
round. This repeats up to `MaxRounds`; if no round reaches consensus,
the engine falls back to the round with the most accepts above the
quorum, or marks the task as failed.

#### Reputation updates
After each task finalization, reputations are clamped to `[0, 100]` and
updated as follows:
- **Leader:** ±10 on task success / failure
- **Each voter:** ±1 depending on whether their vote agreed with the
  final outcome
