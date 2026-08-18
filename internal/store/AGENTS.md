# internal/store navigation card

`internal/store/` implements Git, PostgreSQL, and S3-compatible object stores for config/auth persistence plus secure local auth-file rendering.
Read this card before changing remote persistence, sync/recovery, spool paths, auth path resolution, atomic writes, deletion, file permissions, symlink/reparse handling, or cooldown storage.
Key files: `gitstore.go`, `objectstore.go`, `postgresstore.go`, `postgres_cooldown_store.go`, `objectstore_render_*`, `objectstore_secure_*`.

## Why this is high-risk

- These stores hold credential material and can write/delete both remote objects/rows and local rendered auth files.
- Path containment, atomic installation, private permissions, and POSIX/Windows anti-symlink/reparse behavior are security and compatibility contracts.
- Git recovery and remote sync must preserve local dirty config/auth changes instead of replacing the whole worktree blindly.

## Required before changes

- Trace the caller through `sdk/auth`, `internal/watcher`, `sdk/cliproxy`, and the selected backend.
- Preserve root identity/containment checks, relative-ID validation, `0700` directories, `0600` auth files, temp-file cleanup, and atomic rename/install semantics.
- Keep backend configuration, credentials, bucket/database/repository values out of logs and errors.

## Do not

- 不要 use `RemoveAll` for auth-root replacement or deletion unless the existing backend-specific safe procedure explicitly does so.
- 不要 accept traversing remote object keys or rendered-target relative IDs, and never bypass containment, symlink, junction/reparse, or root-identity checks. Git/PostgreSQL callers may supply trusted absolute auth paths through the existing compatibility flow; tightening that public behavior requires an explicit migration and regression tests.
- 不要 run live remote-store mutation tests without explicit user authorization and disposable resources.

## Validation

- `go test ./internal/store`
- Race-sensitive store changes: `go test -race ./internal/store`
- Windows-specific DACL/reparse behavior requires Windows CI/VM; local non-Windows tests are not equivalent.
- PostgreSQL/S3/Git live integration needs external services, network, and disposable credentials; unit tests remain the default local check.
