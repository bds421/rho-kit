// Command kit-basepin fails a release whose Dockerfiles pin a base image by
// a digest that is behind its tag, once the tag's digest is past the
// dependency wait or an advisory affects the pinned one.
module github.com/bds421/rho-kit/cmd/kit-basepin/v2

go 1.26.8
