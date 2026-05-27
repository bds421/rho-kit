# Changes

## Unreleased — v2.0

- Initial release.
- Outbound HMAC-signed webhook dispatcher with retry on 5xx +
  network errors; 4xx gives up.
- Auto delivery-id (UUIDv7) for receiver-side replay protection.
- Kit headers (signature / timestamp / delivery-id) overwrite
  caller-supplied entries to prevent accidental suppression.
