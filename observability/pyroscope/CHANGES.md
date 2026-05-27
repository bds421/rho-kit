# Changes

## Unreleased — v2.0

- Initial release.
- Component(cfg, opts...) wraps pyroscope-go as a lifecycle.Component.
- Default profile types: CPU + alloc objects/space + inuse objects/space.
- slog adapter routes pyroscope-go's logger interface to slog.
