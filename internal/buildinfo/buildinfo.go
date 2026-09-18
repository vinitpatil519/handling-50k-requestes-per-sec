// Package buildinfo exposes version metadata injected at link time:
//
//	go build -ldflags "-X github.com/titanedge/titanedge/internal/buildinfo.Version=1.2.3 \
//	                   -X github.com/titanedge/titanedge/internal/buildinfo.Commit=abc123"
package buildinfo

import "runtime"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a one-line human readable version string.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ", " + runtime.Version() + ")"
}
