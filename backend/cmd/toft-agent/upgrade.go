package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"golang.org/x/crypto/ssh"
)

// maxBinary bounds what the panel may send as a new agent.
const maxBinary = 64 << 20

var versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// subsystem runs one of the agent's typed requests and returns its exit
// status. Output goes to the channel: "ok" or the reason it refused.
func (a *agentServer) subsystem(sconn *ssh.ServerConn, name string, ch ssh.Channel) int {
	var err error
	switch name {
	case agentproto.SubsystemUpgrade:
		err = a.upgrade(ch)
		if err == nil && a.rollouts != nil {
			select {
			case <-a.rollouts.whenIdle():
			default:
				// A rollout runs: the new version starts once none does
				// (D-111). The connection stays up meanwhile.
				_, _ = io.WriteString(ch, "ok\n")
				slog.Info("the new version starts once the running rollouts end")
				go func() { <-a.rollouts.whenIdle(); a.restartNow() }()
				return 0
			}
		}
		if err == nil {
			a.mu.Lock()
			a.restart = true
			a.mu.Unlock()
			_, _ = io.WriteString(ch, "ok\n")
			// Let the answer reach the panel, then end the connection.
			go func() { time.Sleep(500 * time.Millisecond); _ = sconn.Close() }()
			return 0
		}
	case agentproto.SubsystemRotate:
		if err = a.rotatePrepare(ch); err == nil {
			return 0
		}
	case agentproto.SubsystemRotateCommit:
		if err = a.rotateCommit(ch); err == nil {
			return 0
		}
	case agentproto.SubsystemRollout, agentproto.SubsystemRolloutAttach, agentproto.SubsystemRolloutList:
		if a.rollouts == nil {
			err = errors.New("this agent can't reach Docker to run rollouts")
		} else if err = a.rolloutRequest(name, ch); err == nil {
			return 0
		}
	case agentproto.SubsystemDesired:
		if a.rollouts == nil {
			err = errors.New("this agent can't reach Docker to check the desired state")
		} else if err = a.rollouts.desired(ch); err == nil {
			return 0
		}
	case agentproto.SubsystemRoutes:
		if a.rollouts == nil {
			err = errors.New("this agent can't reach Docker to write the routes")
		} else {
			var req agentproto.RoutesRequest
			var n int
			if err = readRequest(ch, &req); err == nil {
				err = checkEpoch(req.Epoch)
			}
			if err == nil && len(a.rollouts.busy()) > 0 {
				// Its route was chosen when the rollout started: the panel
				// asks again once it ends.
				err = errors.New("a rollout is running: the routes are written once it ends")
			}
			if err != nil {
				break
			}
			if n, err = a.rollouts.routes.sync(context.Background()); err == nil {
				_ = json.NewEncoder(ch).Encode(agentproto.RoutesAck{Services: n})
				return 0
			}
		}
	case agentproto.SubsystemMetrics:
		if a.rollouts == nil {
			err = errors.New("this agent can't reach Docker, so it keeps no metrics")
		} else if err = a.rollouts.metrics.serve(ch); err == nil {
			return 0
		}
	case agentproto.SubsystemHostUpdate, agentproto.SubsystemHostUpdateStatus:
		if err = hostUpdate(name, ch); err == nil {
			return 0
		}
	case agentproto.SubsystemUninstall:
		_, _ = io.WriteString(ch, "ok\n")
		go func() {
			time.Sleep(500 * time.Millisecond)
			slog.Info("the panel asked this agent to remove itself")
			removeSelf()
			os.Exit(agentproto.ExitRevoked)
		}()
		return 0
	}
	slog.Warn("refused a request from the panel", "request", name, "error", err)
	_, _ = io.WriteString(ch, "error: "+err.Error()+"\n")
	return 1
}

// rolloutRequest serves the rollout requests (D-111).
func (a *agentServer) rolloutRequest(name string, ch ssh.Channel) error {
	switch name {
	case agentproto.SubsystemRollout:
		var req rollout.Request
		if err := readRequest(ch, &req); err != nil {
			return err
		}
		if err := checkRolloutID(req.ID()); err != nil {
			return err
		}
		if err := checkEpoch(req.Epoch); err != nil {
			return err
		}
		if req.ServiceID() == "" {
			return errors.New("the rollout names no service")
		}
		if req.Compose != nil {
			if err := checkComposeUp(*req.Compose); err != nil {
				return err
			}
		}
		if err := a.rollouts.start(req); err != nil {
			return err
		}
		return a.rollouts.attach(ch, req.ID(), 0)
	case agentproto.SubsystemRolloutAttach:
		var at rollout.Attach
		if err := readRequest(ch, &at); err != nil {
			return err
		}
		if err := checkRolloutID(at.ID); err != nil {
			return err
		}
		return a.rollouts.attach(ch, at.ID, at.Offset)
	default:
		return json.NewEncoder(ch).Encode(list())
	}
}

