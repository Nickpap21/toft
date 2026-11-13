package docker

import (
	"context"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeData answers the filters removeServiceData uses and records removals.
type fakeData struct {
	volumes map[string][]*volume.Volume      // compose project label → volumes
	images  map[string][]dockerimage.Summary // reference filter → images
	inUse   map[string]bool                  // image tags a container still uses
	removed []string
}

func (f *fakeData) VolumeList(_ context.Context, o volume.ListOptions) (volume.ListResponse, error) {
	label := o.Filters.Get("label")[0]
	return volume.ListResponse{Volumes: f.volumes[label]}, nil
}
func (f *fakeData) VolumeRemove(_ context.Context, name string, _ bool) error {
	if name == "paas-vol-gone-data" {
		return cerrdefs.ErrNotFound
	}
	f.removed = append(f.removed, "volume "+name)
	return nil
}
func (f *fakeData) ImageList(_ context.Context, o dockerimage.ListOptions) ([]dockerimage.Summary, error) {
	return f.images[o.Filters.Get("reference")[0]], nil
}
func (f *fakeData) ImageRemove(_ context.Context, ref string, _ dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error) {
	if f.inUse[ref] {
		return nil, cerrdefs.ErrConflict
	}
	f.removed = append(f.removed, "image "+ref)
	return nil, nil
}

func TestRemoveServiceData(t *testing.T) {
	id := "1234abcd-0000-4000-8000-000000000000"
	proj := rollout.ComposeProjectName(id, "shop")
	f := &fakeData{
		volumes: map[string][]*volume.Volume{
			"com.docker.compose.project=" + proj: {
				{Name: proj + "_dbdata", Labels: map[string]string{"com.docker.compose.project": proj}},
				{Name: "someone-elses", Labels: map[string]string{"com.docker.compose.project": proj}}, // carries the label, not the project's name: stays
			},
		},
		images: map[string][]dockerimage.Summary{
			"paas/" + id: {{RepoTags: []string{"paas/" + id + ":dep1", "paas/" + id + ":dep2"}}},
			proj + "-*":  {{RepoTags: []string{proj + "-web:latest", "nginx:1.27"}}}, // shared ID: only its own tag goes
		},
		inUse: map[string]bool{"paas/" + id + ":dep2": true},
	}

	require.NoError(t, removeServiceData(context.Background(), f, id, "shop", []string{"paas-vol-1234abcd-data", "paas-vol-gone-data"}))
	assert.Equal(t, []string{
		"volume paas-vol-1234abcd-data",
		"volume " + proj + "_dbdata",
		"image paas/" + id + ":dep1",
		"image " + proj + "-web:latest",
	}, f.removed, "a volume already gone and an image still in use are not errors")
}

// A moved service's images go from the server it left; its volumes stay
// there, with their data.
func TestRemoveServiceImagesLeavesVolumes(t *testing.T) {
	id := "1234abcd-0000-4000-8000-000000000000"
	proj := rollout.ComposeProjectName(id, "shop")
	f := &fakeData{
		volumes: map[string][]*volume.Volume{"com.docker.compose.project=" + proj: {{Name: proj + "_dbdata"}}},
		images: map[string][]dockerimage.Summary{
			"paas/" + id: {{RepoTags: []string{"paas/" + id + ":dep1"}}},
			proj + "-*":  {{RepoTags: []string{proj + "-web:latest", "nginx:1.27"}}},
		},
	}
	require.NoError(t, removeServiceImages(context.Background(), f, id, "shop"))
	assert.Equal(t, []string{"image paas/" + id + ":dep1", "image " + proj + "-web:latest"}, f.removed)
}

// sizedHost is a host's volumes and their sizes, for serviceData.
type sizedHost struct {
	volumes []*volume.Volume
	sizes   map[string]int64
	slow    bool // DiskUsage answers only when its context ends
}

func (h *sizedHost) VolumeList(context.Context, volume.ListOptions) (volume.ListResponse, error) {
	return volume.ListResponse{Volumes: h.volumes}, nil
}
func (h *sizedHost) DiskUsage(ctx context.Context, _ types.DiskUsageOptions) (types.DiskUsage, error) {
	if h.slow {
		<-ctx.Done()
		return types.DiskUsage{}, ctx.Err()
	}
	du := types.DiskUsage{}
	for name, size := range h.sizes {
		du.Volumes = append(du.Volumes, &volume.Volume{Name: name, UsageData: &volume.UsageData{Size: size}})
	}
	return du, nil
}

func TestServiceDataNamesWhatADeleteRemoves(t *testing.T) {
	id := "1234abcd-0000-4000-8000-000000000000"
	proj := rollout.ComposeProjectName(id, "shop")
	label := map[string]string{"com.docker.compose.project": proj}
	host := &sizedHost{
		volumes: []*volume.Volume{
			{Name: "paas-vol-1234abcd-data"},
			{Name: proj + "_dbdata", Labels: label},
			{Name: "someone-elses", Labels: label},                       // the label without the name: not the stack's
			{Name: proj + "_cache", Labels: map[string]string{"x": "y"}}, // the name without the label: not the stack's
		},
		sizes: map[string]int64{"paas-vol-1234abcd-data": 2048},
	}
	refs := []service.DataRef{{ID: id, Name: "shop", Volumes: []string{"paas-vol-1234abcd-data", "paas-vol-1234abcd-never"}}}

	got, err := serviceData(context.Background(), host, refs, true)
	require.NoError(t, err)
	assert.Equal(t, []service.DataVolume{
		{Name: "paas-vol-1234abcd-data", Bytes: 2048, From: service.DataFromVolume},
		{Name: proj + "_dbdata", Bytes: -1, From: service.DataFromCompose}, // not sized: listed all the same
	}, got[id], "a volume never created holds nothing and isn't named")
}

// Sizes that take too long to read come back unknown; the volumes stay.
func TestServiceDataSizeTimeout(t *testing.T) {
	was := dataSizeTimeout
	dataSizeTimeout = 10 * time.Millisecond
	t.Cleanup(func() { dataSizeTimeout = was })
	host := &sizedHost{volumes: []*volume.Volume{{Name: "paas-vol-1234abcd-data"}}, slow: true}
	refs := []service.DataRef{{ID: "s1", Name: "db", Volumes: []string{"paas-vol-1234abcd-data"}}}

	got, err := serviceData(context.Background(), host, refs, true)
	require.NoError(t, err)
	assert.Equal(t, []service.DataVolume{{Name: "paas-vol-1234abcd-data", Bytes: -1, From: service.DataFromVolume}}, got["s1"])
}
