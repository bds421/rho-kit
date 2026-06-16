package storage

import (
	"context"
	"errors"
	"fmt"
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

// validatingLister mirrors every in-tree backend Lister: it runs the incoming
// options through ValidateListOptions (which rejects MaxKeys > MaxListPageSize)
// before yielding. fakeLister skips that step, which is why the MaxKeys+1 probe
// bug went unnoticed — only a validating backend surfaces it. It also records
// the largest MaxKeys it was asked for so tests can assert the probe never
// exceeds the cap.
type validatingLister struct {
	all     []ObjectInfo
	maxSeen int
}

func (v *validatingLister) List(_ context.Context, _ string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return func(yield func(ObjectInfo, error) bool) {
		if opts.MaxKeys > v.maxSeen {
			v.maxSeen = opts.MaxKeys
		}
		if err := ValidateListOptions(opts); err != nil {
			yield(ObjectInfo{}, err)
			return
		}
		count := 0
		for _, o := range v.all {
			if opts.StartAfter != "" && o.Key <= opts.StartAfter {
				continue
			}
			if opts.MaxKeys > 0 && count >= opts.MaxKeys {
				return
			}
			count++
			if !yield(o, nil) {
				return
			}
		}
	}
}

func TestListPage_MaxPageSizeDoesNotOverflowProbe(t *testing.T) {
	t.Parallel()
	// A validating backend rejects MaxKeys > MaxListPageSize. ListPage must
	// not hand it a MaxKeys+1 probe at the maximum page size, or every such
	// call would fail validation instead of returning a page.
	lister := &validatingLister{all: []ObjectInfo{{Key: "a"}, {Key: "b"}}}

	page, err := ListPage(context.Background(), lister, "", ListOptions{MaxKeys: MaxListPageSize})
	require.NoError(t, err)
	assert.Equal(t, []ObjectInfo{{Key: "a"}, {Key: "b"}}, page.Objects)
	assert.False(t, page.Truncated)
	assert.Empty(t, page.NextStartAfter)
	assert.LessOrEqual(t, lister.maxSeen, MaxListPageSize, "probe must never exceed MaxListPageSize")
}

func TestListPage_MaxPageSizeFullPageTruncated(t *testing.T) {
	t.Parallel()
	// At the boundary the MaxKeys+1 probe is unavailable, so ListPage must
	// settle truncation with a follow-up peek. Build a full max-size page
	// plus one extra object so the peek finds more.
	all := make([]ObjectInfo, 0, MaxListPageSize+1)
	for i := range MaxListPageSize + 1 {
		all = append(all, ObjectInfo{Key: fmt.Sprintf("k%08d", i)})
	}
	lister := &validatingLister{all: all}

	page, err := ListPage(context.Background(), lister, "", ListOptions{MaxKeys: MaxListPageSize})
	require.NoError(t, err)
	assert.Len(t, page.Objects, MaxListPageSize)
	assert.True(t, page.Truncated, "a full max-size page with more objects must report Truncated")
	assert.Equal(t, all[MaxListPageSize-1].Key, page.NextStartAfter)
	assert.LessOrEqual(t, lister.maxSeen, MaxListPageSize, "probe must never exceed MaxListPageSize")
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
