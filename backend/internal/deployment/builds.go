package deployment

import "github.com/Nickpap21/toft/backend/internal/deploypolicy"

// Builds is what a deploy's builds run under (D-170). Every build asks its
// host's firewall first (builder.Run), whatever a probe of the server once
// read.
type Builds struct {
	// Untrusted: the deploy is an untrusted preview's (D-97). It builds on
	// a builder of its own, which an operator's list of private addresses
	// never opens.
	Untrusted bool
	// Unchecked: the build doesn't ask the firewall. The panel's own host
	// under TOFT_DEV_BUILDS_WITHOUT_FIREWALL, never a server.
	Unchecked bool
}

// WithDevBuilds is TOFT_DEV_BUILDS_WITHOUT_FIREWALL: on the panel's own
// host, and only there, a build runs without the firewall, with a warning
// in its log. A development stack's setting (a Mac has no firewall of
// Toft's); main refuses it in production.
func (s *Service) WithDevBuilds(without bool) { s.devBuilds = without }

// builds is what input's builds run under on its server.
func (s *Service) builds(input TriggerInput, appendLog func(string)) Builds {
	b := Builds{Untrusted: input.netPlan.Home.Untrusted}
	// The server is the one ForDeploy builds on: input.Server's.
	if s.devBuilds && (input.Server == nil || input.Server.ID == "") {
		if input.SourceType != deploypolicy.SourceImage {
			appendLog("⚠ TOFT_DEV_BUILDS_WITHOUT_FIREWALL is set: this host's firewall doesn't keep a build off private addresses or off the host itself. A development stack's setting, never a server's.")
		}
		b.Unchecked = true
	}
	return b
}
