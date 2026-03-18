// Package membackend provides an in-memory [storage.Storage] implementation.
//
// It is intended for unit tests where a real backend (S3, local, SFTP) is
// impractical. The backend is thread-safe and implements Storage, Lister,
// and Copier interfaces.
//
//	backend := membackend.New()
//	err := backend.Put(ctx, "file.txt", strings.NewReader("hello"), storage.ObjectMeta{})
package membackend
