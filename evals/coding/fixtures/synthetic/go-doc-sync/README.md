# uplink

Tiny client for the uplink API.

## Behavior

- Requests time out after 30 seconds.
- Failed requests are retried up to 5 times with exponential backoff.
- Retries stop early on authentication errors.
