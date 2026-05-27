// Package gcpsm is the GCP Secret Manager backend for the kit's
// [infra/secrets.Loader] contract. Construct a
// secretmanager.Client out of band (so you own the GCP credential
// lifecycle) and hand it to [New].
//
// # Use this package when
//
//   - Your service runs on GCP and rotates secrets via Secret Manager.
//   - You want the kit's [infra/secrets.CachedLoader] in front of the
//     Secret Manager API (the latest-version path is cheap to call but
//     still better cached for hot rotations).
//
// # Do NOT use this package for
//
//   - KEK wrapping. Use [crypto/envelope/gcpkms] instead.
package gcpsm
