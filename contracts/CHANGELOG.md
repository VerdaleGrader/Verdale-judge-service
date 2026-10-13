# Contracts changelog

Versions follow semver. The version in `VERSION` must match `info.version` of both OpenAPI files.

## Rules for changing a contract

- **Additive changes are minor versions** (new optional field, new endpoint, new enum value that receivers may treat as unknown). Every receiver ignores unknown JSON fields, so producer and consumer can be deployed in any order.
- **Breaking changes are major versions** (removing or renaming a field, making a field required, changing a type or meaning). A breaking change must be announced in this file with an explicit deploy order, for example "deploy Judge 0.x first, then Core". Prefer shipping it as two additive steps instead (add the new field, migrate, then remove the old one in the next major).
- A new `Verdict` or `Language` value is additive for Judge but needs Core and Frontend to know it before Judge starts emitting it: deploy Core and Frontend first.
- After changing anything here, bump `VERSION` and both `info.version`, regenerate code in this repo (`npm run contracts:generate`), then each consumer re-pins with its own `contracts:sync` script.

## 0.8.0 (2026-10-13)

- Additive. Publish gate and versions (GRD-50 to GRD-54, GRD-116, GRD-117):
  - **Version number `vMajor.Minor`.** `ProblemVersion` gains `major` (same as `number`), `minor`, `label`; `status` gains `verifying` and `verified` (`draft` -> `verifying` -> `verified` -> `published`). `Problem` and `ProblemSummary` gain `version_minor` and `version_label`. The first publish is v1.0.
  - **Verify.** `POST .../verify` starts a verification run (the primary reference solution against every test, hidden ones included, as a Judge `verify` job); `GET .../verify` returns the latest `VerificationRun` (status, per-test results, slowest time and peak memory, `valid` = still matches the content hash). `ProblemEditor.verification` carries it.
  - **Publish rules.** `PublishCheck.key` gains `limits`, `hidden_tests` and `verified`; `ProblemConstraints.publish.min_hidden_tests` (configurable). `ProblemEditor.open_version` is the published version solvers use while `version` may be a Major draft. Publishing now requires a valid passing verification.
  - **Minor and Major updates.** `POST .../updates` (`PublishedUpdateRequest`, `UpdateOutcome`): Core classifies the update from the fields that change (`ProblemConstraints.version_fields` lists the Major and Minor groups) and from `force_major`. Minor is applied directly to the open version (Minor + 1, history kept, optimistic lock with `expected_version`, 409 `VERSION_CONFLICT`); Major creates a draft of the next Major, which has to pass verify and publish. `POST/DELETE .../draft` start or discard a Major draft (needed to change tests and reference solutions).
  - **History.** `GET .../history` (`VersionHistoryPage`) lists updates and state changes (who, when, fields, old and new value) for staff.
  - `SubmissionSummary` gains `version_major` and `outdated` (a newer Major is published; the result is kept, not re-judged).
- Judge contract: only the version is bumped. The `verify` job kind already exists.
- Error codes added: `VERIFY_BLOCKED`, `VERIFY_IN_PROGRESS`, `NO_VERIFICATION`, `VERSION_CONFLICT`, `DRAFT_EXISTS`, `NO_DRAFT`, `NOT_PUBLISHED`, `MAJOR_FIELD_LOCKED`.
- Deploy order: Core first (migrations), then Frontend re-pins with `contracts:sync`. Judge needs no deploy.

## 0.7.0 (2026-10-13)

- Additive. `SubmissionSummary.number` (GRD-115): the submission's place among its author's submissions of the problem, oldest first ("Submission #3" in the solve page). Present on every summary: create, list, `best` and detail.
- Deploy order: Core first, then Frontend re-pins with `contracts:sync`. No migration. Judge needs no deploy; its contract only has its version bumped.

## 0.6.0 (2026-10-13)

- Additive. Previous submissions of one's own, with per-test summary and Best Submission (GRD-113, for GRD-112):
  - `SubmissionSummary.test_runs` (new `TestRun` schema): the verdicts of the tests in judging order as runs, in every list item, create response and detail. Replaces fetching each submission to draw its progress bar.
  - `SubmissionPage.best`: with `scope=mine`, the caller's best submission of the problem (most tests passed, then the earliest sent; never an unfinished one, an ISE or one that passed no test). Null for `scope=all`.
  - Nothing else changes: `GET /api/v1/submissions/{id}` already returns `source` to its author and staff, and a problem has at most 100 tests (`TEST_LIMITS.max_count`), so its `results` stay small.
- Deploy order: Core first (migration `submission_best_index` adds an index), then Frontend re-pins with `contracts:sync`. Judge needs no deploy; its contract only has its version bumped.

## 0.5.0 (2026-10-12)

