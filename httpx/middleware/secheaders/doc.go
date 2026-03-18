// Package secheaders provides middleware that sets security response headers.
//
// By default it adds:
//   - X-Content-Type-Options: nosniff — prevents MIME-sniffing attacks
//   - X-Frame-Options: DENY — prevents clickjacking via iframe embedding
//
// Both headers are set on every response before the next handler runs.
package secheaders
