# Agent execution and evidence

Use this guide when changing code, configuration, or tracked project guidance. `AGENTS.md` is the portable entry point; confirm that the session loads it. Claude Code's AGENTS support depends on version and instruction settings, and an ancestor project `CLAUDE.md` can take precedence. If instructions are absent, explicitly provide the entry point rather than silently changing user settings.

## 1. Bound the task

Record the goal, affected behavior or invariant, starting commit and existing worktree changes, verification scope, and rollback before editing. Use `CONTEXT.md` for domain meanings. Read existing implementations and tests before extending them; obtain approval for new files or structural changes.

Choose the risk tier by consequences, not patch size:

| Tier   | Examples                                                                                   | Required boundary                                                                                                                             |
| ------ | ------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------- |
| Low    | Copy or presentation without behavior changes                                              | Focused checks and honest evidence; an extra auditor is usually unnecessary.                                                                  |
| Medium | Business logic, shared configuration, validation tools                                     | Focused tests and a fresh-context independent audit for complex logic.                                                                        |
| High   | Authorization/security boundaries, deletion, production, release, hardware, external sends | Prepare the scoped change or dry run; obtain explicit authorization before the sensitive change/action. An agent review is not user approval. |

Implementation/testing authorization does not authorize deployment, service restarts, real audio devices, external Scrobbler verification, or deletion of the old playback path. Backup pushes are permitted only within the user's stated delivery authorization; inspect staged content for secrets and runtime files first.

Use supplied local tickets when available. `.scratch/` and `docs/agents/` are local conveniences, not required infrastructure. Keep task state in the current task/ticket and the existing reports; do not create a parallel task database.

## 2. Select the smallest sufficient verification

Agree on the observable interface and acceptance criteria. For a defect, first reproduce it with a regression test, then demonstrate the same test passing after the fix. Expand coverage according to affected dependencies, not a fixed requirement to run every suite.

| Change               | Starting check                                                                                 | Escalation                                                                                              |
| -------------------- | ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Go package           | `make test PKG=./affected/package/...` (substitute the actual package)                         | Related packages; `make test-race PKG=...` for concurrency.                                             |
| Player UI            | `npm test -- --run src/audioplayer` from `ui/`                                                 | Relevant event-stream tests, broader UI checks, then application acceptance if behavior crosses layers. |
| Validation runner    | `python3 -I -B fork/scripts/tests/test_native_playback_validation.py ValidationRunnerTests -v` | Real scoped CLI execution and inspection of its report.                                                 |
| Cross-layer playback | Selected phases of `fork/scripts/native-playback-validation.py`                                | Full software run and separately scoped application acceptance.                                         |

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

