package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cleaning up removes old local images of a project — the ones a build
// machine piles up at a few hundred megabytes each — and nothing else. It
// never touches the registry: deleting a tag there could take down a server
// pulling it, and that is not a decision a "free up disk" button gets to make.
//
// What is kept is decided per environment: the newest N version tags. On top
// of that a tag is always kept when
//   - a container on this machine was started from it (docker would refuse
//     anyway, and the person would read the refusal as a bug),
//   - it is the environment's current version (what the sidebar shows),
//   - it is not a version at all: latest, cache, stable are roles that
//     something else is probably pointing at.
// Removal is `docker rmi <ref>` without -f, so docker's own refusals — an
// image another tag still needs, say — stand.

// CleanupItem is one local tag of the project, and what cleaning does to it.
type CleanupItem struct {
	Ref       string    `json:"ref"`
	Tag       string    `json:"tag"`
	EnvID     string    `json:"envId"`
	EnvName   string    `json:"envName"`
	Size      string    `json:"size"`
	Age       string    `json:"age"`
	CreatedAt time.Time `json:"createdAt"`
	Remove    bool      `json:"remove"`
	// Why says why the tag stays, or why it goes.
	Why string `json:"why"`
}

func planCleanup(p Project, images []LocalImage, history []Build, containers []Container, keep int) []CleanupItem {
	if keep < 1 {
		keep = 1 // keeping nothing would remove the version an environment is on
	}
	envsByRepo := map[string][]Env{}
	for _, e := range p.Envs {
		r := p.Effective(e).Repo()
		envsByRepo[r] = append(envsByRepo[r], e)
	}
	usedBy := map[string]string{}
	for _, c := range containers {
		if _, seen := usedBy[c.Image]; !seen {
			usedBy[c.Image] = c.Name
		}
	}
	// history is newest first: the first success per environment is current.
	current := map[string]string{}
	for _, b := range history {
		if b.Status == "success" && b.ProjectID == p.ID {
			if _, seen := current[b.EnvID]; !seen {
				current[b.EnvID] = b.Ref
			}
		}
	}

	items := []CleanupItem{}
	byEnv := map[string][]int{}
	for _, img := range images {
		envs, ok := envsByRepo[img.Repository]
		if !ok {
			continue
		}
		it := CleanupItem{
			Ref: img.Repository + ":" + img.Tag, Tag: img.Tag,
			Size: img.Size, Age: img.Age, CreatedAt: img.CreatedAt,
		}
		e := envForTag(envs, img.Tag)
		if e == nil {
			it.Why = "không phải tag phiên bản của môi trường nào — để nguyên"
			items = append(items, it)
			continue
		}
		it.EnvID, it.EnvName = e.ID, e.Name
		if _, _, isVersion := classifyTag(img.Tag); !isVersion {
			it.Why = "không phải phiên bản (latest, cache…) — để nguyên"
			items = append(items, it)
			continue
		}
		byEnv[e.ID] = append(byEnv[e.ID], len(items))
		items = append(items, it)
	}

	for envID, idx := range byEnv {
		sort.Slice(idx, func(a, b int) bool {
			ia, ib := items[idx[a]], items[idx[b]]
			if !ia.CreatedAt.Equal(ib.CreatedAt) {
				return ia.CreatedAt.After(ib.CreatedAt)
			}
			return ia.Tag > ib.Tag
		})
		for rank, i := range idx {
			it := &items[i]
			switch {
			case usedBy[it.Ref] != "":
				it.Why = "container " + usedBy[it.Ref] + " đang dùng"
			case current[envID] == it.Ref:
				it.Why = "bản hiện tại của " + it.EnvName
			case rank < keep:
				it.Why = "trong " + strconv.Itoa(keep) + " bản mới nhất"
			default:
				it.Remove = true
				it.Why = "cũ hơn " + strconv.Itoa(keep) + " bản mới nhất"
			}
		}
	}

	sort.SliceStable(items, func(a, b int) bool {
		if items[a].EnvName != items[b].EnvName {
			return items[a].EnvName < items[b].EnvName
		}
		return items[a].CreatedAt.After(items[b].CreatedAt)
	})
	return items
}

// removeImage untags one local image. docker's own message is returned on
// refusal, because it is the one that says what still needs the image.
func (s *Store) removeImage(ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, s.Config().DockerBin, "rmi", ref)
	hideWindow(cmd)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}
