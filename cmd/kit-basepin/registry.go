package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const manifestTypes = "application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json"

// registry reads tags, manifests and push times anonymously over the
// Distribution API and, for Docker Hub, its tag API.
type registry struct {
	client *http.Client
	// hosts maps a registry host to its endpoint (Docker Hub's is not its
	// name); any other is https://<host>. hub is Docker Hub's API.
	hosts  map[string]string
	hub    string
	tokens map[string]string
}

func newRegistry() *registry {
	return &registry{client: &http.Client{Timeout: 30 * time.Second}, hosts: map[string]string{"docker.io": "https://registry-1.docker.io"}, hub: "https://hub.docker.com", tokens: map[string]string{}}
}

func (r *registry) endpoint(host string) string {
	if e, ok := r.hosts[host]; ok {
		return e
	}
	return "https://" + host
}

// get fetches a registry path, answering a Bearer challenge with an
// anonymous pull token.
func (r *registry) get(method string, p pin, path, accept string) (*http.Response, error) {
	u := r.endpoint(p.Host) + "/v2/" + p.Repository + path
	for try := 0; try < 2; try++ {
		req, err := http.NewRequest(method, u, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if t := r.tokens[p.Host+"/"+p.Repository]; t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || try > 0 {
			return resp, nil
		}
		challenge := resp.Header.Get("WWW-Authenticate")
		_ = resp.Body.Close()
		if err := r.token(p, challenge); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("unreachable")
}

func (r *registry) token(p pin, challenge string) error {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return fmt.Errorf("%s: unsupported challenge %q", p.Host, challenge)
	}
	values := map[string]string{}
	for _, part := range strings.Split(params, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			values[k] = strings.Trim(v, `"`)
		}
	}
	q := url.Values{"service": {values["service"]}, "scope": {"repository:" + p.Repository + ":pull"}}
	resp, err := r.client.Get(values["realm"] + "?" + q.Encode())
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return fmt.Errorf("%s: no pull token (%s)", p.Host, resp.Status)
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	r.tokens[p.Host+"/"+p.Repository] = body.Token
	return nil
}

// current is the digest the tag names now.
func (r *registry) current(p pin) (string, error) {
	resp, err := r.get(http.MethodHead, p, "/manifests/"+p.Tag, manifestTypes)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	d := resp.Header.Get("Docker-Content-Digest")
	if resp.StatusCode != http.StatusOK || d == "" {
		return "", fmt.Errorf("%s: tag not resolved (%s)", p.name(), resp.Status)
	}
	return d, nil
}

// pushed is when digest was pushed, from Docker Hub's tag API, a
// tags/list that reports upload times (Google's registries), or the
// image's creation time; zero when none of them tells (a reproducible
// build dates itself 1970).
func (r *registry) pushed(p pin, digest string) time.Time {
	if p.Host == "docker.io" {
		if at := r.hubPushed(p, digest); !at.IsZero() {
			return at
		}
	}
	if at := r.listedUpload(p, digest); !at.IsZero() {
		return at
	}
	if at := r.created(p, digest); at.Year() >= 2000 {
		return at
	}
	return time.Time{}
}

func (r *registry) hubPushed(p pin, digest string) time.Time {
	ns, name, _ := strings.Cut(p.Repository, "/")
	resp, err := r.client.Get(r.hub + "/v2/namespaces/" + ns + "/repositories/" + name + "/tags/" + p.Tag)
	if err != nil {
		return time.Time{}
	}
	defer func() { _ = resp.Body.Close() }()
	var tag struct {
		Digest string    `json:"digest"`
		Pushed time.Time `json:"tag_last_pushed"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&tag) != nil || tag.Digest != digest {
		return time.Time{}
	}
	return tag.Pushed
}

func (r *registry) listedUpload(p pin, digest string) time.Time {
	resp, err := r.get(http.MethodGet, p, "/tags/list", "")
	if err != nil {
		return time.Time{}
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Manifest map[string]struct {
			Uploaded string `json:"timeUploadedMs"`
		} `json:"manifest"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&list) != nil {
		return time.Time{}
	}
	var ms int64
	if _, err := fmt.Sscan(list.Manifest[digest].Uploaded, &ms); err != nil || ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// created is the creation time in the image config, of the linux/amd64
// image of an index.
func (r *registry) created(p pin, digest string) time.Time {
	var m struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS, Architecture string
			} `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if r.decode(p, "/manifests/"+digest, manifestTypes, &m) != nil {
		return time.Time{}
	}
	if m.Config.Digest == "" {
		for _, sub := range m.Manifests {
			if sub.Platform.OS == "linux" && sub.Platform.Architecture == "amd64" {
				return r.created(p, sub.Digest)
			}
		}
		return time.Time{}
	}
	var config struct {
		Created time.Time `json:"created"`
	}
	if r.decode(p, "/blobs/"+m.Config.Digest, "", &config) != nil {
		return time.Time{}
	}
	return config.Created
}

func (r *registry) decode(p pin, path, accept string, v any) error {
	resp, err := r.get(http.MethodGet, p, path, accept)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s%s: %s", p.name(), path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}
