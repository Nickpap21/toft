package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"go.uber.org/zap"
)

// ErrNoReset: the service's app has no Reset (a database root, or not a
// catalog app with an admin login; D-160).
var ErrNoReset = errors.New("this app's admin password can't be reset here")

// ResetFailedError is the app refusing the new password: Output is what it
// printed, the password taken out. Nothing changed: the app, the secret and
// everyone's copies keep the old password.
type ResetFailedError struct{ Output string }

func (e *ResetFailedError) Error() string { return "the app refused the new password" }

// ErrResetUnconfirmed: the app's command ran, but whether it took the new
// password isn't known (the connection broke, or it ran past resetTimeout).
// The new password is stored and kept for the caller, as the app most
// likely has it; a Reset again settles it.
var ErrResetUnconfirmed = errors.New("the app's answer didn't arrive: it may have the new password")

// ErrOutcomeUnknown wraps an orchestrator's error after the command
// started: what it did isn't known.
var ErrOutcomeUnknown = errors.New("the command's outcome is unknown")

// resetTimeout bounds a Reset's command, its transaction held meanwhile.
const resetTimeout = 2 * time.Minute

// ExecResult is a finished command's output and exit code.
type ExecResult struct {
	Output   string
	ExitCode int
}

// ContainerCommand is a command for one of a service's running containers:
// its compose service's (ComposeService), or its own (""), with Stdin on
// its standard input.
type ContainerCommand struct {
	ComposeService string
	Argv, Env      []string
	WorkDir, Stdin string
}

// stdinExecer is satisfied by orchestrators that run a command in a
// service's running container with input on stdin, returning its output
// and exit code; ErrNotRunning when none runs, ErrOutcomeUnknown wrapped
// for an error once the command started.
type stdinExecer interface {
	ExecStdin(ctx context.Context, serviceID, serviceName string, cmd ContainerCommand) (ExecResult, error)
}

// AdminLogin is a catalog app's admin login as the service page shows it:
// its user and how Reset applies ("app" by the app's own command, "deploy"
// at the next start, "" none).
type AdminLogin struct {
	User  string
	Reset string
}

// AdminLogin reads the service's admin login: the user from its plain
// variable (or the app's fixed one). The zero value for a service without
// one.
func (s *ServiceManager) AdminLogin(ctx context.Context, serviceID string) (AdminLogin, error) {
	svc, err := s.repo.GetByID(ctx, serviceID)
	if err != nil {
		return AdminLogin{}, fmt.Errorf("get service: %w", err)
	}
	tmpl, ok := catalog.Get(svc.AddonSlug)
	if svc.AddonSlug == "" || !ok || tmpl.AdminPassword == "" {
		return AdminLogin{}, nil
	}
	out := AdminLogin{User: tmpl.AdminUserFixed}
	if r := tmpl.AdminReset; r != nil {
		out.Reset = map[bool]string{true: "app", false: "deploy"}[r.Argv != nil]
	}
	if tmpl.AdminUser != "" && s.variables != nil {
		vars, err := s.variables.List(ctx, serviceID)
		if err != nil {
			return AdminLogin{}, fmt.Errorf("its variables: %w", err)
		}
		for _, v := range vars {
			if v.Key == tmpl.AdminUser {
				out.User = v.Value
			}
		}
	}
	return out, nil
}

// AdminPasswordReset is a Reset's result: the new password, kept for
// userID too; Deploy when it takes effect at the next deploy (MinIO).
type AdminPasswordReset struct {
	Key, User, Password string
	Deploy              bool
}

// ResetAdminPassword gives the service's app login a new generated
// password (D-160), in one transaction: the secret replaced (everyone's
// older copies deleted), the copy kept for userID, and the app's own
// command run with the password on stdin. The transaction commits only if
// the app took it, so a refusal (*ResetFailedError) or a command that never
// ran changes nothing. Its lock on the secret's row makes a second Reset of
// the service wait for the first. ErrResetUnconfirmed: committed, but the
// app's answer never came. It runs to its end whether or not ctx's caller
// leaves. The caller checks who may (owners and developers with a
// confirmed password) and audits it.
func (s *ServiceManager) ResetAdminPassword(ctx context.Context, userID, serviceID string) (*AdminPasswordReset, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resetTimeout)
	defer cancel()
	svc, err := s.repo.GetByID(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	tmpl, ok := catalog.Get(svc.AddonSlug)
	if svc.AddonSlug == "" || !ok || tmpl.AdminReset == nil {
		return nil, ErrNoReset
	}
	// A preview's copy reads its secrets from its original (D-97).
	if copied, err := s.IsPreviewCopy(ctx, serviceID); err != nil {
		return nil, err
	} else if copied {
		return nil, ErrNoReset
	}
	reset := tmpl.AdminReset
	if reset.Argv != nil && svc.RuntimeStatus != "running" {
		return nil, ErrNotRunning
	}
	if s.tx == nil || s.variables == nil || s.reveals == nil {
		return nil, errors.New("reset an admin password: no transactor, variables or reveals")
	}
	login, err := s.AdminLogin(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := &AdminPasswordReset{Key: tmpl.AdminPassword, User: login.User, Password: generatedSecret(""), Deploy: reset.Argv == nil}
	var unconfirmed, applied bool
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		err := s.variables.ReplaceSecret(ctx, serviceID, userID, out.Key, out.Password)
		if errors.Is(err, envvar.ErrNotFound) {
			_, err = s.variables.CreateSecret(ctx, serviceID, userID, envvar.CreateSecretInput{Key: out.Key, Value: out.Password})
		}
		if err != nil {
			return fmt.Errorf("its secret %s: %w", out.Key, err)
		}
		if err := s.reveals.Keep(ctx, serviceID, userID, out.Key, out.Password, "reset"); err != nil {
			return err
		}
		if out.Deploy {
			return nil
		}
		err = s.applyReset(ctx, svc, reset, out)
		if errors.Is(err, ErrOutcomeUnknown) {
			s.log.Warn("reset an admin password: no answer from the app", zap.String("service_id", serviceID), zap.Error(err))
			unconfirmed = true
			return nil
		}
		applied = err == nil
		return err
	})
	if err != nil {
		if applied {
			s.log.Error("reset an admin password: the app took it, storing it failed", zap.String("service_id", serviceID), zap.Error(err))
		}
		return nil, err
	}
	if unconfirmed {
		return nil, ErrResetUnconfirmed
	}
	return out, nil
}

// applyReset runs the app's command with the new password on stdin. Its
// output is never logged; a refusal returns it without the password.
func (s *ServiceManager) applyReset(ctx context.Context, svc *Service, reset *catalog.AdminReset, r *AdminPasswordReset) error {
	orch, cleanup, err := s.orchestratorFor(ctx, svc.ServerID)
	if err != nil {
		return err
	}
	defer cleanup()
	execer, ok := orch.(stdinExecer)
	if !ok {
		return errors.New("orchestrator cannot run a command with input")
	}
	res, err := execer.ExecStdin(ctx, svc.ID, svc.Name, ContainerCommand{
		ComposeService: reset.Container, Argv: reset.Argv(r.User), Env: reset.Env, WorkDir: reset.WorkDir, Stdin: reset.Stdin(r.Password),
	})
	if err != nil {
		return err
	}
	output := strings.TrimSpace(strings.ReplaceAll(res.Output, r.Password, "••••"))
	if res.ExitCode != 0 || (reset.Prints != "" && output != reset.Prints) {
		if len(output) > 500 {
			output = "…" + output[len(output)-500:]
		}
		return &ResetFailedError{Output: output}
	}
	return nil
}
