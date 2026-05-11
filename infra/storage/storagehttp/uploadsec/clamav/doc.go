// Package clamav implements uploadsec.Scanner using the ClamAV clamd
// INSTREAM protocol.
//
// The package has no third-party dependencies. It speaks the clamd wire
// protocol directly so importing the adapter does not pull a large SDK into
// services that only need upload validation.
package clamav
