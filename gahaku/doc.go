// Package gahaku composes the render service into the two things the binary
// does: serve gRPC (Serve) and run one rendering job as a worker subprocess
// (Work).
//
// It is the layer the CLI hands its flags to. Everything it composes lives
// under pkg/ — pkg/server, pkg/worker, pkg/render — and none of them knows
// about the CLI.
package gahaku
