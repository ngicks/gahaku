// Package gahaku composes the render service into the two things the binary
// does: serve gRPC (Serve) and run one rendering job as a worker subprocess
// (Work).
//
// It is the layer the CLI hands its flags to. Everything it composes lives in
// the packages beside it — pkg/server, pkg/worker, pkg/render — and none of
// them knows about the CLI.
package gahaku

// Version is the human-readable version string. The release helper at
// internal/cmd/release rewrites this declaration when cutting a release,
// then bumps it to the next "-devel" version after tagging.
//
// Edit by hand only when the release helper is unavailable (e.g. cherry-pick
// of a release commit).
const Version = "v0.0.0-devel"
