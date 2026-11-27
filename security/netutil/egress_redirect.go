package netutil

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// egressTransport checks every request, the first and each redirect,
// before it is sent, and sends it through the pool of its verdict.
type egressTransport struct {
	c              *egressConfig
	public, listed http.RoundTripper
}

func (t egressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	listed, err := t.c.target(req)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close() // RoundTrip must close it, also on an error
		}
		return nil, err
	}
	if t.c.userAgent != "" && req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.c.userAgent)
	}
	next := t.public
	if listed {
		next = t.listed
	}
	resp, err := next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if t.c.maxResponse > 0 {
		resp.Body = &limitedBody{ReadCloser: resp.Body, left: t.c.maxResponse}
	}
	return resp, nil
}

// target checks a URL before it is called: https, or http to a listed
// host. It reports whether the host is listed.
func (c *egressConfig) target(req *http.Request) (bool, error) {
	listed, err := c.listed(req, req.URL)
	if err != nil {
		return false, err
	}
	if req.URL.Scheme != "https" && (req.URL.Scheme != "http" || !listed) {
		return false, fmt.Errorf("%w: scheme %q; https only, http to a listed host", ErrEgressRefused, req.URL.Scheme)
	}
	return listed, nil
}

func (c *egressConfig) listed(req *http.Request, u *url.URL) (bool, error) {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return c.listedAt(req.Context(), u.Hostname(), port)
}

// checkRedirect follows at most maxRedirects hops; each is a new request
// through the transport and a new connection through the guard. A hop
// keeps https once it had it, and a host not listed cannot send a call on
// to a listed one: only the caller's own URL may lead inside.
func (c *egressConfig) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > c.maxRedirects {
		return fmt.Errorf("%w: more than %d redirects", ErrEgressRefused, c.maxRedirects)
	}
	from := via[len(via)-1]
	if from.URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("%w: redirect from https to %s", ErrEgressRefused, req.URL.Scheme)
	}
	listed, err := c.target(req)
	if err != nil {
		return err
	}
	if listed {
		origin, err := c.listed(req, from.URL)
		if err != nil {
			return err
		}
		if !origin {
			return fmt.Errorf("%w: redirect into a listed host", ErrEgressRefused)
		}
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		c.strip(req.Header)
	}
	return nil
}

func (c *egressConfig) strip(h http.Header) {
	for name := range h {
		for _, prefix := range c.stripHeaders {
			if len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
				h.Del(name)
			}
		}
	}
}
