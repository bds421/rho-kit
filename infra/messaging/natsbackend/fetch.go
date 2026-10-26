package natsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// MaxPullMessages bounds [PullRequest.MaxMessages]: the messages of one pull
// are buffered before they are returned.
const MaxPullMessages = 10_000

// PullRequest bounds one pull from a durable pull consumer. Unlike
// jetstream's Fetch and FetchBytes, it caps the message count and the bytes
// together, and buffers only MaxMessages.
type PullRequest struct {
	// MaxMessages is the most messages the pull returns, 1 to MaxPullMessages.
	MaxMessages int
	// MaxBytes is the most bytes (headers and data, as the server counts them)
	// the pull returns; 0 is no byte limit.
	MaxBytes int
	// Expires is how long the server keeps the pull open while fewer
	// messages are available. Required.
	Expires time.Duration
	// Heartbeat asks the server for an idle heartbeat at this interval; two
	// missed heartbeats end the pull with jetstream.ErrNoHeartbeat. 0 asks
	// for none. At most half of Expires.
	Heartbeat time.Duration
}

// Pull errors.
var (
	ErrPullRequest     = errors.New("natsbackend: invalid pull request")
	ErrMessageTooLarge = errors.New("natsbackend: the next message is larger than the pull's byte limit")
)

// Validate reports whether the pull's bounds are usable.
func (r PullRequest) Validate() error {
	switch {
	case r.MaxMessages < 1 || r.MaxMessages > MaxPullMessages:
		return fmt.Errorf("%w: MaxMessages must be 1 to %d", ErrPullRequest, MaxPullMessages)
	case r.MaxBytes < 0:
		return fmt.Errorf("%w: MaxBytes must not be negative", ErrPullRequest)
	case r.Expires <= 0:
		return fmt.Errorf("%w: Expires must be positive", ErrPullRequest)
	case r.Heartbeat < 0 || r.Heartbeat > r.Expires/2:
		return fmt.Errorf("%w: Heartbeat must be 0 to half of Expires", ErrPullRequest)
	}
	return nil
}

type pullBody struct {
	Batch     int           `json:"batch"`
	MaxBytes  int           `json:"max_bytes,omitempty"`
	Expires   time.Duration `json:"expires"`
	Heartbeat time.Duration `json:"idle_heartbeat,omitempty"`
}

// Fetch sends one pull request to the durable pull consumer of stream and
// returns what the server delivered before the pull ended: MaxMessages
// messages, MaxBytes bytes, Expires passed or the server had none left.
// The messages are acknowledged as usual (Ack, Nak, NakWithDelay, Term,
// InProgress) and carry their Metadata.
//
// A consumer or stream that does not exist is nats.ErrNoResponders; a
// consumer deleted during the pull is jetstream.ErrConsumerDeleted. When
// the first message alone exceeds MaxBytes, Fetch returns
// ErrMessageTooLarge, so a too small limit never looks like an empty
// consumer. A cancelled ctx ends the pull with the messages so far and
// ctx's error.
func Fetch(ctx context.Context, js jetstream.JetStream, stream, consumer string, req PullRequest) ([]*nats.Msg, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if !nameToken(stream) || !nameToken(consumer) {
		return nil, fmt.Errorf("%w: stream and consumer must be single subject tokens", ErrPullRequest)
	}
	body, err := json.Marshal(pullBody{Batch: req.MaxMessages, MaxBytes: req.MaxBytes, Expires: req.Expires, Heartbeat: req.Heartbeat})
	if err != nil {
		return nil, err
	}
	nc := js.Conn()
	inbox := nc.NewInbox()
	delivered := make(chan *nats.Msg, req.MaxMessages+8)
	sub, err := nc.ChanSubscribe(inbox, delivered)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := nc.PublishRequest(apiPrefix(js.Options())+"CONSUMER.MSG.NEXT."+stream+"."+consumer, inbox, body); err != nil {
		return nil, err
	}
	return collect(ctx, delivered, req)
}

func collect(ctx context.Context, delivered <-chan *nats.Msg, req PullRequest) ([]*nats.Msg, error) {
	expired := time.NewTimer(req.Expires + time.Second)
	defer expired.Stop()
	var heartbeat <-chan time.Time
	var silence *time.Timer
	if req.Heartbeat > 0 {
		silence = time.NewTimer(2 * req.Heartbeat)
		defer silence.Stop()
		heartbeat = silence.C
	}
	msgs := make([]*nats.Msg, 0, req.MaxMessages)
	size := 0
	for {
		select {
		case <-ctx.Done():
			return msgs, ctx.Err()
		case <-expired.C:
			return msgs, nil
		case <-heartbeat:
			return msgs, jetstream.ErrNoHeartbeat
		case m := <-delivered:
			if silence != nil {
				silence.Reset(2 * req.Heartbeat)
			}
			// Server statuses carry no reply subject; a stream message always has
			// its ack subject, even one a publisher dressed up as a status.
			if status := m.Header.Get("Status"); m.Reply == "" && status != "" && len(m.Data) == 0 {
				done, err := statusEnds(status, m.Header.Get("Description"), len(msgs))
				if done || err != nil {
					return msgs, err
				}
				continue
			}
			msgs = append(msgs, m)
			size += m.Size()
			if len(msgs) == req.MaxMessages || req.MaxBytes > 0 && size >= req.MaxBytes {
				return msgs, nil
			}
		}
	}
}

// statusEnds reads a status the server sends instead of a message: whether
// it ends the pull, and with which error.
func statusEnds(status, description string, received int) (bool, error) {
	switch status {
	case "100":
		return false, nil
	case "404", "408":
		return true, nil
	case "503":
		return true, nats.ErrNoResponders
	case "409":
		switch {
		case strings.Contains(description, "Message Size Exceeds MaxBytes"):
			if received == 0 {
				return true, ErrMessageTooLarge
			}
			return true, nil
		case strings.Contains(description, "Consumer Deleted"):
			return true, jetstream.ErrConsumerDeleted
		case strings.Contains(description, "Batch Completed"):
			return true, nil
		}
	}
	return true, fmt.Errorf("natsbackend: the pull ended with status %s %q", status, description)
}

func apiPrefix(o jetstream.JetStreamOptions) string {
	prefix := o.APIPrefix
	switch {
	case o.Domain != "":
		prefix = "$JS." + o.Domain + ".API."
	case prefix == "":
		prefix = jetstream.DefaultAPIPrefix
	}
	if !strings.HasSuffix(prefix, ".") {
		prefix += "."
	}
	return prefix
}

// nameToken: a stream or consumer name is one subject token.
func nameToken(name string) bool {
	return name != "" && !strings.Contains(name, ".") && ValidateSubject(name) == nil
}
