# 4. Retry failed renders and commands

## Status

Accepted

## Context

To avoid useless reloads, the watcher stores a hash of the last rendered
data and skips a render when the hash is unchanged. The hash was saved
before the command ran. If the command failed, or was killed by the
`commandTimeout` (default 30s), the next attempt saw "unchanged" and
skipped. Resync events do not help, because they only trigger a render when
a node IP changes. The output file was updated but the consumer kept its
old config until some node IP changed.

## Decision

- Save the hash only after the command succeeds.
- When a render or command fails, the render loop schedules a retry after
  `retryDelay` (30 seconds) and keeps retrying until it succeeds.

## Consequences

- A slow or failing reload is retried on its own; no node change is needed.
- A permanently broken command or template is retried every 30 seconds
  forever, logging an error each time and incrementing the failure metrics.
- While a retry is waiting, a new node change waits for the retry timer
  (up to 30 seconds) instead of the usual 2 second debounce.
