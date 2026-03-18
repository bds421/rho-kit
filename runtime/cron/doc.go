// Package cron provides a scheduled task runner backed by [robfig/cron/v3].
//
// The Scheduler wraps cron with structured logging, Prometheus metrics, panic
// recovery, and context-aware shutdown. It implements [lifecycle.Component] so
// it can be registered with [lifecycle.Runner] or used via [app.Builder.WithCron].
//
// Jobs receive a context that is cancelled when the scheduler shuts down,
// allowing graceful cleanup of long-running periodic tasks.
//
// # Cron Syntax
//
// Standard five-field cron expressions are supported:
//
//	┌───────────── minute (0–59)
//	│ ┌───────────── hour (0–23)
//	│ │ ┌───────────── day of month (1–31)
//	│ │ │ ┌───────────── month (1–12)
//	│ │ │ │ ┌───────────── day of week (0–6, Sun=0)
//	│ │ │ │ │
//	* * * * *
//
// Predefined schedules: @yearly, @monthly, @weekly, @daily, @hourly.
// Fixed intervals: @every 5m, @every 1h30m.
//
// # Usage
//
//	s := cron.New(logger)
//	s.Add("cleanup", "@daily", func(ctx context.Context) error {
//	    return repo.DeleteExpired(ctx)
//	})
//	// s.Start(ctx) blocks until ctx is cancelled.
package cron
