package main

import (
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"golang.org/x/crypto/ssh"
)

// The environment every command gets, as an sshd login as root would give.
var baseEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"HOME=/root", "USER=root", "LOGNAME=root", "SHELL=/bin/sh", "LANG=C.UTF-8",
}

// ptyReq is the payload of a pty-req.
type ptyReq struct {
	Term          string
	Cols, Rows    uint32
	Width, Height uint32
	Modes         string
}

// session serves one session channel: exec, a shell with a PTY, or one of
// the agent's subsystems.
func (a *agentServer) session(ctx context.Context, sconn *ssh.ServerConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var (
		pty     *ptyReq
		started bool
		proc    *running
	)
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			var p ptyReq
			ok := ssh.Unmarshal(r.Payload, &p) == nil && !started
			if ok {
				pty = &p
			}
			reply(r, ok)
		case "window-change":
			var w struct{ Cols, Rows, Width, Height uint32 }
			if proc != nil && ssh.Unmarshal(r.Payload, &w) == nil {
				proc.resize(w.Cols, w.Rows)
			}
			reply(r, true)
		case "signal":
			var s struct{ Signal string }
			if proc != nil && ssh.Unmarshal(r.Payload, &s) == nil {
				proc.signal(signalNamed(s.Signal))
			}
			reply(r, true)
		case "exec", "shell":
			if started {
				reply(r, false)
				continue
			}
			cmdline := ""
			if r.Type == "exec" {
				var e struct{ Command string }
				if ssh.Unmarshal(r.Payload, &e) != nil {
					reply(r, false)
					continue
				}
				cmdline = e.Command
			}
			if agentproto.Typed(cmdline) {
				reply(r, true)
				started = true
				go func() {
					code := a.subsystem(sconn, cmdline, ch)
					sendExit(ch, code)
					_ = ch.Close()
				}()
				continue
			}
			p, err := start(cmdline, pty, ch)
			if err != nil {
				slog.Warn("could not start a command", "error", err)
				reply(r, false)
				continue
			}
			reply(r, true)
			started, proc = true, p
			go func() {
				code := p.wait()
				sendExit(ch, code)
				_ = ch.Close()
			}()
		default:
			// env and anything else: refused (the environment is fixed).
			reply(r, false)
		}
	}
	// The panel closed the channel (a cancelled call): stop what it started.
	if proc != nil {
		proc.stop()
	}
}

func reply(r *ssh.Request, ok bool) {
	if r.WantReply {
		_ = r.Reply(ok, nil)
	}
}

func sendExit(ch ssh.Channel, code int) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(code))
	_, _ = ch.SendRequest("exit-status", false, b)
}

func signalNamed(name string) syscall.Signal {
	switch ssh.Signal(name) {
	case ssh.SIGKILL:
		return syscall.SIGKILL
	case ssh.SIGINT:
		return syscall.SIGINT
	case ssh.SIGHUP:
		return syscall.SIGHUP
	default:
		return syscall.SIGTERM
	}
}

// running is a started command: its process group and, with a PTY, its
// terminal.
type running struct {
	cmd  *exec.Cmd
	tty  *os.File // PTY master; nil without one
	once sync.Once
	done chan struct{}
	code int
}

// command builds the process for cmdline ("" is a login shell). Each one
// runs in its own transient scope in toft-exec.slice when systemd is there,
// so a build counts against neither the agent's memory nor its tasks.
func command(cmdline string) *exec.Cmd {
	args := []string{"/bin/sh", "-c", cmdline}
	if cmdline == "" {
		args = []string{"/bin/bash", "-l"}
		if _, err := os.Stat(args[0]); err != nil {
			args = []string{"/bin/sh", "-l"}
		}
	}
	if os.Geteuid() == 0 {
		if _, err := os.Stat("/run/systemd/system"); err == nil {
			if sr, err := exec.LookPath("systemd-run"); err == nil {
				args = append([]string{sr, "--scope", "--quiet", "--collect", "--slice=toft-exec.slice", "--"}, args...)
			}
		}
	}
	c := exec.Command(args[0], args[1:]...)
	c.Env = baseEnv
	// root's home only for root: another user can't enter it (the tests).
	c.Dir = "/"
	if _, err := os.Stat("/root"); err == nil && os.Geteuid() == 0 {
		c.Dir = "/root"
	}
	return c
}

func start(cmdline string, pty *ptyReq, ch ssh.Channel) (*running, error) {
	c := command(cmdline)
	p := &running{cmd: c, done: make(chan struct{})}
	if pty != nil {
		c.Env = append(c.Env, "TERM="+pty.Term)
		tty, err := startPTY(c, pty.Cols, pty.Rows)
		if err != nil {
			return nil, err
		}
		p.tty = tty
		go func() { _, _ = copyBuf(tty, ch) }()
		go func() {
			_, _ = copyBuf(ch, tty)
		}()
		return p, nil
	}
	c.Stdin, c.Stdout, c.Stderr = ch, ch, ch.Stderr()
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The process may exit before the panel sends EOF on stdin.
	c.WaitDelay = 2 * time.Second
	if err := c.Start(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *running) wait() int {
	err := p.cmd.Wait()
	if p.tty != nil {
		_ = p.tty.Close()
	}
	code := 0
	if err != nil {
		code = 255
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			} else if ee.ExitCode() >= 0 {
				code = ee.ExitCode()
			}
		}
	}
	p.code = code
	close(p.done)
	return code
}

func (p *running) signal(sig syscall.Signal) {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, sig)
	}
}

func (p *running) resize(cols, rows uint32) {
	if p.tty != nil {
		resizePTY(p.tty, cols, rows)
	}
}

// stop ends the process group: TERM, then KILL if it is still there.
func (p *running) stop() {
	p.once.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		p.signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			p.signal(syscall.SIGKILL)
		}
	})
}
