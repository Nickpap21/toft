package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
)

type fakeAllocQueries struct {
	strs    []sqlcdb.ListLimitStringsForServerRow
	compose sqlcdb.SumAllocatedForServerRow
}

func (f fakeAllocQueries) SumAllocatedForServer(context.Context, pgtype.UUID) (sqlcdb.SumAllocatedForServerRow, error) {
	return f.compose, nil
}
func (f fakeAllocQueries) SumAllocatedForServerOrg(context.Context, sqlcdb.SumAllocatedForServerOrgParams) (sqlcdb.SumAllocatedForServerOrgRow, error) {
	return sqlcdb.SumAllocatedForServerOrgRow(f.compose), nil
}
func (f fakeAllocQueries) ListLimitStringsForServer(context.Context, pgtype.UUID) ([]sqlcdb.ListLimitStringsForServerRow, error) {
	return f.strs, nil
}
func (f fakeAllocQueries) ListLimitStringsForServerOrg(context.Context, sqlcdb.ListLimitStringsForServerOrgParams) ([]sqlcdb.ListLimitStringsForServerOrgRow, error) {
	out := make([]sqlcdb.ListLimitStringsForServerOrgRow, 0, len(f.strs))
	for _, s := range f.strs {
		out = append(out, sqlcdb.ListLimitStringsForServerOrgRow(s))
	}
	return out, nil
}

func strp(s string) *string { return &s }

// Single-container limits are parsed, compose panel sums are added, and a
// service with no memory limit (an exception) adds 0 memory.
func TestSumAllocatedAddsSingleAndComposeLimits(t *testing.T) {
	q := fakeAllocQueries{
		strs: []sqlcdb.ListLimitStringsForServerRow{
			{CpuLimit: strp("0.5"), MemoryLimit: strp("512m")},
			{CpuLimit: nil, MemoryLimit: strp("1g")},
			{CpuLimit: strp(""), MemoryLimit: nil}, // exception, no limit
		},
		compose: sqlcdb.SumAllocatedForServerRow{ComposeMemoryMb: 1280, ComposeCpuCores: 0.25},
	}
	cpu, mem, err := sumAllocatedForServer(context.Background(), q, pgtype.UUID{})
	require.NoError(t, err)
	assert.InDelta(t, 0.75, cpu, 1e-9)
	assert.InDelta(t, 512+1024+1280, mem, 1e-9)

	cpu, mem, err = sumAllocatedForServerOrg(context.Background(), q, pgtype.UUID{}, pgtype.UUID{})
	require.NoError(t, err)
	assert.InDelta(t, 0.75, cpu, 1e-9)
	assert.InDelta(t, 2816, mem, 1e-9)
}

// Rows come ordered by workspace and project; each workspace gets its own
// sums by the same rules, its projects once each, and its service count.
func TestGroupAllocationsByWorkspace(t *testing.T) {
	a, b := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	rows := []sqlcdb.ListServerAllocationsRow{
		{OrgID: a, OrgName: "Personal", ProjectName: "initech", SourceType: "docker-compose", ComposeMemoryMb: 128, ComposeCpuCores: 0.5},
		{OrgID: a, OrgName: "Personal", ProjectName: "initech", SourceType: "docker-image", CpuLimit: strp("0.5"), MemoryLimit: strp("512m")},
		{OrgID: b, OrgName: "Northwind", ProjectName: "", SourceType: "docker-image", MemoryLimit: strp("1g")}, // workspace-shared
		{OrgID: b, OrgName: "Northwind", ProjectName: "nwbooking", SourceType: "docker-image"},                 // no limits
		{OrgID: b, OrgName: "Northwind", ProjectName: "northwind", SourceType: "docker-image", MemoryLimit: strp("256m")},
	}
	got := groupAllocations(rows)
	require.Len(t, got, 2)
	assert.Equal(t, "Personal", got[0].OrgName)
	assert.Equal(t, []string{"initech"}, got[0].Projects)
	assert.Equal(t, 2, got[0].Services)
	assert.InDelta(t, 1.0, got[0].CPUCores, 1e-9)
	assert.InDelta(t, 640, got[0].MemoryMB, 1e-9)
	assert.Equal(t, []string{"nwbooking", "northwind"}, got[1].Projects)
	assert.Equal(t, 3, got[1].Services)
	assert.InDelta(t, 0, got[1].CPUCores, 1e-9)
	assert.InDelta(t, 1280, got[1].MemoryMB, 1e-9)

	assert.NotNil(t, groupAllocations(nil), "an empty server is [] in JSON, not null")
}
