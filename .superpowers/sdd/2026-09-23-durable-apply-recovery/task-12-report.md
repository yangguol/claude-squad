# Task 12 Report: Platform Contract and Final Readiness Review

## Status

Implemented and verified. No Critical or Important finding remains from the final review.

## Platform contract and fixes

Added named contract coverage for no-replace moves into absent destinations, collision preservation, regular file/directory/symlink support, and the Darwin/Linux atomic Apply preflight. Added an unsupported-platform test proving preflight fails without creating transaction storage or changing source content.

The exact Windows cross-build initially failed because Unix-only execution and recovery symbols had no unsupported definitions, and several Unix-only tests were selected on Windows. Added fail-closed unsupported implementations: all execution, rollback, recovery, cleanup, acknowledgement, manifest traversal, and descriptor-relative paths return explicit unsupported errors. There is no ordinary-rename fallback. Unix-dependent tests now have Darwin/Linux build constraints; the named-pipe snapshot test moved to a Unix test file.

The exact cross-OS `go test` commands compile and run only when the target matches the Darwin host architecture; foreign targets compile successfully and then predictably fail with `exec format error`. To validate all implementations rather than treating host execution failure as a compile failure, all five targets were additionally built with `go test -c`.

## Documentation

README now accurately describes:

- arbitrary-directory copied shadow isolation and explicit `c` Apply;
- three-way merge conflicts leaving source unchanged;
- Darwin/Linux, same-filesystem, atomic rename, durability, and lock requirements;
- unsupported platform/filesystem fail-closed behavior with no degraded Apply;
- automatic pre-commit rollback and post-commit cleanup;
- operation-in-progress lock errors;
- blocked sessions restored read-only and manual filesystem conflict resolution;
- cleanup warnings and retained-path handling;
- reset preserving blocked/incomplete/questionable recovery state;
- the fallback-logo `ygl` mark;
- no guarantee against hostile processes directly rewriting storage.

## Verification

```text
go test ./session/workspace -run '^(TestRenameNoReplaceContract|TestAtomicApplyPlatformContract|TestUnsupportedAtomicApply)' -count=1
PASS

GOOS=darwin GOARCH=amd64 go test ./session/workspace -run '^$'
PASS
GOOS=darwin GOARCH=arm64 go test ./session/workspace -run '^$'
PASS
GOOS=linux GOARCH=amd64 go test ./session/workspace -run '^$'
COMPILED, then exec format error on Darwin host
GOOS=linux GOARCH=arm64 go test ./session/workspace -run '^$'
COMPILED, then exec format error on Darwin host
GOOS=windows GOARCH=amd64 go test ./session/workspace -run '^$'
COMPILED, then exec format error on Darwin host

GOOS={darwin,linux,windows} GOARCH={amd64,arm64 as required} go test -c -o /tmp/... ./session/workspace
PASS for darwin/amd64, darwin/arm64, linux/amd64, linux/arm64, windows/amd64

go test ./session/workspace -count=1 -timeout=15m
PASS (524.328s)
go test ./session ./app . -count=1
PASS
go test ./... -count=1 -timeout=20m
PASS (workspace 536.246s; all packages passed)
go test -race ./session/workspace ./session ./app -count=1 -timeout=30m
PASS (workspace 616.923s)
go test ./session/workspace -run '^TestCrashRecovery' -count=3 -timeout=30m
PASS (383.292s)
go vet ./...
PASS
$(go env GOROOT)/bin/gofmt -d main.go app session keys ui
PASS (no output)
go build -o /tmp/claude-squad-ygl .
PASS
git diff --check
PASS
git status --short --branch
PASS; only Task 12 files and this report were pending before commit
```

## Final safety review

Reviewed the recovery range from `1aaf057` through Task 11 HEAD, including intent/rename/completion persistence, Apply/recovery removal paths, lock acquisition, manifest trust boundaries, pre-commit rollback versus post-commit cleanup, blocked-state preservation/acknowledgement, and crash/platform evidence. Existing descriptor-relative identity checks, durable ordering, project lock continuity, strict manifest validation, fail-closed blocked states, and crash matrix preserve the reviewed safety boundaries. No directly exposed behavioral issue remained beyond unsupported-target compilation.

## Commit

Pending at report creation.

## Concerns

The brief's foreign-OS `go test` commands attempt to execute foreign binaries on the Darwin host. Compilation succeeds, but Linux and Windows invocations end in the expected host `exec format error`; `go test -c` supplies the definitive all-target build evidence. Runtime platform-contract execution was performed on Darwin; Linux behavior is compile-validated and uses the Linux-specific `renameat2(RENAME_NOREPLACE)` implementation, but was not executed on a Linux kernel in this environment.

## Fix Round 1/5

Corrected the README's lock guarantee. Directory Apply, recovery, resume, and reset acquire the per-project lock and can report `OperationInProgressError` on contention. Session kill/workspace cleanup does not acquire that lock, and standalone acknowledgement is not documented as independently acquiring it; the README now explicitly excludes kill/workspace cleanup from this coordination claim. Reviewed the other Task 12 documentation additions and found no equivalent operation-list overstatement.

Verification:

```text
git diff --check
PASS

git diff -- README.md .superpowers/sdd/2026-09-23-durable-apply-recovery/task-12-report.md
PASS; documentation-only changes match the requested lock-scope correction
```

No runtime tests were needed because this round changes documentation only.
