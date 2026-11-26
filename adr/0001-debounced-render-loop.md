# 1. Debounced render loop

## Status

Accepted

## Context

Every node event used to render the template and run the command directly
from the informer's event handler. At startup the informer replays every
existing node as an ADD event, so a cluster with N nodes ran the command
(e.g. a Varnish or nginx reload) N times in a row. Node updates during
rollouts or autoscaling arrive in similar bursts. Running the command inside
the event handler also blocked event processing while the command ran.

## Decision

Event handlers only update the in-memory node map and send a non-blocking
signal on a channel with capacity 1. A single `renderLoop` goroutine owns
rendering: on the first signal it starts a 2 second timer (`defaultDebounce`),
and when the timer fires it renders once and runs the command once.

The timer is not reset by later signals, so a steady stream of events delays
a render by at most 2 seconds. Signals that arrive while a render is running
are coalesced into one follow-up render.

## Consequences

- One reload per burst instead of one per node; startup runs the command once.
- Event handling never waits for file writes or the command.
- Only one goroutine renders, so `currentHash` needs no lock.
- Every change now takes up to 2 seconds longer to reach the output file.
- Tests that call `handleNodeEvent` must run `renderLoop` (or call
  `renderAndExecute` directly) to see a render.
