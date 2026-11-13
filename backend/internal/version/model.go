package version

import (
	"github.com/Nickpap21/toft/backend/pkg/registry"
	"github.com/Nickpap21/toft/backend/pkg/scm"
)

// VersionInfo is the API response for GET /services/:id/versions.
type VersionInfo struct {
	SourceType string `json:"source_type"` // "git" | "docker-image"

	// Docker image fields
	CurrentImage  string              `json:"current_image,omitempty"`
	IsOutdated    bool                `json:"is_outdated"`
	LatestTag     string              `json:"latest_tag,omitempty"`
	Tags          []registry.TagEntry `json:"tags,omitempty"`
	RegistryError string              `json:"registry_error,omitempty"` // set when tag fetch fails

	// Git fields
	CurrentBranch string           `json:"current_branch,omitempty"`
	LatestCommit  *scm.CommitInfo  `json:"latest_commit,omitempty"`
	Branches      []scm.BranchInfo `json:"branches,omitempty"`
	GitTags       []scm.TagInfo    `json:"git_tags,omitempty"`
	// SCMError says why branches, tags or the latest commit are missing:
	// the provider refused or didn't answer.
	SCMError string `json:"scm_error,omitempty"`
}
