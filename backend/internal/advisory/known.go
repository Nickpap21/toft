package advisory

// known is every advisory in moby/moby's GitHub repository as of
// 2026-11-11, with the engine releases each affects written by hand from
// its vulnerable and patched versions (the older ones mix Go module, docker
// and free-text entries). Range is parseRange's syntax; empty for one that
// is about no release. CI fails when the repository has one this list
// lacks (scripts/docker-advisories.sh): add it here, newest first.
var known = []struct{ ID, Severity, Summary, FixedIn, Range string }{
	{"GHSA-6jhp-7446-hf7f", "high", "iptables rules for overlay-network encryption are not reinstalled after a firewalld reload", "29.9.3", "< 29.9.3"},
	{"GHSA-7cfq-22r6-qp73", "high", "Docker Engine insecure-registry fallback via malicious DNS responses", "29.8.2", "< 29.8.2"},
	{"GHSA-6m9p-4h64-m6vh", "medium", "Blind VXLAN injection into encrypted overlay networks from cluster peer", "29.8.2", "<= 25.0.18, 26.0.0 - 29.8.1"},
	{"GHSA-rg2x-37c3-w2rh", "high", "Race condition in docker cp allows bind mount redirection to host path", "29.5.1", "< 29.5.1"},
	{"GHSA-vp62-88p7-qqf5", "medium", "Race condition in docker cp allows creation of arbitrary empty files on the host via symlink swap", "29.5.1", "< 29.5.1"},
	{"GHSA-x86f-5xw2-fm2r", "high", "PUT /containers/{id}/archive executes container binary on the host", "29.5.1", "< 29.5.1"},
	{"GHSA-pxq6-2prw-chj9", "medium", "Off-by-one error in plugin privilege validation", "29.3.1", "< 29.3.1"},
	{"GHSA-x744-4wpc-v9h2", "high", "AuthZ plugin bypass with oversized request body", "29.3.1", "< 29.3.1"},
	{"GHSA-x4rx-4gw3-53p4", "medium", "Firewalld reload makes published container ports accessible from remote hosts", "28.3.3", "28.2.x, 28.3.0 - 28.3.2"},
	// Fixed in 28.0.0, and in 25.0.13 on the 25.0 branch.
	{"GHSA-4vq8-7jfc-9cvp", "low", "Firewalld reload removes bridge network isolation", "28.0.0", "< 25.0.13, >= 26.0.0, < 28.0.0"},
	{"GHSA-v23v-6jw2-98fq", "critical", "Authz zero length regression", "27.1.1", "< 23.0.15, 24.0.0 - 24.0.9, 25.0.0 - 25.0.5, 26.0.0 - 26.1.4, 27.0.0 - 27.1.0"},
	{"GHSA-x84c-p2g9-rqv9", "medium", "IPv6 enabled on IPv4-only network interfaces", "26.0.2", "26.0.0, 26.0.1"},
	{"GHSA-mq39-4gv4-mvpx", "medium", "External DNS requests from 'internal' networks could lead to data exfiltration", "26.0.0", "< 23.0.11, >= 24.0.0, < 25.0.5"},
	{"GHSA-xw73-rw38-6vjc", "medium", "Classic builder cache poisoning", "25.0.2", ">= 23.0.0, < 23.0.10, >= 24.0.0, < 24.0.9, >= 25.0.0, < 25.0.2"},
	{"GHSA-jq35-85cj-fj4p", "medium", "/sys/devices/virtual/powercap accessible by default to containers", "24.0.7", "<= 20.10.26, 23.0.0 - 23.0.7, 24.0.0 - 24.0.6"},
	// About Docker's documentation of Swarm's VXLAN port, not a release.
	{"GHSA-vwm3-crmr-xfxw", "high", "The Swarm VXLAN port may be exposed to attack due to ambiguous documentation", "", ""},
	{"GHSA-6wrf-mxfj-pf5p", "medium", "Encrypted overlay network with a single endpoint is unauthenticated", "23.0.3", ">= 1.12.0, < 20.10.24, >= 23.0.0, < 23.0.3"},
	{"GHSA-33pg-m6jh-5237", "medium", "Encrypted overlay network traffic may be unencrypted", "23.0.3", ">= 1.12.0, < 20.10.24, >= 23.0.0, < 23.0.3"},
	{"GHSA-232p-vwff-86mp", "high", "Encrypted overlay network may be unauthenticated", "23.0.3", ">= 1.12.0, < 20.10.24, >= 23.0.0, < 23.0.3"},
	{"GHSA-vp35-85q5-9f25", "low", "Container build can leak any path on the host into the container (using Git CVE-2022-39253)", "20.10.20", "<= 20.10.19"},
	{"GHSA-rc4r-wh2q-q6c4", "low", "Security vulnerability relating to supplementary group permissions", "20.10.18", "< 20.10.18"},
	{"GHSA-2mm7-x5h6-5pvq", "low", "Default inheritable capabilities for linux container should be empty", "20.10.14", "< 20.10.14"},
	{"GHSA-xmmx-7jpf-fx42", "medium", "Ambiguous OCI manifest parsing", "20.10.11", "< 20.10.11"},
	{"GHSA-3fwx-pjgw-3558", "medium", "Insufficiently restricted permissions on data directory", "20.10.9", "< 20.10.9"},
	{"GHSA-v994-f8vw-g7j4", "low", "docker cp allows unexpected chmod of host files", "20.10.9", "< 20.10.9"},
	{"GHSA-7452-xqpj-6rpc", "low", "Access to remapped root allows privilege escalation to real root", "20.10.3", "< 19.03.15, >= 20.0.0, < 20.10.3"},
	{"GHSA-6fj5-m822-rqx8", "medium", "Docker daemon crash during image pull of malicious image", "20.10.3", "< 19.03.15, >= 20.0.0, < 20.10.3"},
}

// mustKnown reads known; a range that doesn't read is a bug the tests catch.
func mustKnown() []Advisory {
	out := make([]Advisory, 0, len(known))
	for _, k := range known {
		a := Advisory{ID: k.ID, Severity: k.Severity, Summary: k.Summary, FixedIn: k.FixedIn, URL: advisoryURL(k.ID)}
		if k.Range != "" {
			spans, err := parseRange(k.Range)
			if err != nil {
				panic("advisory " + k.ID + ": " + err.Error())
			}
			a.spans = spans
		}
		out = append(out, a)
	}
	return out
}
