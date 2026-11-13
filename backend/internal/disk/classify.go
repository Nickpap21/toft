package disk

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Image is one image as the host's Docker reports it.
type Image struct {
	ID         string
	Tags       []string // repository:tag
	Digests    []string // repository@sha256:…
	Size       int64
	SharedSize int64 // -1 when Docker didn't work it out
	Containers int64 // -1 when Docker didn't count them
}

// unique is what removing the image frees.
func (im Image) unique() int64 {
	if im.SharedSize > 0 && im.SharedSize <= im.Size {
		return im.Size - im.SharedSize
	}
	return im.Size
}

// Volume is one volume as the host's Docker reports it.
type Volume struct {
	Name      string
	Labels    map[string]string
	Bytes     int64 // -1 unknown
	RefCount  int64 // -1 unknown
	CreatedAt *time.Time
}

// Usage is a host's Docker disk use and its filesystem.
type Usage struct {
	Images                []Image
	ImagesBytes           int64
	Volumes               []Volume
	BuildCacheBytes       int64
	BuildCacheReclaimable int64
	ContainersBytes       int64
	SizeBytes, UsedBytes  int64 // the filesystem holding Docker's data
	FreeBytes             int64 // on it, as df's Available
}

// HostService is a service that runs on the host.
type HostService struct {
	ID, Name string
	OrgID    string // the workspace that runs it
	Kept     int    // its rollback images kept, 0 the default
}

// LiveDeployment is a deployment that went live with an image.
type LiveDeployment struct {
	ID, ServiceID, ImageTag string
	Rank                    int // 1 the newest of its service
	Removed                 bool
}

// Facts are what the database knows about a host.
type Facts struct {
	Panel bool
	// OrgID is the workspace the report is for: other workspaces' services
	// on a shared host are classed like any, but never named. "" on the
	// panel's own server, an operator's, where all are.
	OrgID string
	// Shared: the server is shared with other workspaces (D-39), so the
	// report stays scoped even once their services are gone and only their
	// leftover images remain.
	Shared      bool
	Services    []HostService
	Deployments []LiveDeployment
	// Prefixes holds the first 8 characters of every service's ID, on any
	// server.
	Prefixes map[string]bool
	// PanelReleases are the versions toft upgrade keeps on the panel's
	// server (the running one and the one before); nil when the panel
	// wasn't installed with the installer, and then none is offered.
	PanelReleases []string
}

// plan is a host's images and volumes, classed.
type plan struct {
	byKind  map[Kind][]Image
	volumes []OrphanVolume
	inUse   VolumesInUse
	// marks: for a removable image's ID, the deployments that can't be
	// rolled back to once it's gone (builds only).
	marks    map[string][]string
	rollback []RollbackKept
	// named: a host service's name by its ID's first 8 characters, to name
	// builds in the examples.
	named map[string]string
	// others totals the other workspaces' services; shared: the host has
	// some, so an example shows only an image of a service of this
	// workspace (ownRefs holds theirs).
	others  OtherWorkspaces
	shared  bool
	ownRefs map[string]bool
}

var (
	builtRepo   = regexp.MustCompile(`^paas/([0-9a-f]{8})[0-9a-f-]{28}$`)
	serviceVol  = regexp.MustCompile(`^paas-vol-([0-9a-f]{8})-`)
	projectName = regexp.MustCompile(`^paas-(?:.+-)?([0-9a-f]{8})$`)
	anonVol     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// normRef writes an image reference as Docker lists it: no docker.io
// prefixes, :latest when it names no tag or digest.
func normRef(ref string) string {
	ref = strings.TrimPrefix(ref, "docker.io/")
	ref = strings.TrimPrefix(ref, "library/")
	if !strings.Contains(ref, "@") && !strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":") {
		ref += ":latest"
	}
	return ref
}

func repoOf(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i]
	}
	return ref
}

func tagOf(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	return ""
}

func refsOf(im Image) []string {
	out := make([]string, 0, len(im.Tags)+len(im.Digests))
	for _, t := range im.Tags {
		if t != "" && t != "<none>:<none>" {
			out = append(out, normRef(t))
		}
	}
	for _, d := range im.Digests {
		if d != "" && d != "<none>@<none>" {
			out = append(out, normRef(d))
		}
	}
	return out
}

