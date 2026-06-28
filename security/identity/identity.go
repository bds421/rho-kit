package identity

// ActorKind classifies who performed the request for audit, rate limits, and
// policy branches. HTTP and gRPC auth middleware stamp the same string values.
type ActorKind string

const (
	// ActorUser is a human or user-shaped session/JWT subject.
	ActorUser ActorKind = "user"
	// ActorAPIKey is a scoped or header API key credential.
	ActorAPIKey ActorKind = "api_key"
	// ActorOAuthClient is an OAuth client-credentials access token.
	ActorOAuthClient ActorKind = "oauth_client"
	// ActorService is a trusted service identity (JWT S2S, mTLS, internal).
	ActorService ActorKind = "service"
	// ActorAnonymous is the zero value; callers treat it as [ActorUser] when
	// Subject is set without an explicit kind.
	ActorAnonymous ActorKind = ""
)

// Ref is the minimal subject/actor/kind triple for audit formatting and
// machine-vs-human policy branches. Transport packages embed or map from
// their richer identity structs.
type Ref struct {
	Subject string
	Actor   string
	Kind    ActorKind
}

// IsMachineKind reports whether kind represents a non-human actor.
func IsMachineKind(kind ActorKind) bool {
	switch kind {
	case ActorAPIKey, ActorOAuthClient, ActorService:
		return true
	default:
		return false
	}
}

// Format returns the conventional actionlog/audit actor string for ref.
// Convention: "<actor_kind>:<actor_id>" (user actors use the UUID subject).
func Format(ref Ref) string {
	switch ref.Kind {
	case ActorUser, ActorAnonymous:
		if ref.Subject != "" {
			return "user:" + ref.Subject
		}
		return "user:" + ref.Actor
	case ActorAPIKey, ActorOAuthClient, ActorService:
		if ref.Actor == "" {
			return string(ref.Kind) + ":"
		}
		return string(ref.Kind) + ":" + ref.Actor
	default:
		if ref.Actor != "" {
			return string(ref.Kind) + ":" + ref.Actor
		}
		return "user:" + ref.Subject
	}
}