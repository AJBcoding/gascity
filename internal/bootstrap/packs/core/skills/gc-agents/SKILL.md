---
name: gc-agents
description: Managing agents — list, peek, nudge, suspend, drain
---

# Agent Management

Agents are the workers in a Gas City workspace. Each runs in its own
session (tmux pane, container, etc).

## Adding agents

```
gc agent add --name <name>             # Scaffold agents/<name>/prompt.template.md
gc agent add --name <name> --dir <rig> # Scaffold a rig-scoped agent.toml
gc agent add --name <name> --prompt-template <file>
```

## Sessions from templates

Every configured template can now spawn sessions directly.

For cities migrating off the old multi-instance model, see
`engdocs/archive/migrations/remove-agent-multi-migration.md`.

Use the session commands directly:

```
gc session new <template>              # Create and attach to a new session
gc session new <template> --no-attach  # Create a detached background session
gc session suspend <id-or-template>    # Suspend a session
gc session close <id-or-template>      # Close a session permanently
gc session kill <name>                 # Force-kill an agent session
gc session submit <name> <message...>  # Deliver AND submit an instruction
gc session nudge <name> <message...>   # Send text to a running agent session
gc session logs <name>                 # Show session logs for an agent
```

When multiple sessions exist for the same template, use the session ID.

## Submit vs nudge

`gc session nudge` types text into the session's input box. Typing is not
submitting: some TUIs hold pasted input in the composer until something
presses Enter. Nudge checks afterwards and reports `NOT DELIVERED` (exit 1)
when the text is still sitting there, so a stranded message is visible
rather than silent — but it is still a message the agent has not acted on.

**To give a running agent an instruction you expect it to act on, use
`gc session submit`.** It delivers and submits, and carries an intent:

```
gc session submit <name> "status update"                          # default
gc session submit <name> "after this run, handle docs" --intent follow_up
gc session submit <name> "stop and do this instead" --intent interrupt_now
```

The intent lets the runtime decide whether to wake the session, inject
immediately, or queue until the current turn ends.

## Pools

Pools still control controller-managed worker capacity. Pool `max`
limits pool-managed workers, not manually created interactive sessions.

## Lifecycle

```
gc agent suspend <name>                # Suspend agent (reconciler skips it)
gc agent resume <name>                 # Resume a suspended agent
```

## Runtime

```
gc runtime drain <name>                # Signal agent to wind down gracefully
gc runtime undrain <name>              # Cancel drain
gc runtime drain-check <name>          # Check if agent has been drained
gc runtime drain-ack <name>            # Acknowledge drain (agent confirms exit)
gc runtime request-restart             # Request graceful restart (reads GC_AGENT env)
```
