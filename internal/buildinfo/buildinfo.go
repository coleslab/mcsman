// Package buildinfo holds the version stamped in at build time
// (make build VERSION=...).
package buildinfo

// Version is set with -ldflags "-X mcsman/internal/buildinfo.Version=...".
var Version = "dev"
