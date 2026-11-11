// Package periodic runs a service's recurring tasks on River: a task every
// interval, a task per organization every interval (a dispatcher lists the
// organizations, each becomes its own job, worked in parallel across
// replicas under the organization's scope), and a run on demand.
//
// A periodic run is unique while it waits or runs: a finished one blocks no
// later run. River's default unique states include completed jobs, which
// would drop every later run of a task until completed jobs are cleaned
// up; [UniqueWhileWaiting] is that policy for a service's own job kinds.
//
// The job kinds are rho.periodic and rho.org_task, in the schema the
// [Options] name.
package periodic