For work outside the native-player ticket set covered by [section 6](#6-native-player-ticket-autonomy), limit audit/fix rounds to three and reconsider requirements, reproduction, scope, or approach at that cap. For every task, inspect the new diff and affected dependencies after the first round; retain unresolved findings without repeatedly reopening verified fixes.

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

## 6. Native-player ticket autonomy

### Authorization and scope

The user authorized autonomous completion of `.scratch/native-player-unified-control/issues` on 2026-11-03 without a fixed implementation, retry, or audit-round limit. For this ticket set, that authorization supersedes the previous three-cycle instructions and count-only stop/approval gates, including historical ticket records. It does not change the defaults for unrelated work or grant production, hardware, release, external-service, destructive, dependency-installation, or file-structure permissions.

Iteration count is a diagnostic metric, not a stop condition. Continue an available implementation, test, diagnosis, or review step without asking for another repair allowance. Keep historical failures and superseded stop records intact; identify the current authorization in the existing ticket. An unchanged failing test, an auditor's ACCEPT label, or the removal of the round cap does not waive an acceptance criterion.

### Execution and convergence

1. **Recover state.** Read the supplied spec, implementation spec, plan, affected tickets and their dependencies. Verify the branch, remote, dirty paths, evidence and active agents before dispatch. Use existing ticket records as the task ledger; preserve unfinished changes and earlier evidence. After a restart, resume or retire an existing assignment before creating another writer.
2. **Select one deliverable.** The parent chooses the smallest coherent slice whose prerequisites permit implementation and records its invariant, observable acceptance criteria, source identity and verification scope. Independent read-only discovery may run in parallel. Allow one writer per overlapping source/test scope; freeze that scope during audit and evidence collection.
3. **Reproduce and repair.** Prefer a persistent behavioral RED before changing production code. Read the implement skill as requested, without invoking it. Send subagent assignments in English with exact scope, completion criteria and prohibited operations. Run long operations in task agents. Extend existing modules and tests; obtain approval for structural changes under the existing rules.
4. **Converge on evidence.** On a repeated failure, compare the new diff and actual failed assertion with the prior hypothesis before another repair. Correct faulty reproduction, inspect the entire input class, or obtain an independent diagnostic/design read when necessary. Change the approach when evidence refutes it; do not repeat an unchanged hypothesis, lower the contract, or keep an obsolete fixture to obtain green tests. A recovery path must be implemented and tested, not described only in a comment. Record round counts, rejected hypotheses and remaining findings in the existing ticket without creating a parallel tracking system.
5. **Verify and adjudicate.** Follow sections 2–4 for final-source checks and independent review. Give a fresh-context auditor the contract, baseline/diff and raw evidence, not the author's conclusion. Reuse that auditor for focused rechecks of repairs; obtain another perspective for unresolved substantive disagreement, not votes. The parent checks each finding and the actual acceptance clauses, including omitted versus null fields and observation versus control authority. Resolve disagreements through code and execution evidence. Request permission only when the next necessary action genuinely lies outside existing authorization.
6. **Accept and deliver.** Accept a slice only after its blocking findings are resolved and required checks have run on its final source. Accept a whole ticket only when every acceptance item has the required evidence and dependencies. Mark unavailable layers `UNVERIFIED`, not PASS. Commit only the accepted scope, inspect staged content, verify it after commit, then back up the current task branch under section 5. An unrelated dirty candidate must not enter a documentation or other scoped commit. The parent records acceptance and directs the next eligible slice; checkpoint delivery is not whole-ticket acceptance.

### Subagent quota fallback

Count consecutive, distinct subagent call attempts that terminate with an explicit resource/quota-exhausted error for the default or explicitly selected non-Haiku model during the current parent run, across assignments. Record the attempt identity, model and actual error in the existing task record; retain the count across renaming, resumption and context recovery. Count repeated notifications for the same failed attempt once. Test failures, permission denials and other errors are not quota failures; a successful subagent work result resets the consecutive-failure count.

After **more than three quota failures (the fourth distinct failed attempt)**, use a fresh, non-fork subagent with explicit `model: "haiku"` for the pending assignment and subsequent subagent calls in that run. A fork ignores model overrides, and messaging an existing agent does not change its model. Preserve scope, permission boundaries, evidence and independent-review requirements; end or retire the previous overlapping assignment before transferring writer ownership. Record the model switch and carry forward the actual task state rather than launching duplicate work.

Before the threshold, retry only necessary pending work with backoff, honoring any reported retry delay; do not create empty calls to exhaust the counter. Do not assume quota pools are independent. If Haiku also fails for quota or is unavailable, preserve the work and report the resource blocker under the loop lifecycle rules instead of claiming completion or changing credentials, settings or permissions. This is a model-selection fallback, not a new implementation-round cap, and it does not automatically switch the parent model.

### Loop lifecycle

Use completion notifications from tracked agents and jobs as the primary wake signal; do not poll them or dispatch duplicate work. For external events without tracked notifications, use a bounded monitor covering success and failure. While useful work is running, keep one dynamic-loop fallback at 1200–1800 seconds and pass the original prompt unchanged. Report only meaningful state changes; an idle notification is not new evidence or authorization.

If one task is externally blocked, record the exact prerequisite and continue other dependency-ready work when it is genuinely independent. Pause the loop only when the user stops it, all required work is accepted, or no authorized executable step remains. In the last case, report the blocking evidence and the specific permission, artifact or environment condition needed; cancel the wakeup instead of waiting indefinitely. A failed test or an exhausted historical round count alone does not justify a pause. Completion means the required software acceptance and delivery are recorded; any separately authorized hardware, production or external-service gates must remain explicit, never silently waived.

### Reusable loop prompt

The following is the canonical task prompt. It delegates procedure to this guide rather than creating another set of rules. It resumes existing work; it does not assume that a historical ACCEPT or BLOCKED record is still current.

```text
/loop 自主完成 .scratch/native-player-unified-control/issues 中的全部实施任务，按依赖顺序逐项实施、独立验收、交付；由你作为 parent/integrator 确认验收并下达下一步指令。向我用中文报告，给 subagent 的任务书使用英文。

先读 AGENTS.md、fork/docs/agent-execution.md（尤其第 6 节自主执行政策）、CONTEXT.md，以及 .scratch/native-player-unified-control/ 下的 spec.md、implementation-spec.md、implementation-plan.md 和相关 issues；只读参考 /mattpocock-skills:implement 的 SKILL.md，不直接调用该 skill。先核对实际分支、remote、工作区、已有 agents 和证据，再继续尚未完成的任务，不重复派发 writer，不丢弃未提交改动。

本组 tickets 已获授权：不设置实施、修正或审计轮数上限，不再因为“三次额度”停工或反复申请继续修复。按执行指南自主复现、诊断、调整方案并继续验证；保持验收标准、独立审计和权限边界。历史停工记录保留为历史，不是当前额度限制。subagent 遇到资源配额耗尽时，按第 6 节 Subagent quota fallback 规则统计独立失败并切换 Haiku；重复通知不重复计数，切换模型不降低验收标准。

优先从实际未解决的阻塞项继续。若现有 caller-scope 候选的 R12 仍未关闭，先补齐 deviceName 省略、显式 null、raw 设备来源及同代/跨代输入的回归，再完成最小一致修复和独立复核；若已关闭，依据最新证据选择下一个依赖就绪的任务。不要用旧状态或提示词中的缺陷名称代替现场核验。

每个切片执行测试先行的最小修复、最终源码检查、独立审计和 parent 验收，记录准确命令、退出码、原始日志、源码标识及残留事项；只在既有 ticket 和忽略的证据目录中维护进度。观察状态不能等同于播放控制权，切片通过不能冒充整票或完整 T14-O 验收，前置条件未满足时不提前实现 proof/P3。修复反复失败时先更新诊断或设计，不重复无效补丁，也不降低标准。

通过验收后，只提交该已验收范围，使用签署提交并推送经核对的当前任务分支；不把其他未验收脏文件混入，不向硬编码默认分支推送，不强推。然后由你选择并派发下一项。生产部署、重启、真实音频设备、外部 Scrobbler、发布及其他未授权操作仍需单独授权，不能把未执行层写成 PASS。

按执行指南维护动态 loop：agent 完成通知为主要唤醒，运行中用 1200–1800 秒兜底，原样传回本提示词；无变化时简短等待，不重复催问或运行同一组检查。有可执行的授权步骤就继续；仅在用户停止、任务全部验收交付，或所有剩余工作确实缺少必要授权/环境/材料且无其他就绪任务时停止，并清楚报告证据、阻塞原因及所需动作。最终汇总各票验收、提交/备份、证据位置，以及仍需另行授权的验证层。
```
