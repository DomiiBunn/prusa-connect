### Fixed

- An account, API credential or pairing request under `/api/v1/auth/`, `GET /api/v1/principal` or a first-run
  request under `/api/v1/setup` that fails with `core.internal/UNEXPECTED` now logs its cause under the trace id.
  One such cause is an account store that could not be written.
- A caller that closes an account, API credential or pairing request under `/api/v1/auth/`, or
  `GET /api/v1/principal`, before the answer is now counted as `core.validation/REQUEST_ABANDONED` with status
  499. It was logged as an error with `core.internal/UNEXPECTED`.
