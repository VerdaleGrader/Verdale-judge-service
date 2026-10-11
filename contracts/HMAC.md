# Request signing (HMAC-SHA256)

Every call between Core and Judge is signed, in both directions:

| Direction                                        | Who signs | Who verifies | Keyring       |
| ------------------------------------------------ | --------- | ------------ | ------------- |
| `POST /internal/jobs`, `GET /internal/jobs/{id}` | Core      | Judge        | core-to-judge |
| Result callback to `callback_url`                | Judge     | Core         | judge-to-core |

The two keyrings are independent: a leaked callback key cannot be used to submit jobs.

## Headers

| Header                | Value                                                            |
| --------------------- | ---------------------------------------------------------------- |
| `X-Verdale-Key-Id`    | Id of the key used, `[A-Za-z0-9_-]{1,64}`                        |
| `X-Verdale-Timestamp` | Unix time in whole seconds, decimal, when the request was signed |
| `X-Verdale-Signature` | `v1=` followed by the lowercase hex HMAC-SHA256                  |

## What is signed

The string to sign is five lines joined by `\n` (no trailing newline):

```
v1
<X-Verdale-Timestamp>
<HTTP method, upper case>
<request path including the query string, exactly as sent, e.g. /internal/jobs>
<lowercase hex SHA-256 of the raw request body; for an empty body, the SHA-256 of zero bytes>
```

`signature = hex(HMAC-SHA256(secret, string_to_sign))`

The secret is used as raw bytes. Secrets must be at least 32 bytes of random data.

## Verifying

The receiver rejects the request with `401` and the standard error body when:

| error_code          | When                                                        |
| ------------------- | ----------------------------------------------------------- |
| `MISSING_SIGNATURE` | Any of the three headers is missing or malformed            |
| `UNKNOWN_KEY_ID`    | The key id is not in the receiver's keyring                 |
| `STALE_TIMESTAMP`   | `abs(now - timestamp) > 300` seconds                        |
| `INVALID_SIGNATURE` | The recomputed signature differs (compare in constant time) |

The receiver must verify against the raw body bytes before parsing JSON.

Replay inside the 300-second window is harmless by design: job creation is idempotent by `job_id` and result callbacks are idempotent by `job_id`.

## Rotating a key without downtime

1. Add the new key id and secret to the verifier's keyring (both keys now accepted). Deploy the verifier.
2. Switch the signer to the new key id. Deploy the signer.
3. After the longest retry window has passed (callbacks retry for up to 24 hours by default), remove the old key from the verifier.

## Test vectors

`hmac-test-vectors.json` holds inputs and expected signatures. Both the TypeScript implementation (Core) and the Go implementation (Judge) run them in their unit tests, so the two cannot drift apart.
