//go:build !linux

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
)

// The agent runs on Linux (D-29); elsewhere it builds for its tests only.

func startPTY(*exec.Cmd, uint32, uint32) (*os.File, error) {
	return nil, errors.New("terminals need Linux")
}

func resizePTY(*os.File, uint32, uint32) {}

func copyBuf(dst io.Writer, src io.Reader) (int64, error) { return io.Copy(dst, src) }
