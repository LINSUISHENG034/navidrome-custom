# Agent execution and evidence

Use this guide when changing code, configuration, or tracked project guidance. `AGENTS.md` is the portable entry point; confirm that the session loads it. Claude Code's AGENTS support depends on version and instruction settings, and an ancestor project `CLAUDE.md` can take precedence. If instructions are absent, explicitly provide the entry point rather than silently changing user settings.

## 1. Bound the task

Record the goal, affected behavior or invariant, starting commit and existing worktree changes, verification scope, and rollback before editing. Use `CONTEXT.md` for domain meanings. Read existing implementations and tests before extending them; obtain approval for new files or structural changes.

Choose the risk tier by consequences, not patch size:

| Tier | Examples | Required boundary |
| --- | --- | --- |
| Low | Copy or presentation without behavior changes | Focused checks and honest evidence; an extra auditor is usually unnecessary. |
| Medium | Business logic, shared configuration, validation tools | Focused tests and a fresh-context independent audit for complex logic. |
| High | Authorization/security boundaries, deletion, production, release, hardware, external sends | Prepare the scoped change or dry run; obtain explicit authorization before the sensitive change/action. An agent review is not user approval. |

Implementation/testing authorization does not authorize deployment, service restarts, real audio devices, external Scrobbler verification, or deletion of the old playback path. Backup pushes are permitted only within the user's stated delivery authorization; inspect staged content for secrets and runtime files first.

Use supplied local tickets when available. `.scratch/` and `docs/agents/` are local conveniences, not required infrastructure. Keep task state in the current task/ticket and the existing reports; do not create a parallel task database.

## 2. Select the smallest sufficient verification

Agree on the observable interface and acceptance criteria. For a defect, first reproduce it with a regression test, then demonstrate the same test passing after the fix. Expand coverage according to affected dependencies, not a fixed requirement to run every suite.

| Change | Starting check | Escalation |
| --- | --- | --- |
| Go package | `make test PKG=./affected/package/...` (substitute the actual package) | Related packages; `make test-race PKG=...` for concurrency. |
| Player UI | `npm test -- --run src/audioplayer` from `ui/` | Relevant event-stream tests, broader UI checks, then application acceptance if behavior crosses layers. |
| Validation runner | `python3 -I -B fork/scripts/tests/test_native_playback_validation.py ValidationRunnerTests -v` | Real scoped CLI execution and inspection of its report. |
| Cross-layer playback | Selected phases of `fork/scripts/native-playback-validation.py` | Full software run and separately scoped application acceptance. |

The validator test class uses local temporary fixtures and subprocesses. Whole-file discovery also includes `AppSmokeEvidenceTests`, whose setup reserves ports; it is not equivalent to the class-only check.

`make format`, snapshot updates, generation, and some tool-installing lint targets mutate the workspace or install tools. Choose check-only commands when gathering evidence. Do not install dependencies or start services merely to make an unrelated check green.

### Scoped software runner

```sh
python3 -I -B fork/scripts/native-playback-validation.py --phase diff-check
python3 -I -B fork/scripts/native-playback-validation.py --phase ui-audioplayer --phase ui-eventstream
```

Use `--help` for supported phase IDs and timeout options. Repeated selections execute once in the runner's canonical order. Unselected phases remain `not_run` with no commands; selections that do not need Go skip Go discovery. Omitting `--phase` retains the full existing run, including its configured stress repetitions.

Selecting an `mpv-null-*` phase does not enable it: existing `--mpv-null`/environment opt-in is still required. Missing prerequisites or skipped target specs cannot count as successful execution.

The runner's default evidence location is `tmp/native-playback-validation/`, with a separate run directory containing `report.json` and command stdout/stderr logs. Reuse those artifacts rather than implementing a second report format.

### Application and physical-output boundaries

`fork/scripts/native-playback-app-smoke.py` starts isolated services and writes fixture state. Final software acceptance requires a clean source snapshot and matching dependency restoration receipt; `--allow-dirty-source` is for debugging only. Read its prerequisites before running it.

