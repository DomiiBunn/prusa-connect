### Fixed

- An account, API credential or pairing request under `/api/v1/auth/`, `GET /api/v1/principal` or a first-run
  request under `/api/v1/setup` that fails with `core.internal/UNEXPECTED` now logs its cause under the trace id.
  One such cause is an account store that could not be written.
