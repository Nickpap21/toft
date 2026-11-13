// Package buildinfo is the release a binary was built from (D-61). The
// release build stamps it through the linker:
//
//	go build -ldflags "-X github.com/Nickpap21/toft/backend/pkg/buildinfo.Version=v0.1.0"
package buildinfo

// Version is the release tag, or "dev" for a build that was not stamped.
var Version = "dev"
