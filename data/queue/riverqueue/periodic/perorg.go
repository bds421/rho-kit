package periodic

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const orgTaskAttempts = 3

// OrgTask is a run of a task for one organization, under its scope.
type OrgTask func(ctx context.Context, orgID string) error

// OrgLister lists the organizations a per-organization task runs for.
type OrgLister func(ctx context.Context) ([]string, error)

// EveryPerOrg runs task for every organization list returns, once per
// interval. A dispatcher only lists the organizations; each one becomes its
// own job, worked in parallel across replicas with the organization as the
// scope, so a slow organization delays only itself.
func (r *Registry) EveryPerOrg(name string, interval time.Duration, list OrgLister, task OrgTask) {
	r.orgTasks[name] = task
	r.Every("dispatch:"+name, interval, func(ctx context.Context) error {
		orgs, err := list(ctx)
		if err != nil || len(orgs) == 0 {
			return err
		}
		client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
		if err != nil {
			return err
		}
		params := make([]river.InsertManyParams, len(orgs))
		for i, org := range orgs {
			params[i] = river.InsertManyParams{Args: orgArgs{Name: name, OrgID: org}}
		}
		_, err = client.InsertMany(ctx, params)
		return err
	})
}

func (r *Registry) runOrgTask(ctx context.Context, job *river.Job[orgArgs]) error {
	task, ok := r.orgTasks[job.Args.Name]
	if !ok {
		return river.JobCancel(fmt.Errorf("periodic: unknown organization task %q", job.Args.Name))
	}
	if r.opts.OrgContext != nil {
		var err error
		if ctx, err = r.opts.OrgContext(ctx, job.Args.OrgID); err != nil {
			return river.JobCancel(err)
		}
	}
	return task(ctx, job.Args.OrgID)
}

type orgArgs struct {
	Name  string `json:"name"`
	OrgID string `json:"orgId"`
	// Trigger names the change that asked for this run, so each change gets a
	// run that starts after it, while a repeated delivery of the same change
	// does not.
	Trigger string `json:"trigger,omitempty"`
}

func (orgArgs) Kind() string { return "rho.org_task" }

// At most one job per task and organization waits or runs; the next
// dispatch picks up whatever a failed run left.
func (orgArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: orgTaskAttempts, UniqueOpts: UniqueWhileWaiting()}
}

// RunOrgTask queues a task for one organization now because of a change
// (trigger, empty for none); the task must serialize itself per
// organization.
func RunOrgTask(ctx context.Context, client *river.Client[pgx.Tx], name, orgID, trigger string) error {
	_, err := client.Insert(ctx, orgArgs{Name: name, OrgID: orgID, Trigger: trigger}, nil)
	return err
}
