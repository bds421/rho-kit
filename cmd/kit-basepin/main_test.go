package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

const (
	oldDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	amd64     = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	config    = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
)

// image is what the fake registry holds for one repository's tag.
type image struct {
	digest   string
	uploaded time.Time // tags/list timeUploadedMs, zero for none
	hub      time.Time // Docker Hub's tag_last_pushed, zero for none
	created  time.Time // the config's creation time
	realm    string    // the token realm, the fake's own when empty
}

// fakeRegistry serves the Distribution API behind an anonymous Bearer
// challenge, Docker Hub's tag API and a token endpoint, for one tag.
func fakeRegistry(t *testing.T, img image) *registry {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Query().Get("scope"), ":pull") {
			http.Error(w, "scope", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "t0k", "access_token": "t0k"})
	})
	mux.HandleFunc("/hub/", func(w http.ResponseWriter, _ *http.Request) {
		if img.hub.IsZero() {
			http.NotFound(w, nil)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"digest": img.digest, "tag_last_pushed": img.hub})
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0k" {
			realm := img.realm
			if realm == "" {
				realm = srv.URL + "/token"
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="fake"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v2/team/base")
		switch path {
		case "/manifests/1.0":
			w.Header().Set("Docker-Content-Digest", img.digest)
		case "/tags/list":
			list := map[string]any{"tags": []string{"1.0"}}
			if !img.uploaded.IsZero() {
				list["manifest"] = map[string]any{img.digest: map[string]string{"timeUploadedMs": strconv.FormatInt(img.uploaded.UnixMilli(), 10)}}
			}
			_ = json.NewEncoder(w).Encode(list)
		case "/manifests/" + img.digest:
			_ = json.NewEncoder(w).Encode(map[string]any{"manifests": []any{map[string]any{"digest": amd64, "platform": map[string]string{"os": "linux", "architecture": "amd64"}}}})
		case "/manifests/" + amd64:
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]string{"digest": config}})
		case "/blobs/" + config:
			_ = json.NewEncoder(w).Encode(map[string]any{"created": img.created})
		default:
			http.NotFound(w, r)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	reg := newRegistry()
	reg.hosts = map[string]string{"docker.io": srv.URL, "registry.example": srv.URL}
	reg.hub = srv.URL + "/hub"
	return reg
}

func dockerfile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func basepin(t *testing.T, reg *registry, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, &out, &out, reg, now)
	return code, out.String()
}

