package issue

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"go.uber.org/zap"
)

// Issues, deeper (D-83): types, keys and their digits, parents and
// children, relations, clones, and the roll-ups that follow a status.

// ─── types ───────────────────────────────────────────────────────────────────

// types are the workspace's issue types, made from defaultTypes the first
// time it has none.
func (s *Service) types(ctx context.Context, orgID string) ([]Type, error) {
	all, err := s.repo.Types(ctx, orgID)
	if err != nil || len(all) > 0 {
		return all, err
	}
	for i, t := range defaultTypes {
		if _, err := s.repo.AddType(ctx, orgID, t, i); err != nil && err != ErrTypeTaken {
			return nil, err
		}
	}
	return s.repo.Types(ctx, orgID)
}

// fallback is the type an issue without one shows: the first standard type
// that isn't archived (Task until owners change it).
func fallback(types []Type) TypeRef {
	for _, t := range types {
		if t.Kind == "standard" && !t.Archived {
			return t.TypeRef
		}
	}
	if len(types) > 0 {
		return types[0].TypeRef
	}
	return TypeRef{Name: "Task", Kind: "standard", Icon: "task", Color: "blue"}
}

// typeOf is the type typeID names, or the fallback.
func typeOf(types []Type, typeID string) TypeRef {
	for _, t := range types {
		if t.ID == typeID {
			return t.TypeRef
		}
	}
	return fallback(types)
}

// usable checks typeID is a type the project may give an issue: the
// workspace's, not archived, not hidden by the project; "" is its default.
func (s *Service) usable(ctx context.Context, p *Project, types []Type, typeID string) (TypeRef, error) {
	if typeID == "" {
		typeID = p.DefaultTypeID
	}
	if typeID == "" {
		return fallback(types), nil
	}
	hidden, err := s.repo.HiddenTypes(ctx, p.ID)
	if err != nil {
		return TypeRef{}, err
	}
	for _, t := range types {
		if t.ID == typeID && !t.Archived && !slices.Contains(hidden, t.ID) {
			return t.TypeRef, nil
		}
	}
	return TypeRef{}, ErrBadType
}

func checkTypeName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if c := utf8.RuneCountInString(n); c < 1 || c > 40 || strings.IndexFunc(n, unicode.IsControl) >= 0 {
		return "", ErrBadTypeInput
	}
	return n, nil
}

// Types are the workspace's issue types.
func (s *Service) Types(ctx context.Context, orgID string) ([]Type, error) {
	return s.types(ctx, orgID)
}

// AddType adds an issue type (owners).
func (s *Service) AddType(ctx context.Context, orgID string, in TypeInput) (*Type, error) {
	all, err := s.types(ctx, orgID)
	if err != nil {
		return nil, err
	}
	name, err := checkTypeName(in.Name)
	if err != nil || !slices.Contains(typeKinds, in.Kind) || !slices.Contains(typeIcons, in.Icon) || !slices.Contains(typeColors, in.Color) {
		return nil, ErrBadTypeInput
	}
	tid, err := s.repo.AddType(ctx, orgID, TypeRef{Name: name, Kind: in.Kind, Icon: in.Icon, Color: in.Color}, len(all))
	if err != nil {
		return nil, err
	}
	return &Type{TypeRef: TypeRef{ID: tid, Name: name, Kind: in.Kind, Icon: in.Icon, Color: in.Color}, Position: len(all)}, nil
}

