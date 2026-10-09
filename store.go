package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// currentSchema is bumped whenever config.json changes shape. Reading an
// older file migrates it in place, after snapshotting the original.
const currentSchema = 5

// KV is one editable key/value row in the settings UI.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Release modes decide how a version reaches an environment.
const (
	// ModeRebuild builds each environment from source with its own build args.
	ModeRebuild = "rebuild"
	// ModePromote builds once in the source environment, then re-tags that
	// exact image for the others so the digest stays identical.
	ModePromote = "promote"
)

// Env is one environment inside a project: staging, production, ...
//
// Every field Env shares with Project is an override: left empty, the
// project's value applies. Project.Effective is the only place that knows
// this rule.
type Env struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TagPrefix string `json:"tagPrefix"`

	Registry string `json:"registry"`
	Image    string `json:"image"`

	// Context is NOT an override any more. Effective fills it in from the
	// project so everything downstream can keep reading env.Context, but no
	// environment stores one of its own.
	//
	// It used to be settable per environment, and that was a mistake: the
	// source folder is what makes a project a project. Two environments
	// building from two folders are two different projects wearing one name
	// — one history, one version table, different code.
	//
	// It also enabled nothing; it only let the path land one tier too low.
	// In the real config it had been used exactly once, and the result was a
	// project whose folder sat on its first environment while the project
	// itself had none — so the second environment inherited nothing and
	// could not build at all. migrateV4 lifts such a value up, and normalise
	// clears whatever is left.
	Context    string `json:"context,omitempty"`
	Dockerfile string `json:"dockerfile"`
	Platform   string `json:"platform"`
	Target     string `json:"target"`
	BuildArgs  []KV   `json:"buildArgs"`
	Labels     []KV   `json:"labels"`
	ExtraFlags string `json:"extraFlags"`

	// EnvFile is the path to an env file a custom build command can read via
	// {envfile} — for a script like build-and-push.sh that bakes
	// NEXT_PUBLIC_* values into the image at build time, so the file chosen
	// here is what decides which environment the resulting image serves.
	// DIMA itself never reads or ships this file; it only hands the path to
	// the command.
	EnvFile string `json:"envFile,omitempty"`

	// BuildCommand replaces `docker build` entirely when set — for folders
	// whose build is driven by a script. Empty means the normal path.
	BuildCommand string `json:"buildCommand"`

	// IsOnlyLocal marks an environment whose image never leaves this
	// machine: a build cache, a local tool image, anything built to be used
	// here and nowhere else.
	//
	// It belongs to the environment, not to the project. One project
	// routinely has a Cache environment that stays put right next to a
	// Staging that ships — that is the normal shape, not an edge case, and
	// a project-wide flag cannot express it at all.
	//
	// It is not an override of a project field: there is no project-level
	// counterpart to inherit from, so Effective passes it through unchanged.
	IsOnlyLocal bool `json:"isOnlyLocal,omitempty"`

	AutoPush    bool   `json:"autoPush"`
	LastVersion string `json:"lastVersion"`

	// LastTag is the pre-v2 name of LastVersion, read once during migration.
	LastTag string `json:"lastTag,omitempty"`
}

// Where a project came from. Origin is a record, not a permission: a
// folder-sync project stays editable like any other, and the folder sync
// only ever adds projects it has not seen before.
const (
	// OriginUser is a project the user created by hand — the only origin a
	// config written before schema 3 can have had.
	OriginUser = "user"
	// OriginFolderSync is a project a FolderRule discovered on disk.
	OriginFolderSync = "folder-sync"
)

// FolderRule watches one folder on disk and files the subfolders it finds
// into a group.
//
// The rule is keyed by Path, never by group name. Group only decides where
// a *newly discovered* folder lands; it is not a link back to the projects
// an earlier sync already created. That is what lets the user rename a
// group in the sidebar without the next sync resurrecting the old name —
// renaming a group is editing Project.Group, and nothing here reads it.
type FolderRule struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Group is the sidebar group new folders are filed into.
	Group string `json:"group"`
	// Registry is prefilled on the projects this rule creates, because a
	// folder of repos usually ships to one registry.
	Registry   string    `json:"registry"`
	LastSyncAt time.Time `json:"lastSyncAt"`
}

