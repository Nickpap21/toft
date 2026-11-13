package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hostupdate"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
)

// hostUpdateTimeout bounds starting a run and reading its status: the run
// itself is a unit of its own and outlives both.
const hostUpdateTimeout = 30 * time.Second

// hostUpdate serves SubsystemHostUpdate and SubsystemHostUpdateStatus
// (D-134) with the lines internal/hostupdate holds, never a command the
// panel sent.
func hostUpdate(name string, ch io.ReadWriter) error {
	ctx, cancel := context.WithTimeout(context.Background(), hostUpdateTimeout)
	defer cancel()
	if name == agentproto.SubsystemHostUpdateStatus {
		out, err := hostSh(ctx, hostupdate.StatusScript, nil)
		if err != nil {
			return err
		}
		_, err = io.WriteString(ch, out)
		return err
	}
	var req agentproto.HostUpdateRequest
	if err := readRequest(ch, &req); err != nil {
		return err
	}
	line, err := hostupdate.StartLine(req.ID, req.Action)
	if err != nil {
		return err
	}
	out, err := hostSh(ctx, line, strings.NewReader(hostupdate.Script))
	if err != nil {
		return err
	}
	_, err = io.WriteString(ch, out)
	return err
}

// hostSh runs script with sh, as root, with the environment commands get.
func hostSh(ctx context.Context, script string, stdin io.Reader) (string, error) {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	c.Env, c.Dir, c.Stdin = baseEnv, "/", stdin
	out, err := c.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", fmt.Errorf("sh: %w", err)
		}
		return "", errors.New(lastLine(msg))
	}
	return string(out), nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
