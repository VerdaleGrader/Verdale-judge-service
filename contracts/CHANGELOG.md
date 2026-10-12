# Contracts changelog

Versions follow semver. The version in `VERSION` must match `info.version` of both OpenAPI files.

## Rules for changing a contract

- **Additive changes are minor versions** (new optional field, new endpoint, new enum value that receivers may treat as unknown). Every receiver ignores unknown JSON fields, so producer and consumer can be deployed in any order.
- **Breaking changes are major versions** (removing or renaming a field, making a field required, changing a type or meaning). A breaking change must be announced in this file with an explicit deploy order, for example "deploy Judge 0.x first, then Core". Prefer shipping it as two additive steps instead (add the new field, migrate, then remove the old one in the next major).
- A new `Verdict` or `Language` value is additive for Judge but needs Core and Frontend to know it before Judge starts emitting it: deploy Core and Frontend first.
- After changing anything here, bump `VERSION` and both `info.version`, regenerate code in this repo (`npm run contracts:generate`), then each consumer re-pins with its own `contracts:sync` script.

## 0.4.0 (2026-10-12)

- Additive. Core public API (GRD-68, GRD-77 to GRD-82, GRD-93, GRD-96, GRD-97, GRD-108):
  - **Roles**: `Role` gains `administrator` and `observer` (owner > administrator > editor > observer > solver); a Space has exactly one owner. Observers read everything staff can read (hidden tests, solutions, every submission) but change nothing and cannot re-judge. **Profile**: `PublicUser.avatar_url` (the provider's picture) and validation of the display name.
  - **Tracks and Collections**: Space > Track > Collection > Problem with ordering, archiving and privacy (`space` or `members`); Track members and invitations (link or per person); progression per Track and Collection and a staff view of everybody's progression. Solvers and visitors only reach problems that sit in a Collection of a Track they can access.
  - **Space-wide submissions (staff only)**: `GET /spaces/{slug}/submissions` (filters, sorting, paging), `/versions`, `/by-member`, `/by-member/{username}/problems`, `/by-track`, `/by-track/{trackSlug}/problems`, `/by-problem`, and `GET /submissions/{submissionId}/tests/{position}` (input, expected output, program output and stderr of one test as windows of lines, hidden tests included). `CreateSubmissionRequest.track` is optional (the Track the solver works in; else the first Track that reaches the problem) and a submission remembers its Track.
  - **Capture limits**: Core asks Judge to keep the start of each test's output (64 KiB of stdout, 8 KiB of stderr, 2 MiB per job); only people who may read hidden tests can read it.
  - **Testcases page**: `PUT /tests` saves all changes at once with a `revision` check (409 `TESTS_CHANGED`), `POST /tests/files` stores a file (multipart `file` or JSON `{text}`) for a later save, `GET /tests/{testId}/files/{side}` returns a window of lines; downloads accept `inline=true`. A testcase may have no expected output yet: `TestCase.output_sha256` and `output_size_bytes` become nullable, `TestSummary.missing_output_count` and `TestCaseList.revision` are new, and publishing is blocked by the new `outputs` publish check (409 `PUBLISH_BLOCKED`) until every testcase has one.
  - **Generate**: `POST /generate` (202), `GET /generate/{jobId}` and `POST /generate/{jobId}/stop` create expected outputs by running a reference solution on the inputs; nothing changes until the author saves the results.
  - **PDF statement (GRD-73)**: `statement_mode` (`markdown` or `pdf`) on the problem and its version, `PUT`/`DELETE .../statement-pdf` and `GET .../statement.pdf` (checked for the same visibility as the problem, sent with `nosniff` and a sandbox CSP), `constraints.statement_pdf` (10 MiB, 10 pages), and a statement publish check that follows the mode. Creating a problem already takes the title, slug and first content in one request (GRD-72).
- Judge API gains `kind: generate`, the optional `capture` request field and `stdout`, `stdout_size_bytes`, `stderr`, `stderr_size_bytes` in test results; `tests[].output` is no longer required (it is for `submission` and `verify`).
- Deploy order: Judge first (it accepts the new optional fields and `generate`), then Core (it asks for them and applies its migrations), then Frontend re-pins with `contracts:sync`. A client that still reads `output_sha256` as a string must handle null before Core is deployed with testcases that have no expected output; Frontend therefore ships after Core.

## 0.3.0 (2026-10-11)

- Additive. Core public API gains problems, tests, reference solutions and submissions (GRD-21 to GRD-27, GRD-43 to GRD-47): `/api/v1/spaces/{slug}/problems` and everything under it (editor view, publish, archive, images, tests, solutions, submissions), `/api/v1/submissions/{id}` with its Server-Sent Events stream and re-judge, and `/api/v1/assets/{name}`. New `Language`, `Verdict` and `TestVerdict` schemas use the same codes as the Judge contract.
- Judge API is unchanged except that the callback is now received by Core at `POST /internal/judge/result` (so `callback_url` is `{core}/internal/judge/result`, which matches the `…/internal/judge/` prefix already in Judge's allow-list). The version is bumped only to keep both files aligned.
- Deploy order: Core first (it creates the tables with its migration), then Frontend re-pins with `contracts:sync`. Judge needs no deploy for this version.

## 0.2.0 (2026-10-11)

- Additive. Core public API gains accounts and Spaces (GRD-10 to GRD-19): OAuth sign-in (`/api/v1/auth/*`), `/api/v1/me`, `/api/v1/users/{username}`, Spaces, members and invitations. New `session` security scheme (cookie `vd_session`) and the CSRF `Origin` rule. Judge API is unchanged (version bumped only to keep both files aligned).
- Deploy order: Core first, then Frontend re-pins with `contracts:sync`.

## 0.1.0 (2026-10-11)

- First version: Core public API skeleton (`/health`, `/ready`, `ErrorResponse`), Judge internal API (`POST /internal/jobs`, `GET /internal/jobs/{job_id}`, result callback), HMAC signing spec and test vectors.