// Project groups environments that ship the same image and holds the
// settings they have in common.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Group is a plain label, not an entity with an identity of its own.
	// Projects sharing a name sit together in the sidebar; an empty one
	// means ungrouped. Renaming a group is editing this string, and a group
	// stops existing as soon as no project names it — nothing to clean up.
	Group       string `json:"group"`
	ReleaseMode string `json:"releaseMode"`
	SourceEnvID string `json:"sourceEnvId"`

	Registry   string `json:"registry"`
	Image      string `json:"image"`
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile"`
	Platform   string `json:"platform"`
	Target     string `json:"target"`
	BuildArgs  []KV   `json:"buildArgs"`
	Labels     []KV   `json:"labels"`
	ExtraFlags string `json:"extraFlags"`
	// EnvFile is the project-wide default for Env.EnvFile. See there.
	EnvFile string `json:"envFile,omitempty"`
	// BuildCommand replaces `docker build` for every environment that does
	// not override it. See Env.BuildCommand.
	BuildCommand string `json:"buildCommand"`

	// Origin records who created this project: OriginUser or
	// OriginFolderSync. Everything written before schema 3 is OriginUser.
	Origin string `json:"origin,omitempty"`
	// FolderPath is the folder a folder-sync project was discovered in. It
	// is kept separate from Context so a user who points the build at a
	// subfolder does not break the match on the next sync.
	FolderPath string `json:"folderPath,omitempty"`
	// FolderMissing is set by the sync when the folder is gone. The project
	// is flagged on screen, never deleted: the build history is the user's,
	// not the folder's, and a folder is missing for boring reasons too — an
	// unplugged drive, a branch switch, a repo moved one level up.
	FolderMissing bool `json:"folderMissing,omitempty"`
	// IsOnlyLocal is the pre-v4 name of Env.IsOnlyLocal, read once during
	// migration and then cleared.
	//
	// Putting it on the project was the wrong shape: a project has a Cache
	// environment AND a Staging one, and marking the whole project local
	// meant the cache could only be expressed by giving it a project of its
	// own. migrateV3 moves the flag down onto every environment, which
	// preserves the old meaning exactly — a project where every environment
	// is local-only is a local-only project.
	IsOnlyLocal bool `json:"isOnlyLocal,omitempty"`

	Envs []Env `json:"envs"`
}

func firstNonEmpty(override, base string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	return base
}

// mergeKV overlays rows on top of base, matching on key. Rows with an empty
// key are dropped. Order is base first, then keys only the overlay has, so
// the resulting command line is stable between runs.
func mergeKV(base, over []KV) []KV {
	out := []KV{}
	seen := map[string]int{}
	for _, kv := range base {
		if strings.TrimSpace(kv.Key) == "" {
			continue
		}
		seen[kv.Key] = len(out)
		out = append(out, kv)
	}
	for _, kv := range over {
		if strings.TrimSpace(kv.Key) == "" {
			continue
		}
		if i, ok := seen[kv.Key]; ok {
			out[i] = kv
			continue
		}
		seen[kv.Key] = len(out)
		out = append(out, kv)
	}
	return out
}

// Effective resolves an environment against its project. Every consumer
// (the builder, the API, the pull command) works with the result, never
// with the raw Env.
func (p Project) Effective(e Env) Env {
	out := e
	out.Registry = firstNonEmpty(e.Registry, p.Registry)
	out.Image = firstNonEmpty(e.Image, p.Image)
	// Not firstNonEmpty: the source folder belongs to the project alone.
	out.Context = p.Context
	out.Dockerfile = firstNonEmpty(e.Dockerfile, p.Dockerfile)
	out.Platform = firstNonEmpty(e.Platform, p.Platform)
	out.Target = firstNonEmpty(e.Target, p.Target)
	out.EnvFile = firstNonEmpty(e.EnvFile, p.EnvFile)
	out.BuildCommand = firstNonEmpty(e.BuildCommand, p.BuildCommand)
	out.BuildArgs = mergeKV(p.BuildArgs, e.BuildArgs)
	out.Labels = mergeKV(p.Labels, e.Labels)
	// Flags are additive by nature, so the environment's are appended to the
	// project's rather than replacing them.
	out.ExtraFlags = strings.TrimSpace(strings.TrimSpace(p.ExtraFlags) + " " + strings.TrimSpace(e.ExtraFlags))
	return out
}

