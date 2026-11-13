package observability

import "testing"

// Where a service's logs go (D-148): an address set wins (unless only
// containers resolve it); else the catalog Loki's 127.0.0.1 port, for the
// services on its own server and not for Loki itself.
func TestShipTo(t *testing.T) {
	local := LokiTarget{ServiceID: "loki", ServerID: "srv-1", Port: 39100}
	panel := LokiTarget{ServiceID: "loki", Port: 39101}
	cases := []struct {
		name              string
		target            LokiTarget
		server, svc, want string
	}{
		{"an address set", LokiTarget{URL: "https://logs.example.com", ServerID: "srv-1", Port: 39100}, "srv-2", "app", "https://logs.example.com"},
		{"a container alias", LokiTarget{URL: "http://loki:3100"}, "srv-1", "app", ""},
		{"an alias from before D-48, Loki here", LokiTarget{URL: "http://loki:3100", ServiceID: "loki", ServerID: "srv-1", Port: 39100}, "srv-1", "app", "http://127.0.0.1:39100"},
		{"same server", local, "srv-1", "app", "http://127.0.0.1:39100"},
		{"another server", local, "srv-2", "app", ""},
		{"the panel's server, Loki elsewhere", local, "", "app", ""},
		{"Loki itself", local, "srv-1", "loki", ""},
		{"single server", panel, "", "app", "http://127.0.0.1:39101"},
		{"Loki not deployed yet", LokiTarget{ServiceID: "loki", ServerID: "srv-1"}, "srv-1", "app", ""},
		{"no Loki", LokiTarget{}, "", "app", ""},
	}
	for _, tc := range cases {
		if got := tc.target.ShipTo(tc.server, tc.svc); got != tc.want {
			t.Errorf("%s: ShipTo = %q, want %q", tc.name, got, tc.want)
		}
	}
}
