// toft-git runs git for Toft's repositories (D-71), alone in the paas-git
// container: no network, no secrets, only the repositories and the socket
// the backend calls it on (internal/gitstore).
//
//	toft-git serve    listen on the socket
//	toft-git health   exit 0 when the socket answers (the container's check)
//	toft-git backup   bundle the repositories that changed (the nightly backup)
//	toft-git restore  rebuild missing or empty repositories from their bundles
//
// Started as pre-receive or post-receive (the links in gitstore.HooksDir),
// it is git's push hook instead (D-75).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
)

func main() {
	gitstore.HookMain()
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "health":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = gitstore.NewClient(gitstore.Socket).Health(ctx)
	case "backup":
		err = backup()
	case "restore":
		err = restore()
	default:
		err = errors.New("usage: toft-git serve|health|backup|restore")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "toft-git:", err)
		os.Exit(1)
	}
}

func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	v, err := gitstore.CheckVersion(ctx)
	if err != nil {
		return err
	}
	// A socket left by a stopped container would refuse the bind.
	if err := os.Remove(gitstore.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove old socket: %w", err)
	}
	ln, err := net.Listen("unix", gitstore.Socket)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	// Only this user and root (the backend) may connect.
	if err := os.Chmod(gitstore.Socket, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	srv := &http.Server{Handler: gitstore.NewExecutor(gitstore.Root).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		// Calls in progress end with the container: the backend reports
		// them to their clients.
		_ = srv.Close()
	}()
	fmt.Fprintf(os.Stderr, "toft-git: git %s, serving %s on %s\n", v, gitstore.Root, gitstore.Socket)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// backup bundles the repositories (D-79) and prints what it did as JSON;
// a repository it couldn't bundle fails it, with the others done.
func backup() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := gitstore.CheckVersion(ctx); err != nil {
		return err
	}
	rep, err := gitstore.Backup(ctx, gitstore.Root)
	_ = json.NewEncoder(os.Stdout).Encode(rep)
	if err != nil {
		return err
	}
	if len(rep.Failed) > 0 {
		return fmt.Errorf("%d of %d repositories not bundled, the first: %s: %s", len(rep.Failed), rep.Repositories, rep.Failed[0].ID, rep.Failed[0].Error)
	}
	return nil
}

// restore rebuilds the repositories from their bundles (install.sh
// --restore) and prints what it did as JSON.
func restore() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := gitstore.CheckVersion(ctx); err != nil {
		return err
	}
	rep, err := gitstore.Restore(ctx, gitstore.Root)
	_ = json.NewEncoder(os.Stdout).Encode(rep)
	if err != nil {
		return err
	}
	if len(rep.Failed) > 0 {
		return fmt.Errorf("%d repositories not restored, the first: %s: %s", len(rep.Failed), rep.Failed[0].ID, rep.Failed[0].Error)
	}
	return nil
}