// BuildEnvID is the environment a build may start in. Under ModePromote a
// project has exactly one; that single source is what makes "the image on
// production is the image we tested" true.
func (p Project) BuildEnvID() string {
	if p.ReleaseMode != ModePromote {
		return ""
	}
	for _, e := range p.Envs {
		if e.ID == p.SourceEnvID && !e.IsOnlyLocal {
			return e.ID
		}
	}
	// The fallback skips local-only environments. Promote re-tags an image
	// on the registry, so a source whose image never reaches one would leave
	// every other environment with nothing to point at.
	for _, e := range p.Envs {
		if !e.IsOnlyLocal {
			return e.ID
		}
	}
	return ""
}

// CanBuild reports whether a build may start in this environment.
func (p Project) CanBuild(envID string) bool {
	if p.ReleaseMode != ModePromote {
		return true
	}
	// A local-only environment stands outside the promote chain: nothing
	// promotes into it and nothing promotes out of it, so the only way it
	// ever gets an image is by building its own. Refusing that would make
	// the environment permanently empty.
	for _, e := range p.Envs {
		if e.ID == envID && e.IsOnlyLocal {
			return true
		}
	}
	return p.BuildEnvID() == envID
}

// Tag is the registry tag an environment gives a project version.
func (e Env) Tag(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "latest"
	}
	return strings.TrimSpace(e.TagPrefix) + v
}

// Repo is the image reference without a tag. Call it on an effective Env.
func (e Env) Repo() string {
	name := strings.Trim(strings.TrimSpace(e.Image), "/")
	if name == "" {
		name = "image"
	}
	if reg := strings.Trim(strings.TrimSpace(e.Registry), "/"); reg != "" {
		name = reg + "/" + name
	}
	return name
}

// Ref is the full image reference for a project version.
func (e Env) Ref(version string) string {
	return e.Repo() + ":" + e.Tag(version)
}

type Config struct {
	SchemaVersion int       `json:"schemaVersion"`
	Projects      []Project `json:"projects"`
	DockerBin     string    `json:"dockerBin"`
	// FolderGroups are the watched folders, keyed by FolderRule.Path.
	FolderGroups []FolderRule `json:"folderGroups"`
}

// FindEnv locates an environment and the project that owns it.
func (c Config) FindEnv(envID string) (Project, Env, bool) {
	for _, p := range c.Projects {
		for _, e := range p.Envs {
			if e.ID == envID {
				return p, e, true
			}
		}
	}
	return Project{}, Env{}, false
}

func (c Config) FindProject(id string) (Project, bool) {
	for _, p := range c.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// Build kinds. A promote produces the same record shape as a build, so the
// history, the logs and the matrix all have one code path.
const (
	KindBuild   = "build"
	KindPromote = "promote"
	// KindImport is a tag that already existed on the machine, read in
	// rather than produced by DIMA.
	KindImport = "import"
)

type Build struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	EnvID       string `json:"envId"`
	EnvName     string `json:"envName"`

	Version string `json:"version"` // project version, e.g. 1.2.3
	Tag     string `json:"tag"`     // what is actually on the registry
	Ref     string `json:"ref"`
	Digest  string `json:"digest"`

	Kind          string `json:"kind"`
	SourceBuildID string `json:"sourceBuildId,omitempty"`

	// Where the source came from. A promote copies these from the build it
	// re-tags, because it ships that same image: the branch that produced
	// the bytes does not change when a second tag is added.
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`

	// OnlyLocal records that this image was built to stay on this machine.
	// It is a snapshot, like Tag and Ref: whether an image was meant to
	// leave is a fact about the build, and keeping it here means renaming or
	// deleting the environment later cannot turn an old local-only build
	// into something the UI offers a Push button for.
	OnlyLocal bool `json:"onlyLocal,omitempty"`

	Status     string    `json:"status"` // running | success | failed | canceled
	Pushed     bool      `json:"pushed"`
	Command    string    `json:"command"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Err        string    `json:"err"`
}

type history struct {
	Builds []Build `json:"builds"`
}

// Store owns every file under ~/.dima and serialises access to them.
type Store struct {
	mu      sync.RWMutex
	root    string
	config  Config
	builds  []Build
	logs    map[string]*logBuffer
	cancels map[string]func()

	buildxOnce sync.Once
	buildxOK   bool

	// Cached answer from `docker version`, refreshed by DockerVersion.
	dockerVersion   string
	dockerOK        bool
	dockerCheckedAt time.Time
}

