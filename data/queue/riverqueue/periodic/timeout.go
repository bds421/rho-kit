package periodic

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
)

// rescueMargin is how long past the longest timeout a job still running
// counts as stuck: its worker died, and River runs it again (or, a periodic
// task with one attempt, discards it, so the next tick inserts it anew).
const rescueMargin = time.Minute

// Timeout lets a task, periodic or per organization, run longer than
// River's default of a minute, which would cancel a long run midway.
func (r *Registry) Timeout(name string, d time.Duration) {
	r.timeouts[name] = d
	r.Runs(d)
}

// Runs tells the registry that a worker added through Workers may run up
// to d, so a job is not taken for stuck while it still works.
func (r *Registry) Runs(d time.Duration) {
	r.longest = max(r.longest, d)
}

// rescueAfter is when a running job counts as stuck: a periodic task its
// dead worker left running blocks its next run until then, so not River's
// hour but just past the longest any job may run.
func (r *Registry) rescueAfter() time.Duration {
	return r.longest + rescueMargin
}

// overran logs a task River cancelled at its timeout, which would
// otherwise fail silently and start from the beginning at the next tick.
func overran(ctx context.Context, name string, timeout time.Duration, err error) error {
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.ErrorContext(ctx, "periodic: a task ran out of its time", "task", name, "timeout", cmp.Or(timeout, river.JobTimeoutDefault), "error", err)
	}
	return err
}

type periodicWorker struct {
	river.WorkerDefaults[periodicArgs]
	r *Registry
}

func (w periodicWorker) Work(ctx context.Context, job *river.Job[periodicArgs]) error {
	return overran(ctx, job.Args.Name, w.Timeout(job), w.r.runTask(ctx, job))
}

func (w periodicWorker) Timeout(job *river.Job[periodicArgs]) time.Duration {
	return w.r.timeouts[job.Args.Name]
}

type orgWorker struct {
	river.WorkerDefaults[orgArgs]
	r *Registry
}

func (w orgWorker) Work(ctx context.Context, job *river.Job[orgArgs]) error {
	return overran(ctx, job.Args.Name, w.Timeout(job), w.r.runOrgTask(ctx, job))
}

func (w orgWorker) Timeout(job *river.Job[orgArgs]) time.Duration {
	return w.r.timeouts[job.Args.Name]
}
