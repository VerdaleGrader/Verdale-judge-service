# Contracts changelog

Versions follow semver. The version in `VERSION` must match `info.version` of both OpenAPI files.

## Rules for changing a contract

- **Additive changes are minor versions** (new optional field, new endpoint, new enum value that receivers may treat as unknown). Every receiver ignores unknown JSON fields, so producer and consumer can be deployed in any order.
- **Breaking changes are major versions** (removing or renaming a field, making a field required, changing a type or meaning). A breaking change must be announced in this file with an explicit deploy order, for example "deploy Judge 0.x first, then Core". Prefer shipping it as two additive steps instead (add the new field, migrate, then remove the old one in the next major).
- A new `Verdict` or `Language` value is additive for Judge but needs Core and Frontend to know it before Judge starts emitting it: deploy Core and Frontend first.
- After changing anything here, bump `VERSION` and both `info.version`, regenerate code in this repo (`npm run contracts:generate`), then each consumer re-pins with its own `contracts:sync` script.

## 0.3.0 (2026-10-11)

- Additive. Core public API gains problems, tests, reference solutions and submissions (GRD-21 to GRD-27, GRD-43 to GRD-47): `/api/v1/spaces/{slug}/problems` and everything under it (editor view, publish, archive, images, tests, solutions, submissions), `/api/v1/submissions/{id}` with its Server-Sent Events stream and re-judge, and `/api/v1/assets/{name}`. New `Language`, `Verdict` and `TestVerdict` schemas use the same codes as the Judge contract.
- Judge API is unchanged except that the callback is now received by Core at `POST /internal/judge/result` (so `callback_url` is `{core}/internal/judge/result`, which matches the `…/internal/judge/` prefix already in Judge's allow-list). The version is bumped only to keep both files aligned.
- Deploy order: Core first (it creates the tables with its migration), then Frontend re-pins with `contracts:sync`. Judge needs no deploy for this version.

## 0.2.0 (2026-10-11)

- Additive. Core public API gains accounts and Spaces (GRD-10 to GRD-19): OAuth sign-in (`/api/v1/auth/*`), `/api/v1/me`, `/api/v1/users/{username}`, Spaces, members and invitations. New `session` security scheme (cookie `vd_session`) and the CSRF `Origin` rule. Judge API is unchanged (version bumped only to keep both files aligned).
- Deploy order: Core first, then Frontend re-pins with `contracts:sync`.

## 0.1.0 (2026-10-11)

- First version: Core public API skeleton (`/health`, `/ready`, `ErrorResponse`), Judge internal API (`POST /internal/jobs`, `GET /internal/jobs/{job_id}`, result callback), HMAC signing spec and test vectors.
