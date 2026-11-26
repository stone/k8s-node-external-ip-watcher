# 3. Write the output file in place

## Status

Accepted. Supersedes the temp-file-and-rename write proposed in commit 85f8707.

## Context

Commit 85f8707 wrote the output to a temp file and renamed it over the
target, so readers never see a partial file. A rename needs write access to
the output *directory*. Typical setups write into root-owned directories
(`/etc/varnish/backends.vcl`, `/etc/nginx/conf.d/`) where the non-root
service user owns only the output file. There every render failed with
"permission denied". The rename also replaced symlinks with regular files
and reset the file's owner and mode.

The consumers (Varnish, nginx, HAProxy) read the file only when the
configured command tells them to, and the command runs after the write has
finished. So the atomic rename protected against a case that does not
happen in normal operation.

## Decision

Render the template into a memory buffer first, then write it in place with
`os.WriteFile`. A template error never touches the existing file.

## Consequences

- Works when only the output file is writable; keeps owner, mode and
  symlinks intact. About 35 fewer lines than the rename version.
- Not atomic: if the process crashes during the write itself, the file can
  be left partly written until the next render rewrites it. A consumer that
  reloads on its own (not via the command) at that moment could read it.
- If atomic writes become necessary, the output directory must be writable
  by the service user.
