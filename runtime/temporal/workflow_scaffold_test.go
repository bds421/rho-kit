package temporal_test

// This file is a copy-paste-ready scaffold for downstream services
// writing their first Temporal workflow against the kit's adapter.
// It exercises the SDK's testsuite.WorkflowTestSuite — an in-memory
// scheduler that drives the exact same code path the production
// worker would, without requiring a Temporal cluster or testcontainer.
//
// What it proves:
//   - A workflow function compiles against the kit's expected idioms.
//   - Activity registration + ExecuteActivity round-trips correctly.
//   - The test environment surfaces both result and error correctly,
//     so service-level workflow tests can assume the harness works.
//
// Services that adopt the kit's [temporal.Worker] should mirror this
// shape — register workflows on the kit's Worker.Registry() in
// production, register them on TestWorkflowEnvironment in tests.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// echoActivity returns its input unchanged. Stand-in for "any
// side-effecting call the workflow needs to make" (HTTP request,
// database write, model invocation). Activity functions take a real
// context.Context and may use SDK helpers like activity.GetLogger.
func echoActivity(_ context.Context, in string) (string, error) {
	if in == "" {
		return "", errors.New("echo: empty input")
	}
	return in, nil
}

// echoWorkflow runs echoActivity once with the supplied input. It
// stands in for any workflow that orchestrates one or more activities
// — the only kit-specific contract is that you register the workflow
// on the worker (production) or test environment (tests) by the same
// reference the caller invokes by.
func echoWorkflow(ctx workflow.Context, in string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
	})
	var out string
	if err := workflow.ExecuteActivity(ctx, echoActivity, in).Get(ctx, &out); err != nil {
		return "", err
	}
	return out, nil
}

func TestEchoWorkflow_HappyPath(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(echoWorkflow)
	env.RegisterActivity(echoActivity)

	env.ExecuteWorkflow(echoWorkflow, "hello")

	require.True(t, env.IsWorkflowCompleted(), "workflow did not complete within harness timeout")
	require.NoError(t, env.GetWorkflowError())

	var result string
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, "hello", result)
}

func TestEchoWorkflow_ActivityErrorPropagates(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(echoWorkflow)
	env.RegisterActivity(echoActivity)

	env.ExecuteWorkflow(echoWorkflow, "")

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err, "empty input must surface the activity error to the caller")
	assert.Contains(t, err.Error(), "empty input")
}

// TestEchoWorkflow_MockedActivity demonstrates how downstream services
// stub out activities that talk to real infrastructure (S3, OpenAI,
// Postgres). The test harness intercepts the registered name and
// returns the canned value; the workflow code path is unchanged.
func TestEchoWorkflow_MockedActivity(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(echoWorkflow)
	env.RegisterActivity(echoActivity)
	env.OnActivity(echoActivity, mock.Anything, "trigger").Return("mocked", nil)

	env.ExecuteWorkflow(echoWorkflow, "trigger")

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result string
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, "mocked", result)
}

// Compile-time guards that the kit-recommended activity/workflow
// shapes match what the SDK expects. If these break the example test
// won't compile, signalling the scaffold drifted from the SDK contract.
var (
	_ func(context.Context, string) (string, error)  = echoActivity
	_ func(workflow.Context, string) (string, error) = echoWorkflow
	_ activity.RegisterOptions                       = activity.RegisterOptions{} //nolint:exhaustruct
)
