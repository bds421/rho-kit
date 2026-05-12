package tenant

import (
	"context"
	"testing"
)

var (
	benchTenantID  ID
	benchTenantOK  bool
	benchTenantErr error
	benchTenantCtx context.Context
)

func BenchmarkValidateID(b *testing.B) {
	var err error
	for i := 0; i < b.N; i++ {
		err = ValidateID("tenant-123_abc.example")
	}
	benchTenantErr = err
}

func BenchmarkWithID(b *testing.B) {
	id := MustNewID("tenant-123")
	base := context.Background()
	var ctx context.Context
	var err error
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ctx, err = WithID(base, id)
		if err != nil {
			b.Fatal(err)
		}
	}
	benchTenantCtx = ctx
}

func BenchmarkFromContext(b *testing.B) {
	ctx, _ := WithID(context.Background(), MustNewID("tenant-123"))
	var id ID
	var ok bool
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		id, ok = FromContext(ctx)
	}
	benchTenantID = id
	benchTenantOK = ok
}

func BenchmarkRequired(b *testing.B) {
	ctx, _ := WithID(context.Background(), MustNewID("tenant-123"))
	var id ID
	var err error
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		id, err = Required(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
	benchTenantID = id
}
