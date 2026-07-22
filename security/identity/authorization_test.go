package identity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingDecider struct {
	subject  string
	action   string
	resource string
}

func (d *recordingDecider) Allow(_ context.Context, subject, action, resource string) error {
	d.subject, d.action, d.resource = subject, action, resource
	return nil
}

func TestAllowUsesCanonicalPrincipalSubject(t *testing.T) {
	decider := &recordingDecider{}
	ctx := WithPrincipal(context.Background(), Principal{Subject: "auth0|user-1", Actor: "user-1"})

	require.NoError(t, Allow(ctx, decider, "read", "document:42"))
	assert.Equal(t, "auth0|user-1", decider.subject)
	assert.Equal(t, "read", decider.action)
	assert.Equal(t, "document:42", decider.resource)
}

func TestAllowRequiresPrincipal(t *testing.T) {
	err := Allow(context.Background(), &recordingDecider{}, "read", "document:42")
	assert.ErrorIs(t, err, ErrPrincipalRequired)
}

func TestAuditActor(t *testing.T) {
	assert.Equal(t, "anonymous", AuditActor(context.Background()))
	ctx := WithPrincipal(context.Background(), Principal{Subject: "subject-1", Actor: "service-a", Kind: ActorService})
	assert.Equal(t, "service-a", AuditActor(ctx))
}
