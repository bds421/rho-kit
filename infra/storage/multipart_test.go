package storage

import (
	"context"
	"testing"
	"time"
)

type multipartListerStorage struct{ multipartStorage }

func (m *multipartListerStorage) ListMultipartUploads(context.Context, string, MultipartUploadListOptions) (MultipartUploadPage, error) {
	return MultipartUploadPage{}, nil
}

func TestValidateMultipartUploadListOptions(t *testing.T) {
	valid := MultipartUploadListOptions{MaxUploads: 100, InitiatedBefore: time.Now().UTC()}
	if err := ValidateMultipartUploadListOptions(valid); err != nil {
		t.Fatalf("valid options: %v", err)
	}
	for name, mutate := range map[string]func(*MultipartUploadListOptions){
		"zero limit":       func(value *MultipartUploadListOptions) { value.MaxUploads = 0 },
		"large limit":      func(value *MultipartUploadListOptions) { value.MaxUploads = MaxMultipartUploadPageSize + 1 },
		"orphan upload id": func(value *MultipartUploadListOptions) { value.UploadIDMarker = "upload" },
		"local cutoff":     func(value *MultipartUploadListOptions) { value.InitiatedBefore = time.Now() },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if err := ValidateMultipartUploadListOptions(value); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}

func TestAsMultipartUploadLister(t *testing.T) {
	backend := &multipartListerStorage{}
	if _, ok := AsMultipartUploadLister(backend); !ok {
		t.Fatal("direct multipart upload lister was not discovered")
	}
	wrapper := &wrapper{inner: backend}
	if _, ok := AsMultipartUploadLister(wrapper); !ok {
		t.Fatal("wrapped multipart upload lister was not discovered")
	}
	opaque := &opaqueWrapper{inner: backend}
	if _, ok := AsMultipartUploadLister(opaque); ok {
		t.Fatal("multipart upload lister bypassed opaque decorator")
	}
}