func defaultConfig() Config {
	staging := Env{ID: newID(), Name: "Staging", TagPrefix: "staging-",
		BuildArgs: []KV{{Key: "NODE_ENV", Value: "staging"}}, Labels: []KV{}}
	prod := Env{ID: newID(), Name: "Production", TagPrefix: "prod-",
		BuildArgs: []KV{{Key: "NODE_ENV", Value: "production"}}, Labels: []KV{}}
	return Config{
		SchemaVersion: currentSchema,
		DockerBin:     "docker",
		FolderGroups:  []FolderRule{},
		Projects: []Project{{
			ID: newID(), Name: "Dự án đầu tiên", ReleaseMode: ModeRebuild,
			Origin: OriginUser,
			Image:  "my-api", Dockerfile: "Dockerfile",
			BuildArgs: []KV{}, Labels: []KV{},
			Envs: []Env{staging, prod},
		}},
	}
}

func OpenStore(root string) (*Store, error) {
	for _, d := range []string{root, filepath.Join(root, "versions"), filepath.Join(root, "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	s := &Store{
		root:    root,
		logs:    map[string]*logBuffer{},
		cancels: map[string]func(){},
	}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	s.loadHistory()
	s.migrateHistory()
	return s, nil
}

func (s *Store) configPath() string  { return filepath.Join(s.root, "config.json") }
func (s *Store) historyPath() string { return filepath.Join(s.root, "history.json") }
func (s *Store) logPath(id string) string {
	return filepath.Join(s.root, "logs", id+".log")
}

// ---------- config ----------

// legacyConfig is the v1 shape: a flat list of environments, no projects.
type legacyConfig struct {
	SchemaVersion int    `json:"schemaVersion"`
	Environments  []Env  `json:"environments"`
	DockerBin     string `json:"dockerBin"`
}

// decodeConfig reads either schema and reports whether the bytes had to be
// migrated, so the caller can snapshot the original before overwriting it.
func decodeConfig(raw []byte) (Config, bool, error) {
	// Notepad and PowerShell write UTF-8 with a byte order mark, and the
	// config file is meant to be editable by hand on Windows.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	var probe struct {
		SchemaVersion int               `json:"schemaVersion"`
		Projects      []json.RawMessage `json:"projects"`
		Environments  []json.RawMessage `json:"environments"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Config{}, false, fmt.Errorf("config.json không đọc được: %w", err)
	}
	if probe.Projects == nil && probe.Environments != nil {
		var old legacyConfig
		if err := json.Unmarshal(raw, &old); err != nil {
			return Config{}, false, fmt.Errorf("config.json không đọc được: %w", err)
		}
		return migrateV1(old), true, nil
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, false, fmt.Errorf("config.json không đọc được: %w", err)
	}
	stale := c.SchemaVersion < currentSchema
	if stale {
		// v2, v3 and v4 share one Go shape — later versions only added
		// fields, and v4's one move keeps the old field for reading — so the
		// same json.Unmarshal reads them all and each migration is a pass
		// over the decoded value rather than a second decoder.
		//
		// migrateV4 runs FIRST, out of version order, and that is deliberate:
		// it rescues the per-environment source folder, and every other step
		// ends by calling normalise, which is what clears that value. Reading
		// it after normalise would read nothing. The lift is harmless at any
		// schema version, so running it early costs nothing.
		c = migrateV4(c)
		if c.SchemaVersion < 3 {
			c = migrateV2(c)
		}
		c = migrateV3(c)
	}
	return normalise(c), stale, nil
}

// migrateV4 brings a v4 config up to v5 by moving the source folder off the
// environments and onto the project, where it belongs.
//
// Only fills a project that has none: a project that already names its own
// folder keeps it, and an environment's stray value is dropped by normalise
// rather than overruling the project. Dropping is right because Effective
// stopped reading it — keeping it would leave a setting on disk that changes
// nothing and confuses everyone who reads the file.
//
// This does not just tidy: the one project in the wild that had used the
// field had its folder on the first environment and nothing on the project,
// so its second environment had no source at all and was refused at build
// time. Lifting the value repairs it.
//
// Unlike the other steps it does not call normalise — see the ordering note
// in decodeConfig.
func migrateV4(c Config) Config {
	for i := range c.Projects {
		p := &c.Projects[i]
		if strings.TrimSpace(p.Context) != "" {
			continue
		}
		for j := range p.Envs {
			if strings.TrimSpace(p.Envs[j].Context) != "" {
				p.Context = p.Envs[j].Context
				break
			}
		}
	}
	return c
}

// migrateV3 brings a v3 config up to v4 by moving the local-only flag from
// the project down onto each of its environments.
//
// The meaning is preserved exactly: before v4 the flag applied to everything
// the project built, so setting it on every environment says the same thing.
// What v4 adds is the ability to say less than that — one Cache environment
// local, the rest shipping normally — which is what the flag was wanted for
// in the first place.
//
// It runs on the same road migrateV1 and migrateV2 do: decodeConfig reports
// the file as migrated, loadConfig snapshots the original into versions/ and
// only then overwrites it, so the v3 file stays one RestoreSnapshot away.
func migrateV3(c Config) Config {
	for i := range c.Projects {
		p := &c.Projects[i]
		if !p.IsOnlyLocal {
			continue
		}
		for j := range p.Envs {
			p.Envs[j].IsOnlyLocal = true
		}
		p.IsOnlyLocal = false
	}
	return normalise(c)
}

// migrateV2 brings a v2 config up to v3. The step is purely additive: no
// field is renamed, moved or dropped, so every environment keeps the exact
// effective values it had. Projects that existed before folder sync did are
// the user's own, and no folder is watched yet.
//
// It runs on the same road migrateV1 does — decodeConfig reports the file
// as migrated, loadConfig snapshots the original into versions/ and only
// then overwrites it — so the v2 file stays one RestoreSnapshot away.
func migrateV2(c Config) Config {
	for i := range c.Projects {
		if strings.TrimSpace(c.Projects[i].Origin) == "" {
			c.Projects[i].Origin = OriginUser
		}
	}
	if c.FolderGroups == nil {
		c.FolderGroups = []FolderRule{}
	}
	return normalise(c)
}

// migrateV1 folds the old flat environment list into a single project and
// lifts every setting the environments already agreed on, so the effective
// configuration is unchanged but the inheritance is visible.
func migrateV1(old legacyConfig) Config {
	p := Project{
		ID: newID(), Name: "Dự án của tôi", ReleaseMode: ModeRebuild,
		Origin:    OriginUser,
		BuildArgs: []KV{}, Labels: []KV{},
		Envs: old.Environments,
	}
	for i := range p.Envs {
		if p.Envs[i].ID == "" {
			p.Envs[i].ID = newID()
		}
		if p.Envs[i].LastVersion == "" {
			p.Envs[i].LastVersion = p.Envs[i].LastTag
		}
		p.Envs[i].LastTag = ""
	}
	liftCommon(&p)
	return normalise(Config{
		SchemaVersion: currentSchema,
		DockerBin:     old.DockerBin,
		Projects:      []Project{p},
	})
}

// liftCommon moves every string setting the environments all share up to
// the project and clears it on the environments. The effective value of
// each environment is identical afterwards.
func liftCommon(p *Project) {
	if len(p.Envs) < 2 {
		return
	}
	fields := []struct {
		proj func(*Project) *string
		env  func(*Env) *string
	}{
		{func(p *Project) *string { return &p.Registry }, func(e *Env) *string { return &e.Registry }},
		{func(p *Project) *string { return &p.Image }, func(e *Env) *string { return &e.Image }},
		{func(p *Project) *string { return &p.Context }, func(e *Env) *string { return &e.Context }},
		{func(p *Project) *string { return &p.Dockerfile }, func(e *Env) *string { return &e.Dockerfile }},
		{func(p *Project) *string { return &p.Platform }, func(e *Env) *string { return &e.Platform }},
		{func(p *Project) *string { return &p.Target }, func(e *Env) *string { return &e.Target }},
		{func(p *Project) *string { return &p.EnvFile }, func(e *Env) *string { return &e.EnvFile }},
		{func(p *Project) *string { return &p.ExtraFlags }, func(e *Env) *string { return &e.ExtraFlags }},
	}
	for _, f := range fields {
		first := *f.env(&p.Envs[0])
		if strings.TrimSpace(first) == "" {
			continue
		}
		same := true
		for i := range p.Envs {
			if *f.env(&p.Envs[i]) != first {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		*f.proj(p) = first
		for i := range p.Envs {
			*f.env(&p.Envs[i]) = ""
		}
	}
}

// normalise fills in the defaults and identifiers a hand-edited or restored
// config.json may be missing.
func normalise(c Config) Config {
	if strings.TrimSpace(c.DockerBin) == "" {
		c.DockerBin = "docker"
	}
	c.SchemaVersion = currentSchema
	for i := range c.Projects {
		p := &c.Projects[i]
		if p.ID == "" {
			p.ID = newID()
		}
		p.Group = strings.TrimSpace(p.Group)
		// A project read from a file written before schema 3, or typed in by
		// hand, has no origin. It was not created by a folder sync.
		if p.Origin != OriginFolderSync {
			p.Origin = OriginUser
		}
		p.FolderPath = strings.TrimSpace(p.FolderPath)
		if p.ReleaseMode != ModePromote {
			p.ReleaseMode = ModeRebuild
		}
		if p.BuildArgs == nil {
			p.BuildArgs = []KV{}
		}
		if p.Labels == nil {
			p.Labels = []KV{}
		}
		if p.Envs == nil {
			p.Envs = []Env{}
		}
		// normalise does not invent environments. It used to, for a
		// local-only project, because the UI hid the environment layer for
		// those and something had to hang the builds on. Now that local-only
		// is a property of an environment rather than of a project, there is
		// no such case left: a project with no environments is simply one
		// the user has not finished setting up, and inventing one would put
		// a row in their list they never asked for.
		for j := range p.Envs {
			e := &p.Envs[j]
			if e.ID == "" {
				e.ID = newID()
			}
			if e.LastVersion == "" && e.LastTag != "" {
				e.LastVersion = e.LastTag
			}
			e.LastTag = ""
			// The source folder lives on the project. Clearing it here is
			// what keeps a value from surviving in the file while Effective
			// ignores it — data that is read by nobody but still on disk is
			// the shape of every lie this app has had to fix.
			e.Context = ""
			if e.BuildArgs == nil {
				e.BuildArgs = []KV{}
			}
			if e.Labels == nil {
				e.Labels = []KV{}
			}
		}
		// A promote project must point at an environment that exists.
		if p.ReleaseMode == ModePromote {
			p.SourceEnvID = p.BuildEnvID()
		} else {
			p.SourceEnvID = ""
		}
	}
	if c.Projects == nil {
		c.Projects = []Project{}
	}
	c.FolderGroups = normaliseFolderRules(c.FolderGroups)
	return c
}

// normaliseFolderRules cleans the watched-folder list. A rule without a
// path watches nothing, and two rules on the same folder would sync it
// twice — the path is the rule's identity, so the first one wins and the
// duplicate is dropped rather than merged into it.
func normaliseFolderRules(rules []FolderRule) []FolderRule {
	out := []FolderRule{}
	seen := map[string]bool{}
	for _, r := range rules {
		r.Path = strings.TrimSpace(r.Path)
		r.Group = strings.TrimSpace(r.Group)
		r.Registry = strings.TrimSpace(r.Registry)
		if r.Path == "" {
			continue
		}
		if seen[folderKey(r.Path)] {
			continue
		}
		seen[folderKey(r.Path)] = true
		if r.ID == "" {
			r.ID = newID()
		}
		out = append(out, r)
	}
	return out
}

// folderKey is the identity of a watched folder: the same folder typed two
// ways has to compare equal. It is deliberately not filepath.Clean, because
// config.json is portable — a file written on Windows is read back by a
// Linux build and by the tests, and filepath only knows the rules of the OS
// it was compiled for.
//
// Case is folded, which is right on Windows and macOS and slightly wrong on
// Linux, where two folders may differ only in case. The trade is one-sided:
// the cost there is refusing a second rule nobody writes, while the cost of
// not folding is `D:\work` and `d:\work\` syncing the same folder twice and
// creating every project in it twice over.
func folderKey(path string) string {
	k := strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	// A trailing separator is how the OS folder picker hands back a drive
	// root, so it says nothing about which folder is meant.
	for len(k) > 1 && strings.HasSuffix(k, "/") {
		k = strings.TrimSuffix(k, "/")
	}
	return strings.ToLower(k)
}

func (s *Store) loadConfig() error {
	raw, err := os.ReadFile(s.configPath())
	if os.IsNotExist(err) {
		s.config = defaultConfig()
		return s.writeConfigFile(s.config)
	}
	if err != nil {
		return err
	}
	c, migrated, err := decodeConfig(raw)
	if err != nil {
		// A file cut in half by a crash must not lock the user out of the
		// app: versions/ normally still holds a readable copy.
		return s.recoverConfigLocked(raw, err)
	}
	s.config = c
	if migrated {
		s.snapshotConfigLocked()
		return s.writeConfigFile(c)
	}
	return nil
}

// recoverConfigLocked rebuilds config.json from the newest snapshot that
// still decodes. Falling back to defaultConfig() would look like a recovery
// while quietly throwing every project away, and the first save afterwards
// would make that loss permanent — so an unrecoverable file stays an error.
// Callers run before the store is shared, like the rest of loadConfig.
func (s *Store) recoverConfigLocked(broken []byte, cause error) error {
	// Kept the way loadHistory keeps an unreadable history.json: the file
	// about to be replaced may be the only copy of a hand edit, and it is
	// also the evidence needed to explain what happened.
	name := fmt.Sprintf("config-loi-%s.json", time.Now().Format("20060102-150405"))
	_ = writeFileAtomic(filepath.Join(s.root, "versions", name), broken, 0o644)

	// Snapshots() already leaves out the config-loi-* copy written just
	// above, so this walks only files that were meant to be restorable.
	for _, snap := range s.Snapshots() {
		raw, err := s.ReadSnapshot(snap.File)
		if err != nil {
			continue
		}
		c, _, err := decodeConfig(raw)
		if err != nil {
			continue
		}
		s.config = c
		return s.writeConfigFile(c)
	}
	return fmt.Errorf("%w — không bản lưu nào trong versions/ đọc được, bản hỏng đã giữ ở versions/%s", cause, name)
}

// writeFileAtomic replaces path in a single step, so an interrupted write
// leaves either the whole old file or the whole new one — never a truncated
// one that stops the app from starting. The temp file has to live in the
// same directory: os.Rename is only atomic inside one filesystem.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// Without the sync the rename can reach the disk before the bytes do,
	// which is exactly the power-loss case this function exists for.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) writeConfigFile(c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.configPath(), append(raw, '\n'), 0o644)
}

func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// snapshotConfigLocked copies the config file as it is on disk into the
// versions folder. Callers hold the lock, or run before the store is shared.
func (s *Store) snapshotConfigLocked() {
	old, err := os.ReadFile(s.configPath())
	if err != nil {
		return
	}
	name := fmt.Sprintf("config-%s.json", time.Now().Format("20060102-150405"))
	_ = writeFileAtomic(filepath.Join(s.root, "versions", name), old, 0o644)
}

// writeConfigLocked normalises and writes without leaving a snapshot behind.
// Callers hold the lock.
func (s *Store) writeConfigLocked(c Config) error {
	c = normalise(c)
	if err := s.writeConfigFile(c); err != nil {
		return err
	}
	s.config = c
	return nil
}

// saveConfigLocked snapshots, normalises and writes in one turn of the lock.
// It is separate from SaveConfig so a caller that already edited s.config
// under the lock can save without releasing it: a save built from a snapshot
// taken before an unlock silently undoes whatever was written during the gap.
func (s *Store) saveConfigLocked(c Config) error {
	s.snapshotConfigLocked()
	return s.writeConfigLocked(c)
}

// SaveConfig snapshots the current file before overwriting it, so every
// settings change is recoverable from the Versions list.
func (s *Store) SaveConfig(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveConfigLocked(c)
}

type Snapshot struct {
	File    string    `json:"file"`
	SavedAt time.Time `json:"savedAt"`
	Size    int64     `json:"size"`
}

func (s *Store) Snapshots() []Snapshot {
	entries, _ := os.ReadDir(filepath.Join(s.root, "versions"))
	out := []Snapshot{}
	for _, e := range entries {
		// Only config snapshots are restorable; the folder also holds the
		// history backup taken during a schema migration.
		if e.IsDir() || !strings.HasPrefix(e.Name(), "config-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// config-loi-*.json is a config.json that did not decode, kept as
		// evidence. It carries the config- prefix and is the newest file in
		// the folder, so it would sit at the top of the restore list and
		// answer every click with "bản lưu hỏng". The file stays on disk —
		// it is what explains the failure — it just is not offered as a way
		// back.
		if strings.HasPrefix(e.Name(), "config-loi-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Snapshot{File: e.Name(), SavedAt: info.ModTime(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out
}

func (s *Store) ReadSnapshot(name string) ([]byte, error) {
	if name != filepath.Base(name) {
		return nil, fmt.Errorf("tên file không hợp lệ")
	}
	return os.ReadFile(filepath.Join(s.root, "versions", name))
}

func (s *Store) RestoreSnapshot(name string) (Config, error) {
	raw, err := s.ReadSnapshot(name)
	if err != nil {
		return Config{}, err
	}
	c, _, err := decodeConfig(raw)
	if err != nil {
		return Config{}, fmt.Errorf("bản lưu hỏng: %w", err)
	}
	if err := s.SaveConfig(c); err != nil {
		return Config{}, err
	}
	return s.Config(), nil
}

// ---------- history ----------

func (s *Store) loadHistory() {
	raw, err := os.ReadFile(s.historyPath())
	if err != nil {
		s.builds = []Build{}
		return
	}
	var h history
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &h); err != nil {
		// Starting empty would overwrite the unreadable file on the next
		// build, so keep a copy of it first.
		name := fmt.Sprintf("history-loi-%s.json", time.Now().Format("20060102-150405"))
		_ = writeFileAtomic(filepath.Join(s.root, "versions", name), raw, 0o644)
		s.builds = []Build{}
		return
	}
	for i := range h.Builds {
		// A build left "running" by a crash or a quit is not running now.
		if h.Builds[i].Status == "running" {
			h.Builds[i].Status = "canceled"
			h.Builds[i].Err = "ứng dụng đã thoát khi build đang chạy"
		}
	}
	s.builds = h.Builds
}

// migrateHistory backfills the fields added in schema 2. Old records carry
// a tag but no version; before projects existed there were no tag prefixes,
// so the tag is the version.
func (s *Store) migrateHistory() {
	changed := false
	for i := range s.builds {
		b := &s.builds[i]
		if b.Kind == "" {
			b.Kind = KindBuild
			changed = true
		}
		if b.Version == "" && b.Tag != "" {
			b.Version = b.Tag
			changed = true
		}
		if b.ProjectID == "" {
			if p, _, ok := s.config.FindEnv(b.EnvID); ok {
				b.ProjectID, b.ProjectName = p.ID, p.Name
				changed = true
			}
		}
	}
	if !changed {
		return
	}
	if old, err := os.ReadFile(s.historyPath()); err == nil {
		name := fmt.Sprintf("history-%s.json", time.Now().Format("20060102-150405"))
		_ = writeFileAtomic(filepath.Join(s.root, "versions", name), old, 0o644)
	}
	s.persistHistoryLocked()
}

func (s *Store) persistHistoryLocked() {
	raw, err := json.MarshalIndent(history{Builds: s.builds}, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(s.historyPath(), append(raw, '\n'), 0o644)
}

// Builds returns the history newest first.
func (s *Store) Builds() []Build {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Build, len(s.builds))
	copy(out, s.builds)
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

func (s *Store) Build(id string) (Build, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.builds {
		if b.ID == id {
			return b, true
		}
	}
	return Build{}, false
}

func (s *Store) addBuild(b Build) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.builds = append(s.builds, b)
	s.persistHistoryLocked()
}

func (s *Store) updateBuild(id string, fn func(*Build)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.builds {
		if s.builds[i].ID == id {
			fn(&s.builds[i])
			s.persistHistoryLocked()
			return
		}
	}
}

func (s *Store) DeleteBuild(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.builds[:0]
	for _, b := range s.builds {
		if b.ID != id {
			kept = append(kept, b)
		}
	}
	s.builds = kept
	delete(s.logs, id)
	s.persistHistoryLocked()
	_ = os.Remove(s.logPath(id))
}

// rememberVersion stores the version last shipped to an environment so the
// build dialog can offer the next one.
func (s *Store) rememberVersion(envID, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.config.Projects {
		for j := range s.config.Projects[i].Envs {
			if s.config.Projects[i].Envs[j].ID == envID {
				s.config.Projects[i].Envs[j].LastVersion = version
				changed = true
			}
		}
	}
	if !changed {
		return
	}
	// writeConfigLocked, not SaveConfig: SaveConfig takes the lock itself and
	// would deadlock here — and the whole point of holding the lock across
	// the edit and the write is that s.config is the truth while we hold it,
	// so nothing written meanwhile can be lost.
	//
	// And not saveConfigLocked either, because there is nothing worth
	// snapshotting. A snapshot exists so the user can undo a settings change;
	// this write changes one lastVersion, which the next build changes again.
	// Ten builds a day would bury the real settings snapshots under three
	// hundred files a month and make the Versions list useless — the opposite
	// of recoverable. SaveConfig still snapshots: that one is a user edit,
	// and it is the one they may want back.
	_ = s.writeConfigLocked(s.config)
}

// HasRunningBuild reports whether any build or promote is still in flight.
func (s *Store) HasRunningBuild() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.builds {
		if b.Status == "running" {
			return true
		}
	}
	return false
}
