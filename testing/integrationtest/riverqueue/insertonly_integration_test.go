//go:build integration

package riverqueue_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/queue/riverqueue/v2/periodic"
)

type answerArgs struct{ Text string }

func (answerArgs) Kind() string { return "test.answer" }

type answerWorker struct {
	river.WorkerDefaults[answerArgs]
	got chan string
}

func (w *answerWorker) Work(_ context.Context, job *river.Job[answerArgs]) error {
	w.got <- job.Args.Text
	return nil
}

// The role that enqueues registers no worker for the kind it inserts; the
// worker role, which does, works it.
func TestIntegration_InsertOnlyEnqueuesAKindOnlyTheWorkerRoleWorks(t *testing.T) {
	pool := periodicPool(t)
	inserter, err := periodic.New(periodic.Options{Schema: "public", MaxAttempts: 1}).InsertOnly(pool, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	_, err = inserter.Insert(t.Context(), answerArgs{Text: "hello"}, nil)
	require.NoError(t, err, "a kind only the worker role registers")

	worker := &answerWorker{got: make(chan string, 1)}
	r := periodic.New(periodic.Options{Schema: "public", MaxAttempts: 1})
	periodic.AddWorker(r, worker)
	runRegistry(t, r, pool)
	require.Equal(t, "hello", <-worker.got)
}