Software-only application results do not establish real MPV, PipeWire, Bluetooth, audible output, or production `RemoteAudioAdapter` acceptance. Run hardware/production checks only under their own authorization, and label every unexecuted layer explicitly.

## 3. Bind evidence to the delivered source

Freeze source while verification runs. Preserve the actual command, working directory, exit code, relevant raw output, complete log path, source identity, and applicable environment/dependency details. A post-test behavior change requires the affected checks to run again.

Interpret the existing report without weakening its gate:

- `pass` means the recorded phase passed; `fail` means its check failed; `blocked` means a prerequisite or required execution evidence was unavailable; `not_run` means it did not execute.
- `scoped_verification` identifies the explicitly selected phases. Its status is `passed` only if all selected phases pass and the source identity is complete and stable; otherwise it is `not_verified`. Without a selection it is `not_applicable`.
- The overall `gate` still rejects critical failures, blockers, and unrun phases. Physical-output validation remains critical and unrun in this software runner.
- Consequently, a scoped run can report `passed` while exiting **1** (`not_accepted` overall). Exit **0** means overall acceptance; exit **2** means argument/configuration/reporting error. Preserve the exit code rather than hiding it with unconditional success.
- Original identity fields describe the beginning of the run; `source_identity_after` records the end. `source_stability` is `stable`, `changed`, or `unavailable`. Changed or missing commit/status/content identity invalidates verification, while raw phase outcomes remain available.
- `source_content_sha256` in terminal output identifies source content. The dirty-scope digest hashes status text, not file contents; it is not a substitute.

Boundary comparison is **not snapshot isolation**. It cannot detect transient changes restored before the final capture, changes outside the fingerprint scope, or mismatched dependency caches/build outputs. Do not claim those properties from a stable result; application acceptance has separate provenance requirements.

Keep logs under ignored output directories. Review configuration, transcripts, and screenshots for sensitive content before sharing; do not commit runtime evidence or personal settings.

## 4. Audit independently and converge

For complex or high-impact changes, use a fresh-context auditor, not a fork inheriting the author's conversation. Supply the goal, acceptance criteria, fixed baseline/diff, necessary domain context, evidence, and prohibited operations. Omit the author's self-assessment.

The auditor reads the changed behavior and relevant callers, reruns decisive checks, and reports findings with location, trigger, expected/actual behavior, evidence, and status. Distinguish important defects, nits, and pre-existing issues. A possible timing permitted by an interface is not proof that the production flow necessarily triggers it; identify the violated invariant.

One auditor is the default for complex changes. Add an independent second perspective when cross-layer risk or unresolved disagreement warrants it, not merely because another model is available. Resolve disagreements through evidence, not vote counts.

Limit audit/fix rounds to three. After the first round, inspect the new diff and affected dependencies; retain unresolved findings without repeatedly reopening verified fixes. If the cap is reached, report the remaining blocker and reconsider requirements, reproduction, scope, or approach before further changes.

Turn a recurring confirmed defect into a regression test first, a deterministic check where useful, and an instruction only when code cannot capture the constraint. Record relevant failure/revert cases in existing task evidence; avoid accumulating rules for transient environment failures.

## 5. Deliver and preserve rollback

Inspect the final diff and staged paths. When commits/backups are authorized, use the existing conventional, signed-off commit format and the verified current task branch/remote. Stop on destination conflicts or missing authentication; never force-push, automatically merge a default branch, or turn a backup into a release.

A checkpoint is not proof of acceptance. `git revert <task-commit>` can undo committed code, not database changes, external sends, or device state; specify separate recovery procedures before authorizing those effects.

Deliver a concise record:

- Result and scope: verified, failed, blocked, or awaiting authorization.
- Tested source/commit, commands, exit codes, raw result excerpts, and report/log paths.
- Independent auditor count, reviewed scope, findings and disposition; say unaudited when applicable.
- Risk tier, rollback, unexecuted layers, and what the evidence cannot establish.
- Commit/push outcome and any remaining user decision.

Measure improvement through comparable tasks: human intervention, time to first useful verification, total time/token cost, review rounds, rejected findings, and subsequent rework. Treat acceleration as unmeasured until these observations exist.
