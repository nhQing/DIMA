package main

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// ImageRepo is one repository found on this machine, with its tags, shaped
// the way a project would be.
type ImageRepo struct {
	Repository string `json:"repository"` // harbor.tech/mm/mai-money-website
	Registry   string `json:"registry"`   // harbor.tech
	Image      string `json:"image"`      // mm/mai-money-website
	Name       string `json:"name"`       // mai-money-website
	// Private marks a repository served by a named registry rather than
	// Docker Hub. Those are almost always the user's own work, so the import
	// screen ticks them by default and leaves postgres and friends alone.
	Private bool       `json:"private"`
	Tags    []ImageTag `json:"tags"`
	// Prefixes are the environment prefixes seen across the version tags,
	// e.g. "staging-" and "prod-". Empty when the tags carry no prefix.
	Prefixes []string `json:"prefixes"`
	Age      string   `json:"age"` // of the newest tag
}

type ImageTag struct {
	Tag     string `json:"tag"`
	Prefix  string `json:"prefix"`
	Version string `json:"version"`
	// IsVersion is false for tags like latest, cache or local, which name a
	// role rather than a release.
	IsVersion bool      `json:"isVersion"`
	ID        string    `json:"id"`
	Size      string    `json:"size"`
	Age       string    `json:"age"`
	CreatedAt time.Time `json:"createdAt"`
}

// splitRepository separates the registry host from the image name.
//
// The first segment is a registry only when it looks like a host: it has a
// dot or a port, or it is localhost. That is the same rule docker itself
// uses, and it is why moby/buildkit is an image on Docker Hub while
// harbor.tech/mm/app is an image on a private registry.
func splitRepository(repo string) (registry, image string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		return parts[0], parts[1]
	}
	return "", repo
}

// tagPattern splits a tag like "staging-1.2.3" into its prefix and version.
var tagPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*[-_])?(.*)$`)

// classifyTag decides whether a tag names a release, and splits off the
// environment prefix if it has one. A release has to contain a digit:
// latest, cache, stable and local do not.
func classifyTag(tag string) (prefix, version string, isVersion bool) {
	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return "", tag, false
	}
	prefix, version = m[1], m[2]
	if version == "" {
		prefix, version = "", tag
	}
	if !strings.ContainsAny(version, "0123456789") {
		return "", tag, false
	}
	return prefix, version, true
}

// classifyTagLocalOnly is classifyTag's counterpart for an environment
// marked IsOnlyLocal. Such an environment never reaches a registry, so tags
// like "cache" and "local" are not placeholders waiting for a real release —
// they ARE the releases, invented by whatever local workflow produced them.
// Requiring a digit there is exactly what made an imported cache environment
// read as "chưa có bản nào" forever, since none of its tags would ever pass
// that rule.
//
// Every tag is a version and none carries a prefix: the prefix exists to keep
// environments apart on a shared registry, and this image is not going to one.
func classifyTagLocalOnly(tag string) (prefix, version string, isVersion bool) {
	if strings.TrimSpace(tag) == "" {
		return "", tag, false
	}
	return "", tag, true
}

// GroupImages folds a flat image list into one entry per repository,
// newest first.
func GroupImages(images []LocalImage) []ImageRepo {
	byRepo := map[string]*ImageRepo{}
	order := []string{}

	for _, img := range images {
		r, ok := byRepo[img.Repository]
		if !ok {
			reg, name := splitRepository(img.Repository)
			short := name
			if i := strings.LastIndex(short, "/"); i >= 0 {
				short = short[i+1:]
			}
			r = &ImageRepo{
				Repository: img.Repository, Registry: reg, Image: name,
				Name: short, Private: reg != "", Age: img.Age,
				Tags: []ImageTag{}, Prefixes: []string{},
			}
			byRepo[img.Repository] = r
			order = append(order, img.Repository)
		}
		prefix, version, isVersion := classifyTag(img.Tag)
		r.Tags = append(r.Tags, ImageTag{
			Tag: img.Tag, Prefix: prefix, Version: version, IsVersion: isVersion,
			ID: img.ID, Size: img.Size, Age: img.Age, CreatedAt: img.CreatedAt,
		})
	}

	out := make([]ImageRepo, 0, len(order))
	for _, name := range order {
		r := byRepo[name]
		seen := map[string]bool{}
		for _, t := range r.Tags {
			if t.IsVersion && t.Prefix != "" && !seen[t.Prefix] {
				seen[t.Prefix] = true
				r.Prefixes = append(r.Prefixes, t.Prefix)
			}
		}
		sort.Strings(r.Prefixes)
		out = append(out, *r)
	}
	return out
}

// EnvsFor proposes the environments a repository should be imported with.
// Tags carrying prefixes become one environment each; otherwise the project
// gets a single environment with no prefix.
func (r ImageRepo) EnvsFor() []Env {
	if len(r.Prefixes) == 0 {
		return []Env{{ID: newID(), Name: "Mặc định", BuildArgs: []KV{}, Labels: []KV{}}}
	}
	envs := make([]Env, 0, len(r.Prefixes))
	for _, p := range r.Prefixes {
		envs = append(envs, Env{
			ID:        newID(),
			Name:      envNameFor(p),
			TagPrefix: p,
			BuildArgs: []KV{}, Labels: []KV{},
		})
	}
	return envs
}

// envNameFor turns a tag prefix into a readable environment name.
func envNameFor(prefix string) string {
	name := strings.Trim(prefix, "-_")
	switch strings.ToLower(name) {
	case "prod", "production":
		return "Production"
	case "stg", "staging":
		return "Staging"
	case "dev", "develop", "development":
		return "Development"
	case "test", "qa":
		return "Test"
	case "uat":
		return "UAT"
	}
	if name == "" {
		return "Mặc định"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
