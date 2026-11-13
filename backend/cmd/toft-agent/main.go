// Command toft-agent connects a server to its Toft panel (D-106). It dials
// out to the panel over a WebSocket on 443 and serves SSH inside it, so the
// panel reaches Docker, runs commands and opens shells on the server without
// the server accepting any connection. Installed by the panel's install
// script as toft-agent.service.
//
//	toft-agent run                 the service (reads /etc/toft-agent/agent.env)
//	toft-agent join --panel URL --server ID --token-file F --panel-key SHA256:...
//	toft-agent check               can this server reach its panel?
//	toft-agent status              what the agent knows
//	toft-agent uninstall           remove the agent from this server
//	toft-agent version
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "run":
		err = run()
	case "join":
		err = join(os.Args[2:])
	case "check":
		err = check()
	case "status":
		err = status()
	case "uninstall":
		err = uninstall()
	case "version", "--version", "-v":
		fmt.Println(buildinfo.Version)
	default:
		fmt.Fprintf(os.Stderr, "usage: toft-agent [run|join|check|status|uninstall|version]\n")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "toft-agent:", err)
		os.Exit(1)
	}
}
