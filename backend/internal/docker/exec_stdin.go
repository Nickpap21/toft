package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// ExecStdin runs cmd in the service's running container (service.ExecStdin).
func (o *LocalOrchestrator) ExecStdin(ctx context.Context, serviceID, serviceName string, cmd service.ContainerCommand) (service.ExecResult, error) {
	return execStdin(ctx, o.dockerClient, serviceID, serviceName, cmd)
}

// ExecStdin runs cmd in the service's running container on the server,
// through the Docker API over SSH (the agent's too).
func (o *RemoteOrchestrator) ExecStdin(ctx context.Context, serviceID, serviceName string, cmd service.ContainerCommand) (service.ExecResult, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return service.ExecResult{}, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return execStdin(ctx, dc, serviceID, serviceName, cmd)
}

// execStdin writes cmd.Stdin to the command, closes it, and returns the
// command's output, stdout and stderr together (at most 64 KiB), with its
// exit code. The input is never in the exec's command line, so neither
// Docker's events nor a process list show it. ctx's end closes the
// connection, which unblocks the read (an SSH channel takes no deadline).
// Errors: service.ErrNotRunning, or another before the command started;
// once it has, every error wraps service.ErrOutcomeUnknown.
func execStdin(ctx context.Context, dc *client.Client, serviceID, serviceName string, cmd service.ContainerCommand) (service.ExecResult, error) {
	name, err := runningContainer(ctx, dc, serviceID, serviceName, cmd.ComposeService)
	if err != nil {
		return service.ExecResult{}, err
	}
	ex, err := dc.ContainerExecCreate(ctx, name, container.ExecOptions{
		Cmd: cmd.Argv, Env: cmd.Env, WorkingDir: cmd.WorkDir,
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return service.ExecResult{}, fmt.Errorf("ContainerExecCreate: %w", err)
	}
	// Attaching starts the command: from here what it did isn't known
	// unless it exits.
	resp, err := dc.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return service.ExecResult{}, fmt.Errorf("%w: ContainerExecAttach: %w", service.ErrOutcomeUnknown, err)
	}
	defer resp.Close()
	stop := context.AfterFunc(ctx, resp.Close)
	defer stop()
	unknown := func(what string, err error) (service.ExecResult, error) {
		return service.ExecResult{}, fmt.Errorf("%w: %s: %w", service.ErrOutcomeUnknown, what, err)
	}
	// HijackedResponse.CloseWrite does nothing on a connection without one,
	// and the command would wait for its input's end until ctx ends.
	cw, ok := resp.Conn.(interface{ CloseWrite() error })
	if !ok {
		return unknown("close stdin", errors.New("the connection can't close its write side"))
	}
	if _, err := io.WriteString(resp.Conn, cmd.Stdin); err != nil {
		return unknown("write stdin", err)
	}
	if err := cw.CloseWrite(); err != nil {
		return unknown("close stdin", err)
	}
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, io.LimitReader(resp.Reader, 64<<10)); err != nil {
		return unknown("read output", err)
	}
	if ctx.Err() != nil {
		return unknown("read output", ctx.Err())
	}
	in, err := dc.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		return unknown("ContainerExecInspect", err)
	}
	if in.Running { // the output reached its limit before the command ended
		return unknown("wait", errors.New("still running"))
	}
	return service.ExecResult{Output: out.String(), ExitCode: in.ExitCode}, nil
}

// runningContainer is the service's running container: its blue/green slot
// (composeService ""), or its compose project's container of composeService.
func runningContainer(ctx context.Context, dc *client.Client, serviceID, serviceName, composeService string) (string, error) {
	list, err := serviceContainerList(ctx, dc, serviceID, serviceName)
	if err != nil {
		return "", err
	}
	for _, c := range list {
		name := rollout.SummaryName(c)
		if c.State != "running" {
			continue
		}
		slot := rollout.IsSlotName(name, serviceID)
		if (composeService == "" && slot) || (composeService != "" && !slot && c.Labels[rollout.LabelComposeService] == composeService) {
			return name, nil
		}
	}
	return "", service.ErrNotRunning
}
