### Fixed

- A first-run step that fails with `core.internal/UNEXPECTED` now logs its cause under the trace id. One such cause
  is a `config.yaml` made unreadable or invalid after the server started.