// UpdateType renames a type, changes its icon, colour or place, or archives
// it or brings it back (owners). The workspace keeps one standard type.
func (s *Service) UpdateType(ctx context.Context, orgID, typeID string, in TypeUpdate) (*Type, error) {
	all, err := s.types(ctx, orgID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(all, func(t Type) bool { return t.ID == typeID })
	if i < 0 {
		return nil, ErrNotFound
	}
	t := all[i]
	if in.Name != nil {
		if t.Name, err = checkTypeName(*in.Name); err != nil {
			return nil, err
		}
	}
	if in.Icon != nil {
		t.Icon = *in.Icon
	}
	if in.Color != nil {
		t.Color = *in.Color
	}
	if in.Position != nil {
		t.Position = *in.Position
	}
	if !slices.Contains(typeIcons, t.Icon) || !slices.Contains(typeColors, t.Color) {
		return nil, ErrBadTypeInput
	}
	if _, err := s.repo.UpdateType(ctx, orgID, t); err != nil {
		return nil, err
	}
	if in.Archived != nil && *in.Archived != t.Archived {
		if *in.Archived && lastStandard(all, t) {
			return nil, ErrLastType
		}
		if _, err := s.repo.SetTypeArchived(ctx, orgID, typeID, *in.Archived); err != nil {
			return nil, err
		}
		t.Archived = *in.Archived
	}
	return &t, nil
}

// lastStandard says whether t is the workspace's only standard type left.
func lastStandard(all []Type, t Type) bool {
	if t.Kind != "standard" {
		return false
	}
	for _, o := range all {
		if o.ID != t.ID && o.Kind == "standard" && !o.Archived {
			return false
		}
	}
	return true
}

// ArchiveType archives a type: issues keep it, new ones can't have it.
func (s *Service) ArchiveType(ctx context.Context, orgID, typeID string) error {
	archived := true
	_, err := s.UpdateType(ctx, orgID, typeID, TypeUpdate{Archived: &archived})
	return err
}

// ─── keys and settings ───────────────────────────────────────────────────────

// keys are the keys a project's issues answer to: its own and its former.
func (s *Service) keys(ctx context.Context, p *Project) ([]string, error) {
	aliases, err := s.repo.Aliases(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if p.Key == "" {
		return aliases, nil
	}
	return append([]string{p.Key}, aliases...), nil
}

// ProjectSettings are a project's key, digits and types.
func (s *Service) ProjectSettings(ctx context.Context, projectID string) (*ProjectSettings, error) {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	aliases, err := s.repo.Aliases(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	hidden, err := s.repo.HiddenTypes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := &ProjectSettings{version: listversion.Of(hidden), Key: p.Key, Aliases: aliases, Digits: p.Digits, WorkspaceDigits: p.OrgDigits,
		InheritDigits: p.OwnDigits == nil, HiddenTypeIDs: hidden, FailedDeployIssues: p.FailedDeployIssues}
	if p.DefaultTypeID != "" {
		out.DefaultTypeID = &p.DefaultTypeID
	}
	if p.Key != "" {
		out.NextKey = KeyOf(p.Key, p.NextNumber, p.Digits)
	}
	return out, nil
}

// SaveProjectSettings changes what in sends. A new key keeps the old one
// as an alias; the project keeps one standard type it uses.
func (s *Service) SaveProjectSettings(ctx context.Context, projectID string, in ProjectSettingsInput) (*ProjectSettings, error) {
	if in.HiddenTypeIDs != nil && in.Version == "" {
		return nil, listversion.ErrRequired
	}
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	newKey := ""
	if in.Key != nil && *in.Key != p.Key {
		key := strings.ToUpper(strings.TrimSpace(*in.Key))
		if !validKey.MatchString(key) {
			return nil, ErrBadKey
		}
		holder, err := s.repo.KeyHolder(ctx, p.OrgID, key)
		if err != nil {
			return nil, err
		}
		if holder != "" && holder != p.ID {
			return nil, ErrKeyTaken
		}
		if key != p.Key {
			newKey = key
		}
	}
	digits := p.OwnDigits
	if in.InheritDigits {
		digits = nil
	} else if in.Digits != nil {
		if *in.Digits < 0 || *in.Digits > 6 {
			return nil, ErrBadDigits
		}
		digits = in.Digits
	}
	types, err := s.types(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	hidden, err := s.repo.HiddenTypes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if in.HiddenTypeIDs != nil {
		hidden = []string{}
		for _, t := range *in.HiddenTypeIDs {
			if !slices.ContainsFunc(types, func(x Type) bool { return x.ID == t }) {
				return nil, ErrBadType
			}
			hidden = append(hidden, t)
		}
		if !slices.ContainsFunc(types, func(t Type) bool { return t.Kind == "standard" && !t.Archived && !slices.Contains(hidden, t.ID) }) {
			return nil, ErrLastType
		}
	}
	def := p.DefaultTypeID
	if in.DefaultTypeID != nil {
		def = *in.DefaultTypeID
	}
	if def != "" && (slices.Contains(hidden, def) || !slices.ContainsFunc(types, func(t Type) bool { return t.ID == def && !t.Archived && t.Kind != "subtask" })) {
		return nil, ErrBadType
	}
	// The list's check comes first: a stale one must leave the key as it was.
	version := ""
	if in.HiddenTypeIDs != nil {
		version = in.Version
	}
	if err := s.repo.SaveSettings(ctx, p.ID, digits, def, version, hidden); err != nil {
		return nil, err
	}
	if newKey != "" {
		if err := s.repo.ReplaceKey(ctx, p.OrgID, p.ID, p.Key, newKey); err != nil {
			return nil, err
		}
	}
	if in.FailedDeployIssues != nil {
		if err := s.repo.SetFailedDeployIssues(ctx, p.ID, *in.FailedDeployIssues); err != nil {
			return nil, err
		}
	}
	return s.ProjectSettings(ctx, p.ID)
}

// ─── parents ─────────────────────────────────────────────────────────────────

// parentFor resolves a parent's key for an issue of type t (selfID "" for
// a new issue): another issue of the workspace, not a sub-task, and not
// the issue itself or one of its descendants. A sub-task needs one. A
// project-scoped key's (keyProject) is in its project.
func (s *Service) parentFor(ctx context.Context, orgID, keyProject, selfID string, t TypeRef, key string, types []Type) (*RefRow, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		if t.Kind == "subtask" {
			return nil, ErrNeedsParent
		}
		return nil, nil
	}
	pid, err := s.byKey(ctx, orgID, key)
	if err != nil {
		return nil, err
	}
	for cur, depth := pid, 0; cur != ""; depth++ {
		if cur == selfID || depth > 20 {
			return nil, ErrBadParent
		}
		ref, err := s.repo.IssueRef(ctx, cur)
		if err != nil {
			return nil, err
		}
		if cur == pid && typeOf(types, ref.TypeID).Kind == "subtask" {
			return nil, ErrBadParent
		}
		cur = ref.ParentID
	}
	parent, err := s.repo.IssueRef(ctx, pid)
	if err == nil && keyProject != "" && parent.ProjectID != keyProject {
		return nil, ErrIssueNotFound
	}
	return parent, err
}

// byKey is the workspace's issue a key names (VLB-12, vlb-012, or a former
// key).
func (s *Service) byKey(ctx context.Context, orgID, key string) (string, error) {
	prefix, num, ok := strings.Cut(strings.ToUpper(strings.TrimSpace(key)), "-")
	n, err := strconv.Atoi(num)
	if !ok || err != nil || n <= 0 || !validKey.MatchString(prefix) {
		return "", ErrIssueNotFound
	}
	return s.repo.IssueByKey(ctx, orgID, prefix, n)
}

// ─── relations ───────────────────────────────────────────────────────────────

// relationKinds maps a kind, from the issue's side, to how it's stored:
// the stored kind and whether this issue is its from.
var relationKinds = map[string]struct {
	kind    string
	outward bool
}{
	"blocks":        {"blocks", true},
	"blocked_by":    {"blocks", false},
	"relates":       {"relates", true},
	"duplicates":    {"duplicates", true},
	"duplicated_by": {"duplicates", false},
	"clones":        {"clones", true},
	"cloned_by":     {"clones", false},
}

// sideOf is a stored relation's kind from one of its issues' side.
func sideOf(kind string, outward bool) string {
	switch {
	case kind == "relates":
		return "relates"
	case outward:
		return kind
	case kind == "blocks":
		return "blocked_by"
	case kind == "duplicates":
		return "duplicated_by"
	}
	return "cloned_by"
}

// Relate relates an issue to another of the workspace by key (D-83), of
// its project for a project-scoped key. A duplicate is cancelled; both
// issues' histories say so.
func (s *Service) Relate(ctx context.Context, projectID string, number int, actorID string, in RelationInput) (*Detail, error) {
	p, st, err := s.issueOf(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	k, ok := relationKinds[in.Kind]
	if !ok || in.Kind == "clones" || in.Kind == "cloned_by" {
		return nil, ErrBadRelation
	}
	otherID, err := s.byKey(ctx, p.OrgID, in.Issue)
	if err != nil {
		return nil, err
	}
	if otherID == st.ID {
		return nil, ErrBadRelation
	}
	other, err := s.repo.IssueRef(ctx, otherID)
	if err != nil {
		return nil, err
	}
	if in.KeyProject != "" && other.ProjectID != in.KeyProject {
		return nil, ErrIssueNotFound
	}
	from, to := st.ID, other.ID
	if !k.outward {
		from, to = to, from
	}
	added, err := s.repo.AddRelation(ctx, from, to, k.kind, actorID)
	if err != nil {
		return nil, err
	}
	if !added {
		return nil, ErrRelated
	}
	selfKey := KeyOf(p.Key, st.Number, p.Digits)
	otherKey := KeyOf(other.Key, other.Number, other.Digits)
	s.note(ctx, Event{IssueID: st.ID, Kind: "related", ActorID: actorID, From: in.Kind, To: otherKey})
	s.note(ctx, Event{IssueID: other.ID, Kind: "related", ActorID: actorID, From: sideOf(k.kind, !k.outward), To: selfKey})
	if k.kind == "duplicates" {
		// The duplicate is cancelled (D-83).
		if _, err := s.repo.Move(ctx, Move{From: openStatuses, To: StatusCancelled, Event: Event{IssueID: from, ActorID: actorID}}); err != nil {
			return nil, err
		}
		s.afterStatus(ctx, from, StatusCancelled)
	}
	return s.Get(ctx, projectID, number)
}

// openStatuses are those an issue can be cancelled or shipped from.
var openStatuses = []string{StatusBacklog, StatusTodo, StatusInProgress, StatusInReview}

// Unrelate removes a relation of the issue.
func (s *Service) Unrelate(ctx context.Context, projectID string, number int, actorID, relationID string) error {
	p, st, err := s.issueOf(ctx, projectID, number)
	if err != nil {
		return err
	}
	r, err := s.repo.GetRelation(ctx, st.ID, relationID)
	if err != nil {
		return err
	}
	other, err := s.repo.IssueRef(ctx, r.Other.ID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteRelation(ctx, r.ID); err != nil {
		return err
	}
	s.note(ctx, Event{IssueID: st.ID, Kind: "unrelated", ActorID: actorID, From: sideOf(r.Kind, r.Outward), To: KeyOf(other.Key, other.Number, other.Digits)})
	s.note(ctx, Event{IssueID: other.ID, Kind: "unrelated", ActorID: actorID, From: sideOf(r.Kind, !r.Outward), To: KeyOf(p.Key, st.Number, p.Digits)})
	return nil
}

// note writes a history row a person's action makes; a failure is logged,
// as the action itself succeeded.
func (s *Service) note(ctx context.Context, e Event) {
	if _, err := s.repo.Record(ctx, e); err != nil {
		s.log.Warn("write an issue's history", zap.Error(err), zap.String("issue_id", e.IssueID), zap.String("kind", e.Kind))
	}
}

// ─── clone ───────────────────────────────────────────────────────────────────

// Clone clones an issue into its project or another of the workspace,
// starting in Backlog, linked to the original (clones), with what in.Keep
// names: assignee, labels, relations, subtasks (one level).
func (s *Service) Clone(ctx context.Context, projectID string, number int, actorID string, in CloneInput) (*Detail, error) {
	src, st, err := s.issueOf(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	dst := src
	if in.ProjectID != "" && in.ProjectID != projectID {
		if dst, err = s.repo.Project(ctx, in.ProjectID); err != nil {
			return nil, err
		}
		if dst.OrgID != src.OrgID {
			return nil, ErrNotFound
		}
	}
	title := in.Title
	if strings.TrimSpace(title) == "" {
		title = st.Title
	}
	n, err := s.cloneOne(ctx, src, dst, st, title, st.ParentID, actorID, in.Keep)
	if err != nil {
		return nil, err
	}
	defer s.changed(dst.OrgID, dst.ID) // the clone exists, whatever follows
	if slices.Contains(in.Keep, "subtasks") {
		types, err := s.types(ctx, src.OrgID)
		if err != nil {
			return nil, err
		}
		kids, err := s.repo.Children(ctx, st.ID)
		if err != nil {
			return nil, err
		}
		clone, err := s.repo.Get(ctx, dst.ID, n)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			if typeOf(types, k.TypeID).Kind != "subtask" {
				continue
			}
			child, err := s.repo.Get(ctx, k.ProjectID, k.Number)
			if err != nil {
				return nil, err
			}
			kp, err := s.repo.Project(ctx, k.ProjectID)
			if err != nil {
				return nil, err
			}
			if _, err := s.cloneOne(ctx, kp, dst, child, child.Title, clone.ID, actorID, in.Keep); err != nil {
				return nil, err
			}
		}
	}
	return s.Get(ctx, dst.ID, n)
}

// cloneOne clones st (of project src) into dst under parentID.
func (s *Service) cloneOne(ctx context.Context, src, dst *Project, st *Stored, title, parentID, actorID string, keep []string) (int, error) {
	var err error
	if dst.Key, err = s.ensureKey(ctx, dst); err != nil {
		return 0, err
	}
	types, err := s.types(ctx, dst.OrgID)
	if err != nil {
		return 0, err
	}
	t, err := s.usable(ctx, dst, types, st.TypeID)
	if err != nil {
		t, err = s.usable(ctx, dst, types, "")
		if err != nil {
			return 0, err
		}
	}
	if t, err := checkTitle(title); err == nil {
		title = t
	} else {
		return 0, err
	}
	in := NewIssue{ProjectID: dst.ID, Title: title, Description: st.Description, Status: StatusBacklog, Priority: st.Priority,
		CreatedBy: actorID, TypeID: t.ID, ParentID: parentID}
	if slices.Contains(keep, "assignee") && st.AssigneeID != "" && s.assignable(ctx, dst.OrgID, st.AssigneeID) == nil {
		in.AssigneeID = st.AssigneeID
	}
	n, err := s.repo.Create(ctx, in)
	if err != nil {
		return 0, err
	}
	clone, err := s.repo.Get(ctx, dst.ID, n)
	if err != nil {
		return 0, err
	}
	if slices.Contains(keep, "labels") {
		if err := s.repo.CopyLabels(ctx, st.ID, clone.ID); err != nil {
			return 0, err
		}
	}
	if slices.Contains(keep, "relations") {
		if err := s.repo.CopyRelations(ctx, st.ID, clone.ID, actorID); err != nil {
			return 0, err
		}
	}
	if _, err := s.repo.AddRelation(ctx, clone.ID, st.ID, "clones", actorID); err != nil {
		return 0, err
	}
	s.note(ctx, Event{IssueID: clone.ID, Kind: "cloned", ActorID: actorID, From: "clones", To: KeyOf(src.Key, st.Number, src.Digits)})
	s.note(ctx, Event{IssueID: st.ID, Kind: "cloned", ActorID: actorID, From: "cloned_by", To: KeyOf(dst.Key, n, dst.Digits)})
	return n, nil
}

// ─── what follows a status ───────────────────────────────────────────────────

// afterStatus follows an issue moving to status: the issues it blocked
// say they no longer are once it ships or is cancelled, and its parent
// ships once every child is done (D-83). Failures are logged: the move
// itself happened. It then tells the workspace's tabs once: every move of an
// issue's status ends here.
func (s *Service) afterStatus(ctx context.Context, issueID, status string) {
	if err := s.follow(ctx, issueID, status, 0); err != nil {
		s.log.Warn("follow an issue's status", zap.Error(err), zap.String("issue_id", issueID))
	}
	if s.pub == nil {
		return
	}
	ref, err := s.repo.IssueRef(ctx, issueID)
	if err != nil {
		s.log.Warn("tell the workspace an issue moved", zap.Error(err), zap.String("issue_id", issueID))
		return
	}
	s.changed(ref.OrgID, ref.ProjectID)
}

func (s *Service) follow(ctx context.Context, issueID, status string, depth int) error {
	if depth > 10 {
		return nil
	}
	ref, err := s.repo.IssueRef(ctx, issueID)
	if err != nil {
		return err
	}
	if status == StatusShipped || status == StatusCancelled {
		blocked, err := s.repo.BlockedIssues(ctx, issueID)
		if err != nil {
			return err
		}
		for _, b := range blocked {
			s.note(ctx, Event{IssueID: b, Kind: "unblocked", Automatic: true, From: status, To: KeyOf(ref.Key, ref.Number, ref.Digits)})
		}
	}
	if ref.ParentID == "" {
		return nil
	}
	parent, err := s.repo.IssueRef(ctx, ref.ParentID)
	if err != nil {
		return err
	}
	types, err := s.types(ctx, parent.OrgID)
	if err != nil {
		return err
	}
	if typeOf(types, parent.TypeID).Kind != "container" {
		return nil
	}
	c, err := s.repo.ChildCounts(ctx, parent.ID)
	if err != nil {
		return err
	}
	if c.Total == 0 || c.Done < c.Total || c.Shipped == 0 {
		return nil
	}
	moved, err := s.repo.Move(ctx, Move{From: openStatuses, To: StatusShipped, Event: Event{IssueID: parent.ID, Automatic: true}})
	if err != nil || !moved {
		return err
	}
	return s.follow(ctx, parent.ID, StatusShipped, depth+1)
}
