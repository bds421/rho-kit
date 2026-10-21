package app

import (
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerConfig_DefaultListensOnIPv6AndIPv4(t *testing.T) {
	t.Setenv("SERVER_HOST", "")
	cfg, err := LoadBaseConfig(0)
	require.NoError(t, err)
	assert.Equal(t, ":0", cfg.Server.Addr())

	ln, err := net.Listen("tcp", cfg.Server.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	hosts := []string{"127.0.0.1"}
	if probe, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		_ = probe.Close()
		hosts = append(hosts, "::1")
	}
	for _, host := range hosts {
		c, err := net.Dial("tcp", net.JoinHostPort(host, port))
		require.NoError(t, err, "dial %s", host)
		_ = c.Close()
	}
}

func TestConfigAddr_BracketsIPv6(t *testing.T) {
	for host, want := range map[string]string{
		"":          ":8080",
		"::":        "[::]:8080",
		"[::]":      "[::]:8080",
		"::1":       "[::1]:8080",
		"[::1]":     "[::1]:8080",
		"127.0.0.1": "127.0.0.1:8080",
		"localhost": "localhost:8080",
	} {
		assert.Equal(t, want, ServerConfig{Host: host, Port: 8080}.Addr(), "server host %q", host)
	}
	assert.Equal(t, "[::1]:9090", InternalConfig{Host: "::1", Port: 9090}.Addr())
	assert.Equal(t, "127.0.0.1:9090", InternalConfig{Port: 9090}.Addr())
}
