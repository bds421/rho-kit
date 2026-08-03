package versionrelocation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

func TestRelocationPinsHistoricalAccessAndLeavesEnumerationPhysical(
	t *testing.T,
) {
	ctx := context.Background()
	backend := membackend.NewImmutable()
	if err := backend.Put(
		ctx,
		"objects/a",
		bytes.NewBufferString("rehydrated"),
		storage.ObjectMeta{Size: 10},
	); err != nil {
		t.Fatal(err)
	}
	target, _, err := backend.CurrentVersion(ctx, "objects/a")
	if err != nil {
		t.Fatal(err)
	}
	old := storage.ObjectVersion{
		Key: "objects/a", Version: "source-provider-generation",
	}
	wrapped, err := New(
		backend,
		ResolverFunc(func(
			_ context.Context,
			object storage.ObjectVersion,
		) (storage.ObjectVersion, error) {
			if object == old {
				return target, nil
			}
			return object, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	exact, ok := storage.AsExactVersionStore(wrapped)
	if !ok {
		t.Fatal("relocated exact-version capability is absent")
	}
	body, _, err := exact.GetVersion(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(body)
	closeErr := body.Close()
	if err != nil || closeErr != nil || string(content) != "rehydrated" {
		t.Fatalf(
			"GetVersion(historical) = (%q, %v, %v)",
			content,
			err,
			closeErr,
		)
	}
	current, _, err := exact.CurrentVersion(ctx, old.Key)
	if err != nil || current != target {
		t.Fatalf("CurrentVersion = (%+v, %v), want raw target", current, err)
	}
	pager, ok := storage.AsExactVersionPageLister(wrapped)
	if !ok {
		t.Fatal("physical page enumeration did not survive relocation")
	}
	page, err := pager.VersionsPage(
		ctx,
		"",
		storage.ExactVersionPageOptions{
			Limit: storage.MaxExactVersionListEntries,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 1 || page.Objects[0] != target {
		t.Fatalf("physical versions = %+v, want raw generations", page.Objects)
	}
	if err = exact.DeleteVersion(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err = backend.GetVersion(
		ctx, target,
	); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("target generation survived relocated deletion: %v", err)
	}
}

func TestRelocationRejectsCrossKeyAndResolverFailure(t *testing.T) {
	ctx := context.Background()
	backend := membackend.NewImmutable()
	if err := backend.Put(
		ctx,
		"objects/a",
		bytes.NewBufferString("body"),
		storage.ObjectMeta{Size: 4},
	); err != nil {
		t.Fatal(err)
	}
	object, _, err := backend.CurrentVersion(ctx, "objects/a")
	if err != nil {
		t.Fatal(err)
	}
	for name, resolver := range map[string]Resolver{
		"cross key": ResolverFunc(func(
			context.Context,
			storage.ObjectVersion,
		) (storage.ObjectVersion, error) {
			return storage.ObjectVersion{
				Key: "objects/b", Version: object.Version,
			}, nil
		}),
		"failure": ResolverFunc(func(
			context.Context,
			storage.ObjectVersion,
		) (storage.ObjectVersion, error) {
			return storage.ObjectVersion{}, errors.New("injected")
		}),
	} {
		t.Run(name, func(t *testing.T) {
			wrapped, newErr := New(backend, resolver)
			if newErr != nil {
				t.Fatal(newErr)
			}
			exact, ok := storage.AsExactVersionStore(wrapped)
			if !ok {
				t.Fatal("exact-version capability is absent")
			}
			if _, _, getErr := exact.GetVersion(
				ctx, object,
			); getErr == nil {
				t.Fatal("resolver failure was accepted")
			}
		})
	}
}

func TestRelocationForwardsBackendClose(t *testing.T) {
	backend := &closingBackend{Storage: membackend.NewImmutable()}
	wrapped, err := New(
		backend,
		ResolverFunc(func(
			_ context.Context,
			object storage.ObjectVersion,
		) (storage.ObjectVersion, error) {
			return object, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(wrapped); err != nil {
		t.Fatal(err)
	}
	if !backend.closed {
		t.Fatal("relocation wrapper did not close its backend")
	}
}

type closingBackend struct {
	storage.Storage
	closed bool
}

func (backend *closingBackend) Unwrap() storage.Storage {
	return backend.Storage
}

func (backend *closingBackend) Close() error {
	backend.closed = true
	return nil
}
