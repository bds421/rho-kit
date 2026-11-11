package periodic

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const (
	periodicWorkers = 2
	stopTimeout     = 10 * time.Second
)

// Task is a run of a periodic task.
type Task func(ctx context.Context) error

// Periodic declares a task that runs every interval, for a module to hand
// its tasks to the registry.
type Periodic struct {
	Name  string
	Every time.Duration
	Task  Task
}

// Options configure a registry.
type Options struct {
	// Schema is the Postgres schema River's tables are in, created by
	// [Migrate].
	Schema string
	// MaxAttempts is River's default for jobs of the workers added
	// through [Registry.Workers].
	MaxAttempts int
	// PeriodicAttempts is how often a tick's run is tried: 1 leaves a
	// failed run to the next tick; more tries it again with River's
	// backoff, past a restart of a dependency, while its retry still keeps
	// the next tick from queueing a second run. A run asked for on demand
	// ([RunTask]) runs once, since it may not repeat what it did.
	PeriodicAttempts int
	// OrgContext scopes a per-organization run to its organization; nil
	// runs it unscoped.
	OrgContext func(ctx context.Context, orgID string) (context.Context, error)
}

// Registry holds a service's tasks and the River configuration that runs
// them.
type Registry struct {
	opts     Options
	workers  *river.Workers
	queues   map[string]river.QueueConfig
	tasks    map[string]Task
	orgTasks map[string]OrgTask
	periodic []*river.PeriodicJob
	timeouts map[string]time.Duration
	longest  time.Duration
}

// New makes a registry; o.Schema and o.MaxAttempts are required,
// o.PeriodicAttempts defaults to 1.
func New(o Options) *Registry {
	if o.Schema == "" || o.MaxAttempts < 1 {
		panic("periodic: Options need a Schema and MaxAttempts")
	}
	if o.PeriodicAttempts < 1 {
		o.PeriodicAttempts = 1
	}
	r := &Registry{
		opts:     o,
		workers:  river.NewWorkers(),
		queues:   map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: periodicWorkers}},
		tasks:    map[string]Task{},
		orgTasks: map[string]OrgTask{},
		timeouts: map[string]time.Duration{},
		longest:  river.JobTimeoutDefault,
	}
	river.AddWorker[periodicArgs](r.workers, periodicWorker{r: r})
	river.AddWorker[orgArgs](r.workers, orgWorker{r: r})
	return r
}

// Queue adds a queue for a service's own workers.
func (r *Registry) Queue(name string, maxWorkers int) {
	r.queues[name] = river.QueueConfig{MaxWorkers: maxWorkers}
}

// Workers are River's, for a service to add its own job kinds.
func (r *Registry) Workers() *river.Workers {
	return r.workers
}

// Every runs task every interval, and once at start.
func (r *Registry) Every(name string, interval time.Duration, task Task) {
	r.tasks[name] = task
	opts := tick(r.opts.PeriodicAttempts)
	r.periodic = append(r.periodic, river.NewPeriodicJob(
		river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) { return periodicArgs{Name: name}, &opts },
		&river.PeriodicJobOpts{RunOnStart: true},
	))
}

// Add registers each declared task.
func (r *Registry) Add(tasks ...Periodic) {
	for _, p := range tasks {
		r.Every(p.Name, p.Every, p.Task)
	}
}

// Task registers a task that runs only when asked for ([RunTask]).
func (r *Registry) Task(name string, task Task) {
	r.tasks[name] = task
}

// Client is a River client that works the registered tasks and the
// service's own workers.
func (r *Registry) Client(pool *pgxpool.Pool, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema:               r.opts.Schema,
		Queues:               r.queues,
		Workers:              r.workers,
		PeriodicJobs:         r.periodic,
		MaxAttempts:          r.opts.MaxAttempts,
		RescueStuckJobsAfter: r.rescueAfter(),
		Logger:               logger,
	})
}

// InsertOnly is a River client that enqueues jobs without working them,
// for roles that leave the work to a worker role.
func (r *Registry) InsertOnly(pool *pgxpool.Pool, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema: r.opts.Schema, Workers: r.workers, MaxAttempts: r.opts.MaxAttempts, Logger: logger,
	})
}

func (r *Registry) runTask(ctx context.Context, job *river.Job[periodicArgs]) error {
	task, ok := r.tasks[job.Args.Name]
	if !ok {
		return river.JobCancel(fmt.Errorf("periodic: unknown task %q", job.Args.Name))
	}
	return task(ctx)
}

// Run starts the client and stops it when ctx ends, for a lifecycle's
// background function.
func Run(client *river.Client[pgx.Tx]) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := client.Start(ctx); err != nil {
			return err
		}
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
		defer cancel()
		return client.Stop(stopCtx)
	}
}

// RunTask asks for one run of a registered task now, unless one already
// waits; any client of the schema may ask, an insert-only one too.
func RunTask(ctx context.Context, client *river.Client[pgx.Tx], name string) error {
	_, err := client.Insert(ctx, periodicArgs{Name: name}, nil)
	return err
}

type periodicArgs struct {
	Name string `json:"name"`
}

func (periodicArgs) Kind() string { return "rho.periodic" }

// A run on demand runs once; a tick's attempts are set where it is inserted.
func (periodicArgs) InsertOpts() river.InsertOpts { return tick(1) }

func tick(attempts int) river.InsertOpts {
	return river.InsertOpts{MaxAttempts: attempts, UniqueOpts: UniqueWhileWaiting()}
}

// waiting are the states in which a job blocks another with the same
// arguments.
var waiting = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
	rivertype.JobStateRetryable, rivertype.JobStateScheduled,
}

// UniqueWhileWaiting lets one job with the same arguments wait or run at a
// time; a finished one blocks no later job.
func UniqueWhileWaiting() river.UniqueOpts {
	return river.UniqueOpts{ByArgs: true, ByState: waiting}
}
