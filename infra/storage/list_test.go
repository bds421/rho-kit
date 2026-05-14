package storage

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLister yields a fixed set of objects in deterministic order so
// ListPage tests can pin the truncation contract without touching a
// real backend (which would force this test out of the storage package).
type fakeLister struct {
	all []ObjectInfo
	err error // optional yield error after yieldErrAfter items
}

func (f fakeLister) List(_ context.Context, _ string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return func(yield func(ObjectInfo, error) bool) {
		count := 0
		for _, o := range f.all {
			if opts.StartAfter != "" && o.Key <= opts.StartAfter {
				continue
			}
			if opts.MaxKeys > 0 && count >= opts.MaxKeys {
				return
			}
			if f.err != nil && count == 1 {
				yield(ObjectInfo{}, f.err)
				return
			}
			count++
			if !yield(o, nil) {
				return
			}
		}
	}
}

func TestListPage_TruncatedAndCursor(t *testing.T) {
	t.Parallel()
	all := []ObjectInfo{
		{Key: "a"},
		{Key: "b"},
		{Key: "c"},
		{Key: "d"},
		{Key: "e"},
	}
	lister := fakeLister{all: all}

	page, err := ListPage(context.Background(), lister, "", ListOptions{MaxKeys: 2})
	require.NoError(t, err)
	assert.Equal(t, []ObjectInfo{{Key: "a"}, {Key: "b"}}, page.Objects)
	assert.True(t, page.Truncated, "MaxKeys+1 probe must detect remaining objects")
	assert.Equal(t, "b", page.NextStartAfter)

	// Resume via NextStartAfter — exhausts the list, no more truncation.
	tail, err := ListPage(context.Background(), lister, "", ListOptions{
		MaxKeys:    10,
		StartAfter: page.NextStartAfter,
	})
	require.NoError(t, err)
	assert.Equal(t, []ObjectInfo{{Key: "c"}, {Key: "d"}, {Key: "e"}}, tail.Objects)
	assert.False(t, tail.Truncated)
	assert.Empty(t, tail.NextStartAfter)
}

func TestListPage_ExactlyFullNotTruncated(t *testing.T) {
	t.Parallel()
	all := []ObjectInfo{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	lister := fakeLister{all: all}

	page, err := ListPage(context.Background(), lister, "", ListOptions{MaxKeys: 3})
	require.NoError(t, err)
	assert.Equal(t, all, page.Objects)
	assert.False(t, page.Truncated, "MaxKeys+1 probe finds nothing → not truncated")
	assert.Empty(t, page.NextStartAfter)
}

func TestListPage_UnlimitedForwardsAll(t *testing.T) {
	t.Parallel()
	all := []ObjectInfo{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	lister := fakeLister{all: all}

	page, err := ListPage(context.Background(), lister, "", ListOptions{MaxKeys: 0})
	require.NoError(t, err)
	assert.Equal(t, all, page.Objects)
	assert.False(t, page.Truncated)
}

func TestListPage_IteratorErrorSurfaces(t *testing.T) {
	t.Parallel()
	want := errors.New("backend down")
	lister := fakeLister{all: []ObjectInfo{{Key: "a"}, {Key: "b"}}, err: want}

	_, err := ListPage(context.Background(), lister, "", ListOptions{MaxKeys: 10})
	require.ErrorIs(t, err, want)
}

func TestListPage_RejectsNilLister(t *testing.T) {
	t.Parallel()
	_, err := ListPage(context.Background(), nil, "", ListOptions{})
	require.Error(t, err)
}
