package org_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// isolationRepo counts the services that follow the default, by server.
type isolationRepo struct {
	fakeRepository
	following map[string]int
}

func (*isolationRepo) GetSettings(context.Context, string) (*org.OrgSettings, error) {
	return &org.OrgSettings{DefaultIsolation: org.IsolationStandard}, nil
}

func (r *isolationRepo) CountDefaultIsolationServices(context.Context, string) (map[string]int, error) {
	return r.following, nil
}

type panelSandbox struct{ asked []string }

func (*panelSandbox) SandboxedLevels(context.Context, string) ([]server.SandboxedLevel, error) {
	return nil, nil
}

func (p *panelSandbox) Sandboxed(_ context.Context, id string) (server.Level, string, error) {
	p.asked = append(p.asked, id)
	return server.Level{ID: "sandboxed", State: "available", Reason: "systrap here"}, "20261012.0", nil
}

func (*panelSandbox) SandboxedStrict(context.Context, string) (server.Level, string, error) {
	return server.Level{ID: "sandboxed_strict", State: "unavailable", Reason: "Needs Toft's agent"}, "20261012.0", nil
}

// The panel's own server reports what its probe read (D-97), not a fixed
// "unavailable", whenever the workspace has services there.
func TestIsolation_PanelServerReportsItsGvisor(t *testing.T) {
	sb := &panelSandbox{}
	svc := org.NewService(&isolationRepo{following: map[string]int{"": 2}}, zap.NewNop()).WithServerSandboxes(sb)
	out, err := svc.Isolation(context.Background(), "org-1")
	require.NoError(t, err)
	require.Len(t, out.Servers, 1)
	assert.Equal(t, org.IsolationServer{Name: "The panel's own server", Sandboxed: "available", Reason: "systrap here",
		SandboxedStrict: "unavailable", StrictReason: "Needs Toft's agent", GvisorVersion: "20261012.0", Following: 2}, out.Servers[0])
	assert.Equal(t, []string{""}, sb.asked)

	// Services there that chose their own level: listed, none following.
	out, err = org.NewService(&isolationRepo{following: map[string]int{"": 0}}, zap.NewNop()).WithServerSandboxes(sb).Isolation(context.Background(), "org-1")
	require.NoError(t, err)
	require.Len(t, out.Servers, 1)
	assert.Equal(t, 0, out.Servers[0].Following)

	// None there: not listed.
	out, err = org.NewService(&isolationRepo{following: map[string]int{}}, zap.NewNop()).WithServerSandboxes(sb).Isolation(context.Background(), "org-1")
	require.NoError(t, err)
	assert.Empty(t, out.Servers)
}
