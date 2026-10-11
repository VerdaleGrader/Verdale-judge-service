# Contracts changelog

Versions follow semver. The version in `VERSION` must match `info.version` of both OpenAPI files.

## Rules for changing a contract

- **Additive changes are minor versions** (new optional field, new endpoint, new enum value that receivers may treat as unknown). Every receiver ignores unknown JSON fields, so producer and consumer can be deployed in any order.
- **Breaking changes are major versions** (removing or renaming a field, making a field required, changing a type or meaning). A breaking change must be announced in this file with an explicit deploy order, for example "deploy Judge 0.x first, then Core". Prefer shipping it as two additive steps instead (add the new field, migrate, then remove the old one in the next major).
- A new `Verdict` or `Language` value is additive for Judge but needs Core and Frontend to know it before Judge starts emitting it: deploy Core and Frontend first.
- After changing anything here, bump `VERSION` and both `info.version`, regenerate code in this repo (`npm run contracts:generate`), then each consumer re-pins with its own `contracts:sync` script.

## 0.1.0 (2026-10-11)

- First version: Core public API skeleton (`/health`, `/ready`, `ErrorResponse`), Judge internal API (`POST /internal/jobs`, `GET /internal/jobs/{job_id}`, result callback), HMAC signing spec and test vectors.