func tagged(im Image) bool {
	for _, t := range im.Tags {
		if t != "" && t != "<none>:<none>" {
			return true
		}
	}
	return false
}

// classify sorts every image and volume of a host into its class.
func classify(u Usage, f Facts) plan {
	p := plan{byKind: map[Kind][]Image{}, marks: map[string][]string{}, named: map[string]string{}, ownRefs: map[string]bool{}}
	kept := map[string]int{}
	names := map[string]string{}
	foreign := map[string]bool{} // service ID → another workspace's
	p.shared = f.Shared && f.OrgID != ""
	for _, s := range f.Services {
		n := s.Kept
		if n <= 0 {
			n = DefaultRollbackKept
		}
		kept[s.ID], names[s.ID] = n, s.Name
		if f.OrgID != "" && s.OrgID != f.OrgID {
			foreign[s.ID], p.shared = true, true
			p.others.Services++
			continue
		}
		if len(s.ID) >= 8 {
			p.named[s.ID[:8]] = s.Name
		}
	}
	keepRefs := map[string]string{}  // ref → the service keeping it
	oldRefs := map[string][]string{} // ref → deployments beyond their service's count
	for _, d := range f.Deployments {
		ref := normRef(d.ImageTag)
		if !foreign[d.ServiceID] {
			p.ownRefs[ref] = true
		}
		if n, ok := kept[d.ServiceID]; ok && d.Rank <= n {
			keepRefs[ref] = d.ServiceID
		} else if !d.Removed && builtRepo.MatchString(repoOf(ref)) {
			// Only a build can't come back: an image service's rollback
			// pulls a removed image again.
			oldRefs[ref] = append(oldRefs[ref], d.ID)
		}
	}
	panelKeep := map[string]bool{}
	for _, v := range f.PanelReleases {
		if v != "" {
			panelKeep[v] = true
		}
	}
	perService := map[string]*RollbackKept{}

	for _, im := range u.Images {
		refs := refsOf(im)
		kind := UnusedPulled
		keeper := ""
		for _, r := range refs {
			if s, ok := keepRefs[r]; ok {
				keeper = s
				break
			}
		}
		switch {
		case im.Containers != 0: // -1 (unknown) keeps it too
			kind = InUse
		case keeper != "":
			kind = Rollback
		case f.Panel && panelRelease(refs, panelKeep, f.PanelReleases != nil) == 1:
			kind = PanelReleases
		case !tagged(im):
			kind = Untagged
		case f.Panel && panelRelease(refs, panelKeep, f.PanelReleases != nil) == 2:
			kind = OldPanelReleases
		default:
			if id8, ok := builtBy(refs); ok {
				if live, compose := f.Prefixes[id8], composeBuilt(refs); live && compose {
					kind = Rollback // a live compose project's own build
				} else {
					kind = OldBuilds
				}
			}
		}
		p.byKind[kind] = append(p.byKind[kind], im)
		if kind == Rollback && foreign[keeper] {
			p.others.RollbackImages++
			p.others.RollbackBytes += im.unique()
		} else if kind == Rollback && keeper != "" {
			rk := perService[keeper]
			if rk == nil {
				rk = &RollbackKept{ServiceID: keeper, ServiceName: names[keeper], Kept: kept[keeper]}
				perService[keeper] = rk
			}
			rk.Images++
			rk.Bytes += im.unique()
		}
		if kind.Removable() {
			for _, r := range refs {
				p.marks[im.ID] = append(p.marks[im.ID], oldRefs[r]...)
			}
		}
	}
	for _, rk := range perService {
		p.rollback = append(p.rollback, *rk)
	}
	sort.Slice(p.rollback, func(i, j int) bool { return p.rollback[i].ServiceName < p.rollback[j].ServiceName })

	for _, v := range u.Volumes {
		kind, project, ok := orphan(v, f.Prefixes)
		if !ok || v.RefCount != 0 {
			p.inUse.Count++
			p.inUse.Bytes += max(v.Bytes, 0)
			continue
		}
		p.volumes = append(p.volumes, OrphanVolume{Name: v.Name, Bytes: v.Bytes, CreatedAt: v.CreatedAt, Kind: kind, ServiceName: project})
	}
	sort.SliceStable(p.volumes, func(i, j int) bool { return p.volumes[i].Bytes > p.volumes[j].Bytes })
	return p
}

