//go:build integration

package leaderk8s

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	k8slease "github.com/bds421/rho-kit/infra/leaderelection/k8slease/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// uniqueName scopes each test to its own Lease so parallel runs on
// the same fake clientset cannot collide on the lock object.
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// TestElector_AcquiresAndShutsDownOnCtxCancel pins the happy-path
// contract: a single Elector against an empty fake apiserver acquires
// the Lease, fires OnAcquired, releases on ctx cancel, and surfaces
// OnLost exactly once.
func TestElector_AcquiresAndShutsDownOnCtxCancel(t *testing.T) {
	client := fake.NewClientset()
	ns := "kit-system"
	name := uniqueName(t, "leader")

	e := k8slease.New(client, ns, name, "pod-a",
		k8slease.WithLeaseDuration(2*time.Second),
		k8slease.WithRenewDeadline(time.Second),
		k8slease.WithRetryPeriod(100*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acquired := make(chan struct{}, 1)
	lost := atomic.Int64{}
	runErr := make(chan error, 1)

	go func() {
		runErr <- e.Run(ctx, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) {
				select {
				case acquired <- struct{}{}:
				default:
				}
			},
			OnLost: func() { lost.Add(1) },
		})
	}()

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("OnAcquired was never invoked within 5s")
	}

	assert.True(t, e.IsLeader(), "IsLeader must return true after OnAcquired")

	cancel()
	select {
	case err := <-runErr:
		// LeaderElector returns when ctx cancels; Run wraps ctx.Err.
		assert.ErrorIs(t, err, context.Canceled,
			"Run must propagate ctx.Err() on caller-initiated shutdown")
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s after ctx cancel")
	}
	assert.Equal(t, int64(1), lost.Load(),
		"OnLost must fire exactly once for the acquired term on shutdown")
	assert.False(t, e.IsLeader(), "IsLeader must be false after OnLost")
}

// TestElector_TwoCompetingElectorsOnSameLease pins the cross-replica
// exclusion contract: a second Elector targeting the same Lease must
// not acquire while the first holds it. After the first cancels and
// the Lease is released (ReleaseOnCancel=true), the second eventually
// wins.
func TestElector_TwoCompetingElectorsOnSameLease(t *testing.T) {
	client := fake.NewClientset()
	ns := "kit-system"
	name := uniqueName(t, "competing")

	e1 := k8slease.New(client, ns, name, "pod-1",
		k8slease.WithLeaseDuration(2*time.Second),
		k8slease.WithRenewDeadline(time.Second),
		k8slease.WithRetryPeriod(100*time.Millisecond),
	)
	e2 := k8slease.New(client, ns, name, "pod-2",
		k8slease.WithLeaseDuration(2*time.Second),
		k8slease.WithRenewDeadline(time.Second),
		k8slease.WithRetryPeriod(100*time.Millisecond),
	)

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	e1Acquired := make(chan struct{}, 1)
	e2Acquired := make(chan struct{}, 1)

	go func() {
		_ = e1.Run(ctx1, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) { e1Acquired <- struct{}{} },
		})
	}()

	select {
	case <-e1Acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("first elector never acquired")
	}

	go func() {
		_ = e2.Run(ctx2, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) { e2Acquired <- struct{}{} },
		})
	}()

	// e2 must be blocked while e1 holds the Lease.
	select {
	case <-e2Acquired:
		t.Fatal("second elector acquired while first still held the Lease")
	case <-time.After(500 * time.Millisecond):
	}
	assert.True(t, e1.IsLeader(), "first elector must still report leadership while holding the Lease")
	assert.False(t, e2.IsLeader(), "second elector must not report leadership while waiting")

	cancel1()

	select {
	case <-e2Acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("second elector never acquired after first relinquished")
	}
	assert.True(t, e2.IsLeader(), "second elector must report leadership after acquiring")
	assert.Eventually(t, func() bool { return !e1.IsLeader() }, 5*time.Second, 20*time.Millisecond,
		"cancelled first elector must transition IsLeader to false")
}

// TestElector_LeaseObjectWrittenWithIdentity confirms the adapter
// actually persists the Lease object via the API — not just inside
// client-go's in-memory state — and stamps it with the configured
// identity. This is the load-bearing assertion the doc comment
// promises ("kubectl get lease ... shows who currently leads").
func TestElector_LeaseObjectWrittenWithIdentity(t *testing.T) {
	client := fake.NewClientset()
	ns := "kit-system"
	name := uniqueName(t, "ident")

	e := k8slease.New(client, ns, name, "pod-identity",
		k8slease.WithLeaseDuration(2*time.Second),
		k8slease.WithRenewDeadline(time.Second),
		k8slease.WithRetryPeriod(100*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acquired := make(chan struct{}, 1)
	go func() {
		_ = e.Run(ctx, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) {
				select {
				case acquired <- struct{}{}:
				default:
				}
				<-ctx.Done()
			},
		})
	}()

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("OnAcquired was never invoked within 5s")
	}

	// At this point the Lease object MUST exist in the fake apiserver
	// with our identity in HolderIdentity. Use a small Eventually so
	// the assertion is robust to the brief window between acquire and
	// the holder field being persisted on subsequent renewals.
	require.Eventually(t, func() bool {
		lease, err := client.CoordinationV1().Leases(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil || lease == nil {
			return false
		}
		holder := lease.Spec.HolderIdentity
		return holder != nil && *holder == "pod-identity"
	}, 3*time.Second, 50*time.Millisecond,
		"the Lease object must be persisted with the elector's identity in HolderIdentity")
}