// upgrade reads an UpgradeHeader line and the binary, and installs it if
// the release key signed it as this version and it is newer than this agent.
func (a *agentServer) upgrade(ch ssh.Channel) error {
	br := bufio.NewReader(ch)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read the header: %w", err)
	}
	var h agentproto.UpgradeHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return fmt.Errorf("read the header: %w", err)
	}
	if !versionRe.MatchString(h.Version) {
		return fmt.Errorf("%q is not a release version", h.Version)
	}
	if _, ok := buildinfo.Parse(buildinfo.Version); ok && !buildinfo.Newer(h.Version, buildinfo.Version) {
		return fmt.Errorf("refusing %s: this agent runs %s and never goes back", h.Version, buildinfo.Version)
	}
	if h.Size <= 0 || h.Size > maxBinary {
		return fmt.Errorf("a binary of %d bytes", h.Size)
	}
	bin := make([]byte, h.Size)
	if _, err := io.ReadFull(br, bin); err != nil {
		return fmt.Errorf("read the binary: %w", err)
	}
	sum := sha256.Sum256(bin)
	if hex.EncodeToString(sum[:]) != strings.ToLower(h.SHA256) {
		return errors.New("the binary's SHA-256 doesn't match")
	}
	if err := verifyManifest(h.Version, runtime.GOARCH, sum, h.ManifestSignature); err != nil {
		return err
	}
	return installVersion(h.Version, bin)
}

// manifest is the line release.yml signs for each agent binary
// (agentproto.UpgradeHeader.ManifestSignature).
func manifest(version, arch string, sum [32]byte) []byte {
	return fmt.Appendf(nil, "toft-agent %s linux %s %x\n", version, arch, sum)
}

// verifyManifest checks the release key signed version for linux/arch with
// the binary whose SHA-256 is sum, so a signed release can't be offered
// under another version (D-123). An agent with no release key refuses
// every upgrade: it is reinstalled instead.
func verifyManifest(version, arch string, sum [32]byte, sigB64 string) error {
	key := releasePublicKey()
	if key == nil {
		return errors.New("this agent has no release key, so it can't verify upgrades: install it again from the panel")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("the upgrade carries no release manifest signature")
	}
	if !ed25519.Verify(key, manifest(version, arch, sum), sig) {
		return fmt.Errorf("the release key didn't sign this binary as %s for linux/%s", version, arch)
	}
	return nil
}

// installVersion writes releases/<v>/toft-agent and makes it current, with
// the running version as the trial's way back.
func installVersion(version string, bin []byte) error {
	dir := filepath.Join(libDir, "releases", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "toft-agent"), bin, 0o755); err != nil {
		return err
	}
	prev := buildinfo.Version
	if err := switchCurrent(version); err != nil {
		return err
	}
	s := readState()
	s.Trial, s.Previous = version, prev
	writeState(s)
	slog.Info("installed a new version; restarting into it", "version", version, "from", prev)
	return nil
}

// switchCurrent points current at releases/<v> (a symlink swapped by rename).
func switchCurrent(version string) error {
	if version == "" {
		return errors.New("no version to switch to")
	}
	target := filepath.Join("releases", version)
	if _, err := os.Stat(filepath.Join(libDir, target, "toft-agent")); err != nil {
		return fmt.Errorf("version %s is not installed: %w", version, err)
	}
	tmp := filepath.Join(libDir, "current.tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(libDir, "current"))
}

// rotatePrepare makes a new key, kept aside, and answers its public half.
func (a *agentServer) rotatePrepare(ch ssh.Channel) error {
	next, err := createKey(nextKeyFile())
	if err != nil {
		return fmt.Errorf("make a new key: %w", err)
	}
	_, _ = io.WriteString(ch, authorizedKey(next.PublicKey())+"\n")
	return nil
}

// rotateCommit switches to the key rotatePrepare made; the next connection
// presents it (the panel has pinned it by now).
func (a *agentServer) rotateCommit(ch ssh.Channel) error {
	if err := os.Rename(nextKeyFile(), keyFile()); err != nil {
		return fmt.Errorf("switch keys: %w", err)
	}
	signer, err := loadOrCreateKey()
	if err != nil {
		return err
	}
	*a.signer = signer
	slog.Info("switched to a new agent key", "fingerprint", ssh.FingerprintSHA256(signer.PublicKey()))
	_, _ = io.WriteString(ch, "ok\n")
	return nil
}

// removeSelf disables the service and deletes the agent's files. The key
// goes with them, so the server can only come back by joining again.
func removeSelf() {
	_ = exec.Command("systemctl", "disable", "toft-agent.service").Run()
	removeRelayInterface()
	for _, p := range []string{unitFile, envFile, binLink} {
		_ = os.Remove(p)
	}
	_ = os.RemoveAll(stateDir)
	_ = os.RemoveAll(filepath.Dir(envFile))
	_ = os.RemoveAll(libDir)
	_ = exec.Command("systemctl", "daemon-reload").Run()
}