// panelRelease: 0 when refs aren't the panel's images, 1 a release to keep
// (the running one or the one before, or any while the state is unknown),
// 2 an older one.
func panelRelease(refs []string, keep map[string]bool, known bool) int {
	found := 0
	for _, r := range refs {
		repo := repoOf(r)
		if !strings.HasSuffix(repo, "/paas-backend") && !strings.HasSuffix(repo, "/paas-frontend") {
			continue
		}
		if !known || keep[tagOf(r)] {
			return 1
		}
		found = 2
	}
	return found
}

// builtBy is the service ID prefix of an image Toft built: paas/<id>:… for
// a git service, paas-…-<id8>-<service>:… for a compose build.
func builtBy(refs []string) (string, bool) {
	for _, r := range refs {
		repo := repoOf(r)
		if m := builtRepo.FindStringSubmatch(repo); m != nil {
			return m[1], true
		}
	}
	if id8, ok := composeBuiltBy(refs); ok {
		return id8, true
	}
	return "", false
}

var composeImage = regexp.MustCompile(`^paas-(?:.+-)?([0-9a-f]{8})-[a-z0-9][a-z0-9_.-]*$`)

func composeBuiltBy(refs []string) (string, bool) {
	for _, r := range refs {
		if m := composeImage.FindStringSubmatch(repoOf(r)); m != nil {
			return m[1], true
		}
	}
	return "", false
}

func composeBuilt(refs []string) bool {
	_, ok := composeBuiltBy(refs)
	return ok
}

// orphan reports whether no service owns v, and why.
func orphan(v Volume, prefixes map[string]bool) (VolumeKind, string, bool) {
	if m := serviceVol.FindStringSubmatch(v.Name); m != nil {
		return DeletedService, "", !prefixes[m[1]]
	}
	if proj := v.Labels["com.docker.compose.project"]; proj != "" && strings.HasPrefix(v.Name, proj+"_") {
		if m := projectName.FindStringSubmatch(proj); m != nil {
			return ComposeOrphan, proj, !prefixes[m[1]]
		}
		return "", "", false
	}
	if _, anon := v.Labels["com.docker.volume.anonymous"]; anon && anonVol.MatchString(v.Name) {
		return Anonymous, "", true
	}
	return "", "", false
}

// own reports whether an image with these refs belongs to a service of the
// workspace: a deployment of one went live with it, or one built it. An
// image no service is known to own might be another workspace's.
func (p plan) own(refs []string) bool {
	for _, r := range refs {
		if p.ownRefs[r] {
			return true
		}
	}
	id8, ok := builtBy(refs)
	_, own := p.named[id8]
	return ok && own
}

// example adds im to a class's examples: a build by its service's name,
// once (a deleted service's builds go unnamed), any other image by its
// first reference.
func (p plan) example(ex []string, k Kind, im Image) []string {
	refs := refsOf(im)
	if !tagged(im) {
		return ex
	}
	if p.shared && !p.own(refs) {
		return ex
	}
	if k == OldBuilds {
		id8, _ := builtBy(refs)
		name, ok := p.named[id8]
		if !ok || slices.Contains(ex, name) {
			return ex
		}
		return append(ex, name)
	}
	return append(ex, refs[0])
}

// classes summarises a plan's images, with the kinds the automatic clean
// takes marked.
func (p plan) classes(auto []Kind) []Class {
	isAuto := map[Kind]bool{}
	for _, k := range auto {
		isAuto[k] = true
	}
	out := make([]Class, 0, len(kindOrder))
	for _, k := range kindOrder {
		ims := p.byKind[k]
		c := Class{Kind: k, Removable: k.Removable(), Automatic: isAuto[k], Count: len(ims), Examples: make([]string, 0, 5)}
		for _, im := range ims {
			c.Bytes += im.unique()
			if len(c.Examples) < 5 {
				c.Examples = p.example(c.Examples, k, im)
			}
		}
		out = append(out, c)
	}
	return out
}