// A pin behind its tag fails once the tag's digest is past the wait, by
// whichever push time the registry tells; within the wait it passes; with
// no push time it fails.
func TestAPinBehindItsTagFailsAfterTheWait(t *testing.T) {
	day := 24 * time.Hour
	for name, c := range map[string]struct {
		host  string
		img   image
		pass  bool
		state string
	}{
		"current":                {"registry.example", image{digest: oldDigest}, true, "current"},
		"uploaded 10 days ago":   {"registry.example", image{digest: newDigest, uploaded: now.Add(-10 * day)}, false, "BEHIND: the tag is sha256:222222222222 since 2026-10-28"},
		"uploaded 2 days ago":    {"registry.example", image{digest: newDigest, uploaded: now.Add(-2 * day)}, true, "newer digest in its wait: the tag is sha256:222222222222 since 2026-11-05, due 2026-11-12"},
		"Docker Hub, 8 days ago": {"docker.io", image{digest: newDigest, hub: now.Add(-8 * day)}, false, "since 2026-10-30"},
		"Docker Hub, 1 day ago":  {"docker.io", image{digest: newDigest, hub: now.Add(-day)}, true, "in its wait"},
		"built 9 days ago":       {"registry.example", image{digest: newDigest, created: now.Add(-9 * day)}, false, "built 2026-10-29 (a build date: the push time is unknown)"},
		"built 1 day ago":        {"registry.example", image{digest: newDigest, created: now.Add(-24 * time.Hour)}, false, "built 2026-11-06 (a build date"},
		"no push time (1970)":    {"registry.example", image{digest: newDigest, created: time.Unix(0, 0)}, false, "pushed at an unknown time"},
	} {
		ref := c.host + "/team/base:1.0@" + oldDigest
		if c.host == "docker.io" {
			ref = "team/base:1.0@" + oldDigest
		}
		code, out := basepin(t, fakeRegistry(t, c.img), dockerfile(t, "FROM --platform=$BUILDPLATFORM "+ref+" AS build\nFROM build AS runtime\n"))
		if (code == 0) != c.pass || code == 2 || !strings.Contains(out, c.state) {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// An advisory on the pinned digest overrides the wait; one on a digest no
// Dockerfile pins, or on the digest the tag still has, is bad input.
func TestAnAdvisoryOverridesTheWait(t *testing.T) {
	file := dockerfile(t, "ARG BASE=registry.example/team/base:1.0@"+oldDigest+"\nFROM ${BASE}\n")
	advise := func(digest string) string {
		return dockerfile(t, `[{"image":"registry.example/team/base","digest":"`+digest+`","advisory":"DSA-1-1","reason":"ca-certificates"}]`)
	}
	fresh := image{digest: newDigest, uploaded: now.Add(-time.Hour)}
	if code, out := basepin(t, fakeRegistry(t, fresh), "-advisories", advise(oldDigest), file); code != 1 || !strings.Contains(out, "DSA-1-1 (ca-certificates) overrides the wait") {
		t.Errorf("advisory on the pin: exit %d\n%s", code, out)
	}
	if code, out := basepin(t, fakeRegistry(t, fresh), "-advisories", advise(newDigest), file); code != 2 || !strings.Contains(out, "remove the entry") {
		t.Errorf("advisory on another digest: exit %d\n%s", code, out)
	}
	if code, out := basepin(t, fakeRegistry(t, image{digest: oldDigest}), "-advisories", advise(oldDigest), file); code != 2 || !strings.Contains(out, "not in a newer image yet") {
		t.Errorf("advisory, tag unchanged: exit %d\n%s", code, out)
	}
	if code, _ := basepin(t, fakeRegistry(t, fresh), "-advisories", dockerfile(t, `[{"image":"x"}]`), file); code != 2 {
		t.Errorf("an incomplete advisory: exit %d", code)
	}
}

// A FROM without a digest, a missing file or a tag the registry does not
// know is bad input; stages, scratch and ${} references are not pins.
func TestBadInputIsNoPass(t *testing.T) {
	reg := fakeRegistry(t, image{digest: oldDigest})
	if code, out := basepin(t, reg, dockerfile(t, "FROM alpine:3.22\n")); code != 2 || !strings.Contains(out, "is not pinned") {
		t.Errorf("unpinned: exit %d\n%s", code, out)
	}
	if code, _ := basepin(t, reg, filepath.Join(t.TempDir(), "missing")); code != 2 {
		t.Errorf("missing file: exit %d", code)
	}
	if code, out := basepin(t, reg, dockerfile(t, "FROM registry.example/team/base:2.0@"+oldDigest+"\n")); code != 2 || !strings.Contains(out, "tag not resolved") {
		t.Errorf("unknown tag: exit %d\n%s", code, out)
	}
	if code, out := basepin(t, reg, dockerfile(t, "FROM scratch\n")); code != 0 {
		t.Errorf("no pins: exit %d\n%s", code, out)
	}
	if code, _ := basepin(t, reg); code != 2 {
		t.Errorf("no Dockerfile: exit %d", code)
	}
}

// FROM ${X} is checked by ARG X's default, quoted or not; an ARG with no
// default, or one that is not pinned, is refused.
func TestFromAnArgUsesItsPinnedDefault(t *testing.T) {
	reg := fakeRegistry(t, image{digest: oldDigest})
	ref := "registry.example/team/base:1.0@" + oldDigest
	if code, out := basepin(t, reg, dockerfile(t, "ARG BASE=\""+ref+"\"\nFROM $BASE AS build\n")); code != 0 || !strings.Contains(out, "current") {
		t.Errorf("quoted default: exit %d\n%s", code, out)
	}
	for name, body := range map[string]string{
		"no default":       "ARG BASE\nFROM ${BASE}\n",
		"unpinned default": "ARG BASE=registry.example/team/base:1.0\nFROM ${BASE}\n",
	} {
		if code, out := basepin(t, reg, dockerfile(t, body)); code != 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// A pin of one platform's manifest is current while the tag's index lists
// it, and behind once the index moved on without it.
func TestAPlatformManifestPinIsComparedWithTheIndex(t *testing.T) {
	file := dockerfile(t, "FROM registry.example/team/base:1.0@"+amd64+"\n")
	if code, out := basepin(t, fakeRegistry(t, image{digest: newDigest}), file); code != 0 || !strings.Contains(out, "current") {
		t.Errorf("listed: exit %d\n%s", code, out)
	}
	other := dockerfile(t, "FROM registry.example/team/base:1.0@"+config+"\n")
	if code, out := basepin(t, fakeRegistry(t, image{digest: newDigest, uploaded: now.Add(-30 * 24 * time.Hour)}), other); code != 1 {
		t.Errorf("not listed: exit %d\n%s", code, out)
	}
}

// A token realm must use the registry's scheme and domain.
func TestATokenRealmElsewhereIsRefused(t *testing.T) {
	file := dockerfile(t, "FROM registry.example/team/base:1.0@"+oldDigest+"\n")
	for name, realm := range map[string]string{
		"another domain": "http://auth.example.invalid/token",
		"another scheme": "https://127.0.0.1/token",
	} {
		if code, out := basepin(t, fakeRegistry(t, image{digest: oldDigest, realm: realm}), file); code != 2 || !strings.Contains(out, "token realm") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// -compose checks service images: a pinned one, a variable's pinned
// default; a variable without default and one of -own are skipped; an
// unpinned image or one pinned by digest alone is refused.
func TestComposeImagesAreChecked(t *testing.T) {
	reg := fakeRegistry(t, image{digest: newDigest, uploaded: now.Add(-20 * 24 * time.Hour)})
	ref := "registry.example/team/base:1.0@" + oldDigest
	compose := dockerfile(t, "services:\n  db:\n    image: "+ref+"\n  web:\n    image: \"${WEB_IMAGE:-"+ref+"}\"\n  app:\n    image: ${APP_IMAGE:?set it}\n  proxy:\n    image: example/proxy:1.0@"+oldDigest+"\n")
	code, out := basepin(t, reg, "-own", "example/", "-compose", compose)
	if code != 1 || strings.Count(out, "BEHIND") != 2 || strings.Contains(out, "proxy") {
		t.Errorf("compose: exit %d\n%s", code, out)
	}
	for name, img := range map[string]string{
		"unpinned":    "registry.example/team/base:1.0",
		"digest only": "registry.example/team/base@" + oldDigest,
	} {
		if code, out := basepin(t, reg, "-compose", dockerfile(t, "services:\n  db:\n    image: "+img+"\n")); code != 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}
