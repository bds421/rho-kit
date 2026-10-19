package centrifuge

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	cfg "github.com/centrifugal/centrifuge"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// Backend is a broker and/or presence manager installed on the native node
// before it runs. Replicas that share a backend deliver publications, history
// and presence across every node; without one the node keeps the native
// in-memory broker and presence manager (single replica only).
type Backend struct {
	// Broker replaces the native in-memory broker. Nil keeps it.
	Broker cfg.Broker
	// PresenceManager replaces the native in-memory presence manager. Nil
	// keeps it.
	PresenceManager cfg.PresenceManager
	// Close releases resources the factory opened (for example Redis
	// connections). It runs once, after the node shut down or after a failed
	// native Run. Optional.
	Close func()
}

// BackendFactory builds the [Backend] for a native node. It runs inside
// [Node.Start] before the native Run, so connections are opened by the
// lifecycle rather than at construction. A failed Start may call it again.
type BackendFactory func(node *cfg.Node) (Backend, error)

// WithBackend installs a caller-supplied broker/presence manager, typically
// to run several replicas behind a shared Redis (see [RedisBackend]). The
// factory must return at least one of Broker or PresenceManager.
func WithBackend(fn BackendFactory) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithBackend requires a non-nil factory")
	}
	return func(c *config) { c.backend = fn }
}

// RedisBackendConfig configures [RedisBackend].
type RedisBackendConfig struct {
	// Shards are the Redis connections. At least one is required; channels
	// are consistently sharded across them.
	Shards []cfg.RedisShardConfig
	// Prefix namespaces every Redis key and channel. Empty uses the native
	// default ("centrifuge"). Set a distinct prefix per application when
	// several share one Redis.
	Prefix string
	// PresenceTTL bounds how long presence entries stay valid without an
	// update. Zero uses the native default (60s).
	PresenceTTL time.Duration
	// PlaintextUnsafe allows non-loopback shards without TLS or a password.
	// Leave false outside reviewed private networks.
	PlaintextUnsafe bool
}

// RedisBackend returns a [BackendFactory] that installs the native Redis
// broker and presence manager. Shard connections are opened in [Node.Start]
// and closed after [Node.Stop].
//
// Transport safety mirrors the kit's Redis module: a shard that targets a
// non-loopback host must set TLSConfig (or use a rediss:// address) and a
// password, unless PlaintextUnsafe is set. Hosts are classified without DNS.
// Panics on an invalid configuration.
func RedisBackend(c RedisBackendConfig) BackendFactory {
	if len(c.Shards) == 0 {
		panic("realtime/centrifuge: RedisBackend requires at least one shard")
	}
	if c.PresenceTTL < 0 {
		panic("realtime/centrifuge: RedisBackend requires PresenceTTL >= 0")
	}
	if !c.PlaintextUnsafe {
		for i, shard := range c.Shards {
			if err := checkRedisShardTransport(shard); err != nil {
				panic(fmt.Sprintf("realtime/centrifuge: RedisBackend shard %d: %v", i, err))
			}
		}
	}
	shardConfigs := append([]cfg.RedisShardConfig(nil), c.Shards...)
	return func(node *cfg.Node) (Backend, error) {
		shards := make([]*cfg.RedisShard, 0, len(shardConfigs))
		closeShards := func() {
			for _, s := range shards {
				s.Close()
			}
		}
		for _, sc := range shardConfigs {
			shard, err := cfg.NewRedisShard(node, sc)
			if err != nil {
				closeShards()
				return Backend{}, redact.WrapError("redis shard", err)
			}
			shards = append(shards, shard)
		}
		broker, err := cfg.NewRedisBroker(node, cfg.RedisBrokerConfig{Prefix: c.Prefix, Shards: shards})
		if err != nil {
			closeShards()
			return Backend{}, redact.WrapError("redis broker", err)
		}
		presence, err := cfg.NewRedisPresenceManager(node, cfg.RedisPresenceManagerConfig{
			Prefix:      c.Prefix,
			Shards:      shards,
			PresenceTTL: c.PresenceTTL,
		})
		if err != nil {
			closeShards()
			return Backend{}, redact.WrapError("redis presence manager", err)
		}
		return Backend{Broker: broker, PresenceManager: presence, Close: closeShards}, nil
	}
}

// checkRedisShardTransport rejects non-loopback shards without TLS or a
// password.
func checkRedisShardTransport(s cfg.RedisShardConfig) error {
	tlsOn := s.TLSConfig != nil
	password := s.Password
	var hosts []string
	switch {
	case len(s.ClusterAddresses) > 0:
		hosts = s.ClusterAddresses
	case len(s.SentinelAddresses) > 0:
		hosts = s.SentinelAddresses
		if s.SentinelTLSConfig == nil && !allLoopback(hosts) {
			return errors.New("non-loopback sentinel requires SentinelTLSConfig")
		}
	default:
		host, urlTLS, urlPassword, err := parseRedisAddress(s.Address)
		if err != nil {
			return err
		}
		if host == "" {
			// unix socket: local by construction.
			return nil
		}
		hosts = []string{host}
		tlsOn = tlsOn || urlTLS
		if password == "" {
			password = urlPassword
		}
	}
	if allLoopback(hosts) {
		return nil
	}
	if !tlsOn {
		return errors.New("non-loopback address requires TLSConfig or a rediss:// address (or PlaintextUnsafe)")
	}
	if password == "" {
		return errors.New("non-loopback address requires a password (or PlaintextUnsafe)")
	}
	return nil
}

// parseRedisAddress extracts the host, whether the scheme implies TLS, and a
// URL password from a native shard address. host is empty for unix sockets.
func parseRedisAddress(addr string) (host string, tlsOn bool, password string, err error) {
	if !strings.Contains(addr, "://") {
		return addr, false, "", nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", false, "", errors.New("invalid address URL")
	}
	if pw, ok := u.User.Password(); ok {
		password = pw
	}
	switch u.Scheme {
	case "unix":
		return "", false, password, nil
	case "rediss":
		tlsOn = true
	}
	return u.Host, tlsOn, password, nil
}

func allLoopback(addrs []string) bool {
	for _, addr := range addrs {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		if !netutil.IsLoopbackHostLiteral(host) {
			return false
		}
	}
	return true
}
