package centrifuge

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	cfg "github.com/centrifugal/centrifuge"
	"github.com/centrifugal/protocol"
)

type publicationStateKey struct{}
type publicationState struct{ refused atomic.Bool }

func (n *Node) writeContext(ctx context.Context) context.Context {
	if n.publicationWritePolicy == nil {
		return ctx
	}
	return context.WithValue(ctx, publicationStateKey{}, new(publicationState))
}

func (n *Node) installPublicationWritePolicy() {
	n.node.OnTransportWrite(func(client *cfg.Client, event cfg.TransportWriteEvent) bool {
		if event.FrameType != protocol.FrameTypePushPublication {
			return true
		}
		state, ok := client.Context().Value(publicationStateKey{}).(*publicationState)
		if !ok {
			return false
		} // Mandatory connecting callback must establish state.
		if state.refused.Load() {
			return false
		}
		refuse := func() bool {
			if state.refused.CompareAndSwap(false, true) {
				// Never recursively close from the writer/close-flush callback.
				// Slow uses upstream's no-flush path. The latch also protects any
				// concurrent PermissionDenied/Shutdown flush already in progress.
				go client.Disconnect(cfg.DisconnectSlow)
			}
			return false
		}
		// Native close releases the channel mutex after unsubscription and before
		// acquiring the writer mutex. IsSubscribed is safe in this hook, including
		// close flushing, and avoids authority I/O for already removed channels.
		if event.Channel == "" || n.stopped.Load() || !client.IsSubscribed(event.Channel) || client.Context().Err() != nil {
			return refuse()
		}
		transport := client.Transport()
		publication := PublicationWriteEvent{
			Channel: event.Channel, Data: event.Data, FrameType: event.FrameType,
			Protocol: transport.Protocol(), Unidirectional: transport.Unidirectional(),
		}
		if !n.publicationWritePolicy(Connection{client: client}, publication) || n.stopped.Load() || !client.IsSubscribed(event.Channel) || client.Context().Err() != nil {
			return refuse()
		}
		return !state.refused.Load()
	})
}

func (n *Node) observeSubscriptionReady(client *cfg.Client, channel string, ready <-chan struct{}) {
	// Upstream closes SubscriptionReady on failures too. Verify installed state
	// after its synchronous callback; do not allocate an observer goroutine or
	// infer success merely from the closed channel.
	select {
	case <-ready:
	default:
		return
	}
	if client.Context().Err() != nil || !slices.Contains(client.Channels(), channel) {
		return
	}
	n.subscriptionReadyObserver(Connection{client: client}, channel)
}

// Connection is a concurrency-safe view of an accepted client. Applications may
// register, send to and disconnect it without replacing mandatory kit callbacks.
// Send queues a framework push; success is not proof of browser receipt.
type Connection struct{ client *cfg.Client }

func (c Connection) ID() string               { return c.client.ID() }
func (c Connection) UserID() string           { return c.client.UserID() }
func (c Connection) Context() context.Context { return c.client.Context() }
func (c Connection) Send(data []byte) error {
	if err := c.client.Send(data); err != nil {
		return redact.WrapError("realtime/centrifuge: client send", err)
	}
	return nil
}
func (c Connection) Disconnect(advice cfg.Disconnect) { c.client.Disconnect(advice) }

func policyExpiry(jwtExpiry, policyExpiry int64, now time.Time) (int64, error) {
	if jwtExpiry < 0 || policyExpiry < 0 || (jwtExpiry > 0 && policyExpiry > jwtExpiry) {
		return 0, errors.New("realtime/centrifuge: policy cannot extend verified token expiry")
	}
	expiry := jwtExpiry
	if policyExpiry > 0 {
		expiry = policyExpiry
	}
	if expiry > 0 && expiry <= now.Unix() {
		return 0, errors.New("realtime/centrifuge: verified connection expired")
	}
	return expiry, nil
}

func boundedPolicyExpiry(ctx context.Context, jwtExpiry int64, reply ConnectionPolicyReply, now time.Time) (int64, error) {
	expiry, err := policyExpiry(jwtExpiry, reply.ExpireAt, now)
	if err != nil {
		return 0, err
	}
	for _, candidate := range []context.Context{ctx, reply.Context} {
		if candidate == nil {
			continue
		}
		if err := candidate.Err(); err != nil {
			return 0, err
		}
		if deadline, ok := candidate.Deadline(); ok && (expiry == 0 || deadline.Unix() < expiry) {
			expiry = deadline.Unix()
		}
	}
	return policyExpiry(jwtExpiry, expiry, now)
}

func (n *Node) refresh(client *cfg.Client, event cfg.RefreshEvent, cb cfg.RefreshCallback) {
	if !event.ClientSideRefresh || n.verifier == nil || n.refreshPolicy == nil {
		cb(cfg.RefreshReply{}, cfg.DisconnectInvalidToken)
		return
	}
	claims, err := n.verifier.VerifyContext(client.Context(), extractBearer(event.Token), time.Now())
	if err != nil {
		advice, _ := connectVerifyFailure(err)
		cb(cfg.RefreshReply{}, advice)
		return
	}
	if claims.Subject == "" || claims.Subject != client.UserID() {
		cb(cfg.RefreshReply{}, cfg.DisconnectInvalidToken)
		return
	}
	copy := *claims
	copy.Permissions = slices.Clone(claims.Permissions)
	policy, err := n.refreshPolicy(client.Context(), Connection{client: client}, event, copy)
	if err != nil {
		cb(cfg.RefreshReply{}, cfg.DisconnectPermissionDenied)
		return
	}
	expiry, err := boundedPolicyExpiry(client.Context(), claims.ExpiresAt, policy, time.Now())
	if err != nil {
		cb(cfg.RefreshReply{Expired: true}, nil)
		return
	}
	cb(cfg.RefreshReply{ExpireAt: expiry, Info: slices.Clone(policy.Info)}, nil)
}
