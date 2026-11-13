package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// RemoveServiceData deletes what a deleted service leaves on this host once
// its containers are gone: its named volumes, its compose stack's volumes
// and the images built for it. Pulled images stay; other services may use
// them, and the next image prune takes the rest.
func (o *LocalOrchestrator) RemoveServiceData(ctx context.Context, serviceID, serviceName string, volumes []string) error {
	return removeServiceData(ctx, o.dockerClient, serviceID, serviceName, volumes)
}

// RemoveServiceData is LocalOrchestrator.RemoveServiceData on a remote host.
func (o *RemoteOrchestrator) RemoveServiceData(ctx context.Context, serviceID, serviceName string, volumes []string) error {
	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return removeServiceData(ctx, dc, serviceID, serviceName, volumes)
}

// RemoveServiceImages deletes the images built for a service on this host
// (removeServiceImages): a moved service's, on the server it left. Its
// volumes stay there, with their data.
func (o *LocalOrchestrator) RemoveServiceImages(ctx context.Context, serviceID, serviceName string) error {
	return removeServiceImages(ctx, o.dockerClient, serviceID, serviceName)
}

// RemoveServiceImages is LocalOrchestrator.RemoveServiceImages on a remote host.
func (o *RemoteOrchestrator) RemoveServiceImages(ctx context.Context, serviceID, serviceName string) error {
	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return removeServiceImages(ctx, dc, serviceID, serviceName)
}

// dataClient is the part of the Docker client removeServiceData uses.
type dataClient interface {
	VolumeList(ctx context.Context, options volume.ListOptions) (volume.ListResponse, error)
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
	ImageList(ctx context.Context, options dockerimage.ListOptions) ([]dockerimage.Summary, error)
	ImageRemove(ctx context.Context, imageID string, options dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error)
}

var _ dataClient = (*client.Client)(nil)

// A move removes a service's images through deployment.ImageRemover; without
// these, a changed signature would skip it silently.
var (
	_ deployment.ImageRemover = (*LocalOrchestrator)(nil)
	_ deployment.ImageRemover = (*RemoteOrchestrator)(nil)
)

// service.Data reads through this; without it, a changed signature would
// make every server unreadable and every delete ask for the name.
type dataReader interface {
	ServiceData(ctx context.Context, refs []service.DataRef, sizes bool) (map[string][]service.DataVolume, error)
}

var (
	_ dataReader = (*LocalOrchestrator)(nil)
	_ dataReader = (*RemoteOrchestrator)(nil)
)

// removeServiceData removes the named volumes given, the volumes of the
// service's compose projects and the images built for the service: git
// builds (paas/<service id>:<deployment>) and compose builds
// (<project>-<compose service>). Names are platform-derived, never read
// from labels alone (tenants choose their images' labels); a compose
// volume must carry its project's label and its project's name prefix.
func removeServiceData(ctx context.Context, dc dataClient, serviceID, serviceName string, volumes []string) error {
	var errs []error
	gone := func(err error) bool { return err == nil || cerrdefs.IsNotFound(err) }

	for _, v := range volumes {
		if err := dc.VolumeRemove(ctx, v, false); !gone(err) {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", v, err))
		}
	}

	projects := rollout.ComposeProjectNames(serviceID, serviceName)
	for _, proj := range projects {
		list, err := dc.VolumeList(ctx, volume.ListOptions{Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+proj))})
		if err != nil {
			errs = append(errs, fmt.Errorf("list volumes of %s: %w", proj, err))
			continue
		}
		for _, name := range composeVolumes(list.Volumes, []string{proj}) {
			if err := dc.VolumeRemove(ctx, name, false); !gone(err) {
				errs = append(errs, fmt.Errorf("remove volume %s: %w", name, err))
			}
		}
	}

	if err := removeServiceImages(ctx, dc, serviceID, serviceName); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// composeVolumes are the volumes of the compose projects given: the
// project's label and its name prefix, never the label alone (tenants
// choose labels). A delete removes these; ServiceData names them.
func composeVolumes(vols []*volume.Volume, projects []string) []string {
	out := []string{}
	for _, v := range vols {
		if v == nil {
			continue
		}
		for _, proj := range projects {
			if v.Labels["com.docker.compose.project"] == proj && strings.HasPrefix(v.Name, proj+"_") {
				out = append(out, v.Name)
				break
			}
		}
	}
	return out
}

