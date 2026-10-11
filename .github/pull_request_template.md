## What and why

<!-- Story key (GRD-n) and a short summary. -->

## How it was tested

- [ ] `make check` passes (lint, unit tests, contract check)
- [ ] `make test-integration` passes when the sandbox, runners or queue changed

## Contract

- [ ] No contract change, or the change was made in Verdale-core-service/contracts first and re-pinned with `make contracts-sync`

## Checklist

- [ ] Config changes are in `config.json` and `.env.example`, with no secrets
- [ ] Logs carry `job_id` / `request_id` and no secrets or hidden test content
