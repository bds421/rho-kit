// Package awssm is the AWS Secrets Manager backend for the kit's
// [infra/secrets.Loader] contract. Construct an aws-sdk-go-v2
// secretsmanager.Client out of band (so you own the AWS session
// lifecycle, region, role assumption) and hand it to [New].
//
// # Use this package when
//
//   - Your service runs on AWS and rotates secrets via Secrets Manager.
//   - You want the kit's [infra/secrets.CachedLoader] in front of
//     Secrets Manager (Secrets Manager's API has a tight RPS budget).
//
// # Do NOT use this package for
//
//   - KEK wrapping. Use [crypto/envelope/awskms] instead — KMS and
//     Secrets Manager are different services with different semantics.
//   - Storing data that should never be retrieved (one-way hashes).
package awssm