// dataSizeTimeout bounds reading volume sizes for ServiceData: Docker walks
// every volume's files, which takes long on big ones. Past it, sizes are
// unknown (-1); no volume is left out for it. Listing the volumes gets the
// same bound, so a hung daemon can't hold a delete dialog.
var dataSizeTimeout = 10 * time.Second

// ServiceData names what deleting each service would remove from this host
// (service.Data): its own named volumes that exist and its compose stack's,
// with sizes, from one volume list and one size read.
func (o *LocalOrchestrator) ServiceData(ctx context.Context, refs []service.DataRef, sizes bool) (map[string][]service.DataVolume, error) {
	return serviceData(ctx, o.dockerClient, refs, sizes)
}

// ServiceData is LocalOrchestrator.ServiceData on a remote host.
func (o *RemoteOrchestrator) ServiceData(ctx context.Context, refs []service.DataRef, sizes bool) (map[string][]service.DataVolume, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return serviceData(ctx, dc, refs, sizes)
}

// sizedClient is the part of the Docker client serviceData uses.
type sizedClient interface {
	VolumeList(ctx context.Context, options volume.ListOptions) (volume.ListResponse, error)
	diskUser
}

func serviceData(ctx context.Context, dc sizedClient, refs []service.DataRef, sizes bool) (map[string][]service.DataVolume, error) {
	lctx, cancelList := context.WithTimeout(ctx, dataSizeTimeout)
	defer cancelList()
	list, err := dc.VolumeList(lctx, volume.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	exists := make(map[string]bool, len(list.Volumes))
	for _, v := range list.Volumes {
		if v != nil {
			exists[v.Name] = true
		}
	}
	out := make(map[string][]service.DataVolume, len(refs))
	var names []string
	for _, ref := range refs {
		vols := []service.DataVolume{}
		for _, name := range ref.Volumes {
			if exists[name] { // never created: it holds nothing
				vols = append(vols, service.DataVolume{Name: name, Bytes: -1, From: service.DataFromVolume})
			}
		}
		for _, name := range composeVolumes(list.Volumes, rollout.ComposeProjectNames(ref.ID, ref.Name)) {
			vols = append(vols, service.DataVolume{Name: name, Bytes: -1, From: service.DataFromCompose})
		}
		for _, v := range vols {
			names = append(names, v.Name)
		}
		out[ref.ID] = vols
	}
	if len(names) == 0 || !sizes {
		return out, nil
	}
	sctx, cancel := context.WithTimeout(ctx, dataSizeTimeout)
	defer cancel()
	read, err := volumeSizes(sctx, dc, names)
	if err != nil {
		return out, nil // sizes unknown; the volumes stay listed
	}
	for id, vols := range out {
		for i := range vols {
			if b, ok := read[vols[i].Name]; ok {
				vols[i].Bytes = b
			}
		}
		out[id] = vols
	}
	return out, nil
}

// removeServiceImages removes the images built for a service: git builds
// (paas/<service id>:<deployment>) and compose builds (<project>-<compose
// service>), by their platform-derived names. Pulled images stay; other
// services may use them, and the next image prune takes the rest. An image
// a container still uses stays too.
func removeServiceImages(ctx context.Context, dc dataClient, serviceID, serviceName string) error {
	var errs []error
	gone := func(err error) bool { return err == nil || cerrdefs.IsNotFound(err) }

	// Each Docker reference filter, with the prefix a tag it matches must have.
	refs := [][2]string{{"paas/" + serviceID, "paas/" + serviceID + ":"}}
	for _, proj := range rollout.ComposeProjectNames(serviceID, serviceName) {
		refs = append(refs, [2]string{proj + "-*", proj + "-"})
	}
	for _, ref := range refs {
		images, err := dc.ImageList(ctx, dockerimage.ListOptions{Filters: filters.NewArgs(filters.Arg("reference", ref[0]))})
		if err != nil {
			errs = append(errs, fmt.Errorf("list images %s: %w", ref[0], err))
			continue
		}
		for _, img := range images {
			// By tag, not by ID: an identical build of another stack shares
			// the ID, and keeps its own tag (and so the image).
			for _, tag := range img.RepoTags {
				if !strings.HasPrefix(tag, ref[1]) {
					continue
				}
				_, err := dc.ImageRemove(ctx, tag, dockerimage.RemoveOptions{PruneChildren: true})
				// A conflict is an image a container still uses: it stays.
				if !gone(err) && !cerrdefs.IsConflict(err) {
					errs = append(errs, fmt.Errorf("remove image %s: %w", tag, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}
