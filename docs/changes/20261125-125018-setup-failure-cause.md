### Fixed

- A first-run step that fails with `core.internal/UNEXPECTED` now names its cause in the log line under the
  trace id, such as a `config.yaml` that became unreadable or invalid after the server started
