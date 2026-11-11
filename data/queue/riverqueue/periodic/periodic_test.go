package periodic

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func newRegistry(periodicAttempts int) *Registry {
	return New(Options{Schema: "river", MaxAttempts: 1, PeriodicAttempts: periodicAttempts})
}

// A tick's run is tried as often as the options say, and its retry still
// keeps the next tick from queueing a second run; a run on demand runs
// once.
func TestATicksRunIsTriedAsOftenAsConfigured(t *testing.T) {
	if got := tick(newRegistry(5).opts.PeriodicAttempts); got.MaxAttempts != 5 || !slices.Contains(got.UniqueOpts.ByState, rivertype.JobStateRetryable) {
		t.Errorf("a tick: %+v", got)
	}
	if got := (periodicArgs{}).InsertOpts(); got.MaxAttempts != 1 || !got.UniqueOpts.ByArgs || slices.Contains(got.UniqueOpts.ByState, rivertype.JobStateCompleted) {
		t.Errorf("on demand: %+v", got)
	}
}

// A task given a timeout runs under it; the others keep River's default.
func TestATaskRunsUnderItsOwnTimeout(t *testing.T) {
	r := newRegistry(1)
	r.EveryPerOrg("long", time.Hour, func(context.Context) ([]string, error) { return nil, nil }, func(context.Context, string) error { return nil })
	r.Timeout("long", 30*time.Minute)
	if d := (orgWorker{r: r}).Timeout(&river.Job[orgArgs]{JobRow: &rivertype.JobRow{}, Args: orgArgs{Name: "long"}}); d != 30*time.Minute {
		t.Errorf("the long task runs %s", d)
	}
	if d := (periodicWorker{r: r}).Timeout(&river.Job[periodicArgs]{JobRow: &rivertype.JobRow{}, Args: periodicArgs{Name: "dispatch:long"}}); d != 0 {
		t.Errorf("its dispatcher runs %s", d)
	}
}

// A job running past the longest any job may run counts as stuck soon
// after, not after River's hour, so a periodic task a dead worker left
// running is not blocked that long.
func TestAJobCountsAsStuckJustPastTheLongestTimeout(t *testing.T) {
	r := newRegistry(1)
	if d := r.rescueAfter(); d != 2*time.Minute {
		t.Errorf("with River's default timeout a job counts as stuck after %s", d)
	}
	r.Timeout("cases.prune", 30*time.Minute)
	AddWorker(r, slowWorker{})
	if d := r.rescueAfter(); d != 31*time.Minute {
		t.Errorf("with a 30-minute task a job counts as stuck after %s", d)
	}
}

// A task River cancels at its timeout is logged, through the client's
// logger, with its name, not left to fail silently and start over at the
// next tick.
func TestATaskThatRunsOutOfItsTimeIsLogged(t *testing.T) {
	var logged bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	r := newRegistry(1)
	r.logger = slog.New(slog.NewTextHandler(&logged, nil))
	if err := r.overran(ctx, "workspace.purge", 0, ctx.Err()); err == nil {
		t.Fatal("the task's error was swallowed")
	}
	if !strings.Contains(logged.String(), "task=workspace.purge") || !strings.Contains(logged.String(), "timeout=1m0s") {
		t.Errorf("logged %q", logged.String())
	}
	logged.Reset()
	_ = r.overran(t.Context(), "workspace.purge", 0, errors.New("refused"))
	if logged.Len() != 0 {
		t.Errorf("an ordinary failure was logged as running out of time: %q", logged.String())
	}
}

// An organization's run gets the scope the options derive; one the scope
// refuses is cancelled, not retried.
func TestAnOrganizationsRunIsScoped(t *testing.T) {
	type key struct{}
	r := New(Options{Schema: "river", MaxAttempts: 1, OrgContext: func(ctx context.Context, orgID string) (context.Context, error) {
		if orgID == "bad" {
			return nil, errors.New("not an organization")
		}
		return context.WithValue(ctx, key{}, orgID), nil
	}})
	var scoped string
	r.EveryPerOrg("sync", time.Hour, nil, func(ctx context.Context, orgID string) error {
		scoped, _ = ctx.Value(key{}).(string)
		return nil
	})
	if err := r.runOrgTask(t.Context(), &river.Job[orgArgs]{Args: orgArgs{Name: "sync", OrgID: "o1"}}); err != nil || scoped != "o1" {
		t.Errorf("ran with scope %q, %v", scoped, err)
	}
	err := r.runOrgTask(t.Context(), &river.Job[orgArgs]{Args: orgArgs{Name: "sync", OrgID: "bad"}})
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) {
		t.Errorf("a refused scope: %v", err)
	}
}

type slowArgs struct{}

func (slowArgs) Kind() string { return "test.slow" }

type slowWorker struct{ river.WorkerDefaults[slowArgs] }

func (slowWorker) Timeout(*river.Job[slowArgs]) time.Duration { return 10 * time.Minute }

func (slowWorker) Work(context.Context, *river.Job[slowArgs]) error { return nil }

// A worker added with a timeout of its own is given that long before it
// counts as stuck, so a job running within its time is never run twice.
func TestAnAddedWorkersTimeoutCountsForTheRescue(t *testing.T) {
	r := newRegistry(1)
	AddWorker(r, slowWorker{})
	if d := r.rescueAfter(); d != 11*time.Minute {
		t.Errorf("with a 10-minute worker a job counts as stuck after %s", d)
	}
}