- Additive. Roles, Space-wide submissions for staff and the testcases page with Generate (GRD-93, GRD-96, GRD-97, GRD-108):
  - **Roles.** `Role` gains `administrator` and `observer` (strongest first: owner > administrator > editor > observer > solver). A Space has exactly one owner; `PATCH .../members/{username}` with `role: owner` hands the Space over (the previous owner becomes an administrator). Administrators run members, invitations and settings except visibility, archiving and handover (owner only), and manage Tracks and Collections like editors; they cannot create or change other administrators or the owner. Observers are read-only staff: they read hidden tests, solutions, drafts, every Track, every submission and everyone's progress, and change nothing (they cannot re-judge). `CreateInviteRequest.role` accepts `administrator`, `editor`, `observer`, `solver`. Leaving or removing the owner is refused with 409.
  - **Space-wide submissions** (owner, administrator, editor, observer): `GET /api/v1/spaces/{slug}/submissions` and `/versions`, `/by-member`, `/by-member/{username}/problems`, `/by-track`, `/by-track/{trackSlug}/problems`, `/by-problem`, and `GET /api/v1/submissions/{submissionId}/tests/{position}` (input, expected output, program output and stderr of one test as windows of lines). New schemas `SpaceSubmissionPage`, `SpaceSubmissionRow`, `MemberRollupPage`, `MemberProblemPage`, `TrackRollup`, `TrackSubmissionProblems`, `ProblemRollupPage`, `SubmissionTestDetail`, `TextSlice` and their parts.
  - `POST .../submissions` accepts an optional `track` (the Track the solver is working in); it is remembered and shown to staff.
  - **Testcases page.** `POST .../tests/files` (stage a file by SHA-256), `PUT .../tests` (`saveTestCases`: the whole list at once with the `revision` it started from; 409 `TESTS_CHANGED`), `GET .../tests/{testId}/files/{side}` (window of lines). `TestCaseList.revision`; `TestCase.output_sha256` and `output_size_bytes` are nullable (a testcase may wait for its expected output); `TestSummary.missing_output_count`; `PublishCheck.key` gains `outputs`.
  - **Generate.** `POST .../generate`, `GET .../generate/{jobId}`, `POST .../generate/{jobId}/stop` run a saved reference solution on the inputs to create expected outputs.
  - **Judge contract.** `JobRequest.kind` gains `generate`; `JobRequest.capture` (`Capture`) asks Judge to send back what the program printed; `TestResult` gains `stdout`, `stdout_size_bytes`, `stderr`, `stderr_size_bytes`; `TestCase.output` may be left out (generate only).
- Behaviour that is not a field change: the Space's `owner` role is unique (a Space that had several owners keeps the oldest and the others became administrators); an invitation to `administrator` can only be created or revoked by the owner.
- Deploy order: Judge first (new job kind and capture are ignored by an old Core and optional for it), then Core (migrations `add_administrator_observer_roles`, `single_owner_per_space`, `submission_test_outputs`, `optional_output_and_generate`, `submission_track`), then Frontend re-pins with `contracts:sync`.

## 0.4.0 (2026-10-12)

- Additive. Core public API gains Tracks and Collections and the rule that solvers reach problems only through a Track they can see (GRD-68, GRD-72 to GRD-82):
  - `/api/v1/spaces/{slug}/tracks` and below (CRUD, archive/restore, whole-set reorder, Collections in a Track, members, invitations, `progress`), `/api/v1/spaces/{slug}/collections` and below (CRUD, archive/restore, add/remove/reorder problems). New schemas `Track`, `TrackDetail`, `Collection`, `CollectionDetail`, `TrackProblem`, `Progress`, `UserProgress`, `MemberProgress`.
  - `GET /api/v1/problem-constraints` (the editor's limits, before any problem exists).
  - `PUT` and `GET /api/v1/spaces/{slug}/problems/{problemSlug}/statement/pdf`; `statement_mode` (`markdown` or `pdf`) and `statement_pdf` on `Problem` and `ProblemVersion`; `PATCH` problem accepts `statement_mode`; `constraints.pdf`.
  - `avatar_url` (nullable) on `Me` and `PublicUser`; `track_name` on `CreateSpaceRequest`; `track` (nullable) on `Invite` and `InviteOutcome`.
- Behaviour change that is not a field change: `GET /spaces/{slug}/problems`, `GET .../problems/{problemSlug}` and `POST .../submissions` now answer for solvers, outsiders and visitors only for problems that sit in a Collection of a Track they can see (404 otherwise). Owners and editors are unaffected. Every existing Space gets a starter Track "General" with a Collection "General" holding its existing problems, so nothing a solver could see before disappears.
- New required response fields (`avatar_url`, `statement_mode`, `statement_pdf`, `track`) are additive for readers that ignore unknown fields.
- Deploy order: Core first (migration `add_tracks_and_collections` runs before it serves), then Frontend re-pins with `contracts:sync`. Judge needs no deploy.

## 0.3.0 (2026-10-11)

- Additive. Core public API gains problems, tests, reference solutions and submissions (GRD-21 to GRD-27, GRD-43 to GRD-47): `/api/v1/spaces/{slug}/problems` and everything under it (editor view, publish, archive, images, tests, solutions, submissions), `/api/v1/submissions/{id}` with its Server-Sent Events stream and re-judge, and `/api/v1/assets/{name}`. New `Language`, `Verdict` and `TestVerdict` schemas use the same codes as the Judge contract.
- Judge API is unchanged except that the callback is now received by Core at `POST /internal/judge/result` (so `callback_url` is `{core}/internal/judge/result`, which matches the `…/internal/judge/` prefix already in Judge's allow-list). The version is bumped only to keep both files aligned.
- Deploy order: Core first (it creates the tables with its migration), then Frontend re-pins with `contracts:sync`. Judge needs no deploy for this version.

## 0.2.0 (2026-10-11)

- Additive. Core public API gains accounts and Spaces (GRD-10 to GRD-19): OAuth sign-in (`/api/v1/auth/*`), `/api/v1/me`, `/api/v1/users/{username}`, Spaces, members and invitations. New `session` security scheme (cookie `vd_session`) and the CSRF `Origin` rule. Judge API is unchanged (version bumped only to keep both files aligned).
- Deploy order: Core first, then Frontend re-pins with `contracts:sync`.

## 0.1.0 (2026-10-11)

- First version: Core public API skeleton (`/health`, `/ready`, `ErrorResponse`), Judge internal API (`POST /internal/jobs`, `GET /internal/jobs/{job_id}`, result callback), HMAC signing spec and test vectors.
