package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// logBuffer keeps a build's output in memory and mirrors it to disk so the
// log survives a restart. The UI polls it with a byte offset.
type logBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	file *os.File
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.Write(p)
	}
	return l.buf.Write(p)
}

func (l *logBuffer) writeString(s string) { _, _ = l.Write([]byte(s)) }

func (l *logBuffer) since(offset int) (string, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buf.Bytes()
	if offset < 0 || offset > len(b) {
		offset = 0
	}
	return string(b[offset:]), len(b)
}

func (l *logBuffer) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

func (s *Store) logFor(id string) *logBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lb, ok := s.logs[id]; ok {
		return lb
	}
	lb := &logBuffer{}
	// Opened for append, never truncated: pushing a build from an earlier
	// session reopens its log, and os.Create would wipe the build output that
	// is the only record of how that image was produced.
	//
	// The in-memory buffer has to start from what is already on disk, because
	// LogSince serves offsets out of that buffer. A buffer that started empty
	// would number the same bytes differently from the file, and the UI would
	// then show the wrong slice of the log.
	if raw, err := os.ReadFile(s.logPath(id)); err == nil {
		lb.buf.Write(raw)
	}
	if f, err := os.OpenFile(s.logPath(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		lb.file = f
	}
	s.logs[id] = lb
	return lb
}

// LogSince returns new output past offset. It falls back to the on-disk log
// for builds from an earlier session.
func (s *Store) LogSince(id string, offset int) (string, int) {
	s.mu.RLock()
	lb, ok := s.logs[id]
	s.mu.RUnlock()
	if ok {
		return lb.since(offset)
	}
	raw, err := os.ReadFile(s.logPath(id))
	if err != nil {
		return "", 0
	}
	if offset < 0 || offset > len(raw) {
		offset = 0
	}
	return string(raw[offset:]), len(raw)
}

func (s *Store) registerCancel(id string, fn func()) {
	s.mu.Lock()
	s.cancels[id] = fn
	s.mu.Unlock()
}

func (s *Store) Cancel(id string) bool {
	s.mu.Lock()
	fn, ok := s.cancels[id]
	s.mu.Unlock()
	if ok && fn != nil {
		fn()
	}
	return ok
}

func (s *Store) clearCancel(id string) {
	s.mu.Lock()
	delete(s.cancels, id)
	s.mu.Unlock()
}

// buildArgv assembles the docker command line for an environment.
func buildArgv(env Env, ref string) []string {
	args := []string{"build", "-t", ref}
	if f := strings.TrimSpace(env.Dockerfile); f != "" {
		args = append(args, "-f", f)
	}
	for _, kv := range env.BuildArgs {
		if strings.TrimSpace(kv.Key) == "" {
			continue
		}
		args = append(args, "--build-arg", kv.Key+"="+kv.Value)
	}
	for _, kv := range env.Labels {
		if strings.TrimSpace(kv.Key) == "" {
			continue
		}
		args = append(args, "--label", kv.Key+"="+kv.Value)
	}
	if p := strings.TrimSpace(env.Platform); p != "" {
		args = append(args, "--platform", p)
	}
	if t := strings.TrimSpace(env.Target); t != "" {
		args = append(args, "--target", t)
	}
	if extra := strings.Fields(env.ExtraFlags); len(extra) > 0 {
		args = append(args, extra...)
	}
	ctx := strings.TrimSpace(env.Context)
	if ctx == "" {
		ctx = "."
	}
	return append(args, ctx)
}

// Start kicks off a build (and an optional push) in the background and
// returns the new build record immediately. It always works with the
// environment resolved against its project.
func (s *Store) Start(p Project, target Env, version string, push bool) Build {
	env := p.Effective(target)
	ref := env.Ref(version)
	docker := s.Config().DockerBin
	git := ReadGit(env.Context)

	// A folder whose build is driven by a script replaces docker build
	// entirely; everything after the build — push, digest, version history,
	// promote — carries on unchanged.
	custom := strings.TrimSpace(env.BuildCommand)
	bin, argv := docker, buildArgv(env, ref)
	shown := docker + " " + strings.Join(argv, " ")
	if custom != "" {
		shown = expandVars(custom, buildVarValues(p, target, env, version, ref))
		bin, argv = shellCommand(shown)
	}

	// A local-only environment never has anywhere to push to. Start is the
	// one place that actually runs `docker push`, so this is enforced here
	// too — not just at the HTTP layer — in case some future caller reaches
	// Start with push=true for an environment it should not apply to.
	// Silently dropping the flag here (rather than erroring) is deliberate:
	// the HTTP handler is where the person gets told no, before anything
	// runs; by the time control reaches Start the build itself must go ahead
	// regardless.
	push = push && !env.IsOnlyLocal

	b := Build{
		ID:          newID(),
		ProjectID:   p.ID,
		ProjectName: p.Name,
		EnvID:       target.ID,
		EnvName:     target.Name,
		Version:     version,
		Tag:         env.Tag(version),
		Ref:         ref,
		Kind:        KindBuild,
		OnlyLocal:   env.IsOnlyLocal,
		Branch:      git.Branch,
		Commit:      git.Commit,
		Dirty:       git.Dirty,
		Status:      "running",
		Command:     shown,
		StartedAt:   time.Now(),
	}
	s.addBuild(b)
	s.rememberVersion(target.ID, version)

	lb := s.logFor(b.ID)
	ctx, cancel := context.WithCancel(context.Background())
	s.registerCancel(b.ID, cancel)

	go func() {
		defer cancel()
		defer s.clearCancel(b.ID)
		defer lb.close()

		// A command left over from before {version} was removed would run
		// with the placeholder still in it and tag the image under some name
		// DIMA never looks at. The HTTP layer already refuses this, but Start
		// is where the command actually runs, so it refuses here too.
		if msg := CheckBuildCommand(custom); msg != "" {
			lb.writeString("$ " + b.Command + "\n\nLỗi: " + msg + "\n")
			s.finish(b.ID, "failed", msg)
			return
		}

		lb.writeString("$ " + b.Command + "\n\n")
		err := s.run(ctx, lb, env.Context, bin, argv...)
		if err != nil {
			status := "failed"
			reason := err.Error()
			if ctx.Err() != nil {
				status = "canceled"
				// Say only what we know. DIMA killed the command and everything
				// it started on this machine, but `docker build` does its work
				// inside the Docker daemon, which is not ours to stop. Claiming
				// the build stopped would be the same kind of lie as marking a
				// canceled build successful.
				reason = "đã hủy trên máy này"
				lb.writeString("\nĐã hủy: DIMA dừng lệnh và mọi tiến trình nó khởi động.\n" +
					"Nếu đây là docker build thì phần chạy bên trong Docker daemon có thể vẫn đang làm nốt —\n" +
					"DIMA không dừng được nó. Cần dừng hẳn thì dùng `docker buildx prune` hoặc khởi động lại Docker.\n")
			} else {
				lb.writeString("\n" + err.Error() + "\n")
			}
			s.finish(b.ID, status, reason)
			return
		}

		// A script that does not tag its result as {ref} leaves nothing for
		// push, digest or promote to work with, so the build has failed even
		// though the command returned 0.
		//
		// This used to be a note in the log followed by the push running
		// anyway. That is the dangerous version: the tag DIMA is about to
		// push either does not exist — a confusing error — or still points at
		// an OLDER image from a previous build, in which case DIMA quietly
		// ships the wrong bytes and reports success. Stopping here is the
		// whole point of having checked.
		if custom != "" && !s.imageExists(env.Context, docker, ref) {
			reason := "lệnh build chạy xong nhưng không tạo ra image " + ref + " trên máy"
			note := "\nLỗi: lệnh chạy xong nhưng không thấy image " + ref + " trên máy.\n"

			// Before blaming the command, check whether it produced the same
			// image under another registry. That is nearly always what has
			// happened, and saying so turns a rerun into a one-field fix.
			near := []string{}
			if imgs, err := s.LocalImages(); err == nil {
				near = nearMisses(imgs, ref)
			}
			if len(near) > 0 {
				reason = "lệnh build đã tạo ra " + near[0] + ", nhưng DIMA đang theo dõi " + ref
				note += "Nhưng trên máy có: " + strings.Join(near, ", ") + "\n" +
					"Cùng tên image, cùng tag, chỉ khác phần registry — nên nhiều khả năng\n" +
					"lệnh của bạn chạy đúng, chỉ là ô Registry của dự án đang không khớp.\n" +
					"Sửa ô Registry trong Cài đặt dự án cho khớp rồi build lại là xong;\n" +
					"không cần đổi script.\n"
			} else {
				note += "Lệnh của bạn phải gắn tag image thành đúng tên đó — dùng biến {ref}.\n" +
					"Hai chỗ hay lệch nhau: tiền tố tag của môi trường, và ô Registry của dự án.\n"
			}
			note += "DIMA dừng ở đây và không push: nếu đẩy tiếp, nó sẽ đẩy bất cứ image cũ nào\n" +
				"đang mang tag này, hoặc báo một lỗi chẳng liên quan.\n"

			lb.writeString(note)
			s.finish(b.ID, "failed", reason)
			return
		}

		if push {
			lb.writeString("\n$ " + docker + " push " + ref + "\n\n")
			if err := s.run(ctx, lb, env.Context, docker, "push", ref); err != nil {
				status := "failed"
				if ctx.Err() != nil {
					status = "canceled"
				}
				lb.writeString("\n" + err.Error() + "\n")
				s.finish(b.ID, status, "build xong nhưng push lỗi: "+err.Error())
				return
			}
			s.updateBuild(b.ID, func(x *Build) { x.Pushed = true })
		}

		if d := s.digest(env.Context, docker, ref); d != "" {
			s.updateBuild(b.ID, func(x *Build) { x.Digest = d })

			// A digest for this repository means the image has been to the
			// registry under this exact name. DIMA did not put it there —
			// that branch above sets Pushed itself and never reaches here
			// with it unset — so the build command did, which is what a
			// script called build-and-push does.
			//
			// Recording that is not cosmetic. Without it the record says
			// "chỉ có trên máy" about an image that is on the registry, and
			// the UI offers a Push button for it. Pressing that re-pushes
			// the same bytes, which is harmless, but the same button on a
			// version that a LATER build has since moved the local tag for
			// would push the wrong image — the exact hole canPush exists to
			// close. Better to know the image has already shipped.
			//
			// A freshly built image carries no RepoDigest, and re-tagging
			// does not copy one from another image, so a build that only
			// built stays "chỉ có trên máy".
			if !push {
				lb.writeString("\nImage này đã có digest trên registry (" + d + ")," +
					" tức là lệnh build của bạn đã tự đẩy nó lên.\n" +
					"DIMA ghi nhận là đã push — nó không tự chạy lệnh push nào.\n")
				s.updateBuild(b.ID, func(x *Build) { x.Pushed = true })
			}
		}
		s.finish(b.ID, "success", "")
	}()

	return b
}

// canPush is the one place that decides whether a build's own status and
// history let it be pushed — the part of the question that does not need to
// know which environment the build belongs to. canPushEnv below layers the
// local-only check on top; the HTTP handler and PushExisting
// both go through canPushEnv, so the answer the UI is given and the
// answer the push path obeys can never drift apart.
//
// Why a build that did not succeed must not be pushed: `docker build -t` only
// moves the tag when the build finishes, so after a failed or canceled build
// the tag on this machine still points at the image of the PREVIOUS build.
// Pushing that record would put the wrong bytes on the registry and then leave
// a record carrying a digest — enough to satisfy every guard promoteBuild has,
// which is how the wrong image reaches production.
//
// The rule is stated over the statuses that are refused rather than by
// excluding one of them: the old "everything except failed" shape is exactly
// what let canceled slip through.
func canPush(b Build) error {
	if strings.TrimSpace(b.Ref) == "" {
		return fmt.Errorf("bản này không có tên image nên không biết phải đẩy gì lên registry — build lại để DIMA ghi được ref")
	}
	switch b.Status {
	case "running":
		return fmt.Errorf("bản này đang chạy nên chưa có image hoàn chỉnh để đẩy — chờ nó chạy xong rồi push")
	case "failed":
		return fmt.Errorf("bản này build lỗi nên tag %s trên máy vẫn đang là image của lần build trước — push sẽ đẩy nhầm bản lên registry; sửa lỗi rồi build lại", b.Ref)
	case "canceled":
		return fmt.Errorf("bản này bị hủy giữa chừng nên tag %s trên máy vẫn đang là image của lần build trước — push sẽ đẩy nhầm bản lên registry; build lại rồi push", b.Ref)
	}
	// Đã push rồi thì không push lại. Nghe như một hạn chế thừa, nhưng nó
	// chặn đúng cái lỗ vừa bịt ở trên, chỉ khác lối vào: tag trên máy là thứ
	// thay đổi được, còn bản ghi này thì đứng yên. Một lần build sau dùng lại
	// cùng version đã kéo tag sang image khác, nên push lại bản cũ sẽ đẩy
	// image MỚI lên registry rồi gắn digest của nó vào bản ghi CŨ — sai y hệt
	// trường hợp build bị hủy. Image đã nằm trên registry dưới đúng tag này
	// rồi; cần đẩy lại thì build lại.
	if b.Pushed {
		return fmt.Errorf("bản này đã đẩy lên registry rồi. Tag %s trên máy có thể đã bị một lần build sau kéo sang image khác, nên push lại dễ đưa nhầm bản lên; cần thay đổi thì build một version mới", b.Ref)
	}
	return nil
}

// onlyLocal reports whether this build's image must never leave the machine.
//
// It asks two sources because neither alone is complete. The build's own
// snapshot is the honest record — whether an image was meant to stay here is
// a fact about the build, and it survives the environment being renamed or
// deleted out from under old history. But rows imported from existing images,
// and every build made before the flag existed, carry no snapshot, so the
// environment's current setting has to answer for those.
//
// Either saying yes is enough. The two can only disagree by the user changing
// the setting later, and when they disagree the safe answer is the one that
// does not push.
func onlyLocal(cfg Config, b Build) bool {
	if b.OnlyLocal {
		return true
	}
	if _, e, ok := cfg.FindEnv(b.EnvID); ok {
		return e.IsOnlyLocal
	}
	return false
}

// canPushEnv layers the local-only rule on top of canPush. Every caller that
// can see the config goes through here rather than canPush alone — that is
// what keeps the push endpoint and PushExisting from drifting apart on a
// local-only environment, the same way canPush keeps them from drifting
// apart on build status.
//
// Checked before canPush: a local-only environment's imported rows are
// commonly "success" with no digest, and running that through canPush first
// would answer "push trước đã" — true of the field, wrong about the
// environment, and it sends the person looking for a push button that will
// never work.
func canPushEnv(cfg Config, b Build) error {
	if onlyLocal(cfg, b) {
		// Both names, because neither alone locates the setting: several
		// projects have an environment called Cache, and a project name
		// without the environment does not say which of its environments to
		// go and untick.
		where := strings.TrimSpace(b.EnvName)
		if where == "" {
			where = "này"
		}
		if p := strings.TrimSpace(b.ProjectName); p != "" {
			where += " của dự án " + p
		}
		return fmt.Errorf("môi trường %s được đánh dấu chỉ chạy trên máy này (không bao giờ đẩy lên registry) nên không push được — bỏ tick đó trong cài đặt môi trường nếu bạn thực sự cần đẩy lên registry", where)
	}
	return canPush(b)
}

// PushExisting pushes an image that was built earlier without pushing.
func (s *Store) PushExisting(b Build, workdir string) error {
	if err := canPushEnv(s.Config(), b); err != nil {
		return err
	}
	docker := s.Config().DockerBin
	lb := s.logFor(b.ID)
	lb.writeString("\n$ " + docker + " push " + b.Ref + "\n\n")
	if err := s.run(context.Background(), lb, workdir, docker, "push", b.Ref); err != nil {
		lb.writeString("\n" + err.Error() + "\n")
		return err
	}
	// A push records that the bytes reached the registry, nothing more. It
	// never rewrites Status: the status belongs to the build that produced the
	// image, and pushing does not re-run that build.
	s.updateBuild(b.ID, func(x *Build) { x.Pushed = true })
	if d := s.digest(workdir, docker, b.Ref); d != "" {
		s.updateBuild(b.ID, func(x *Build) { x.Digest = d })
	}
	return nil
}

// repoOf strips the tag or digest from an image reference, leaving the
// repository. The colon of a registry port is not a tag separator, so only
// a colon after the last slash counts.
func repoOf(ref string) string {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i]
	}
	return ref
}

// promoteSteps is the docker command line (or lines) that give an existing
// image a second tag.
//
// buildx does it on the registry: nothing is transferred, the digest is
// untouched and a multi-arch manifest survives. Without buildx the image
// has to come down to this machine and go back up, which is slower and
// flattens a multi-arch manifest to the local platform.
func promoteSteps(buildx bool, srcRef, dstRef string) [][]string {
	if buildx {
		return [][]string{{"buildx", "imagetools", "create", "-t", dstRef, srcRef}}
	}
	return [][]string{
		{"pull", srcRef},
		{"tag", srcRef, dstRef},
		{"push", dstRef},
	}
}

// buildxAvailable is checked once per run: the answer cannot change while
// the app is open.
func (s *Store) buildxAvailable() bool {
	s.buildxOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, s.Config().DockerBin, "buildx", "version")
		hideWindow(cmd)
		s.buildxOK = cmd.Run() == nil
	})
	return s.buildxOK
}

// BuildxOK reports whether promotes can skip the pull/push round trip.
func (s *Store) BuildxOK() bool { return s.buildxAvailable() }

// Promote gives an image that is already on the registry a second tag, so
// another environment serves the byte-for-byte same image. Nothing is
// rebuilt, so the digest of the result equals the digest of the source —
// that equality is the whole point, and it is what makes a rollback just a
// promote of an older version.
func (s *Store) Promote(p Project, target Env, src Build, version string) Build {
	env := p.Effective(target)
	ref := env.Ref(version)
	srcRef := repoOf(src.Ref) + "@" + src.Digest
	docker := s.Config().DockerBin
	steps := promoteSteps(s.buildxAvailable(), srcRef, ref)

	lines := make([]string, 0, len(steps))
	for _, st := range steps {
		lines = append(lines, docker+" "+strings.Join(st, " "))
	}

	b := Build{
		ID:            newID(),
		ProjectID:     p.ID,
		ProjectName:   p.Name,
		EnvID:         target.ID,
		EnvName:       target.Name,
		Version:       version,
		Tag:           env.Tag(version),
		Ref:           ref,
		Digest:        src.Digest,
		Kind:          KindPromote,
		SourceBuildID: src.ID,
		Branch:        src.Branch,
		Commit:        src.Commit,
		Dirty:         src.Dirty,
		Status:        "running",
		Command:       strings.Join(lines, " && "),
		StartedAt:     time.Now(),
	}
	s.addBuild(b)
	s.rememberVersion(target.ID, version)

	lb := s.logFor(b.ID)
	ctx, cancel := context.WithCancel(context.Background())
	s.registerCancel(b.ID, cancel)

	go func() {
		defer cancel()
		defer s.clearCancel(b.ID)
		defer lb.close()

		lb.writeString("Đưa " + srcRef + "\n      sang " + ref + "\n\n")
		if !s.buildxAvailable() {
			lb.writeString("(không có docker buildx — phải kéo image về máy rồi đẩy lên lại)\n\n")
		}
		for i, st := range steps {
			lb.writeString("$ " + lines[i] + "\n\n")
			if err := s.run(ctx, lb, env.Context, docker, st...); err != nil {
				status := "failed"
				if ctx.Err() != nil {
					status = "canceled"
				}
				lb.writeString("\n" + err.Error() + "\n")
				s.finish(b.ID, status, "promote lỗi: "+err.Error())
				return
			}
		}
		s.updateBuild(b.ID, func(x *Build) { x.Pushed = true })
		lb.writeString("\nXong. Digest giữ nguyên: " + src.Digest + "\n")
		s.finish(b.ID, "success", "")
	}()

	return b
}

func (s *Store) run(ctx context.Context, lb *logBuffer, workdir, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	groupChild(cmd)
	// CommandContext would kill just this one process, which is never where the
	// build actually runs: docker hands the work to its daemon, and a custom
	// build command runs through a shell whose children outlive it. Cancel has
	// to take down the whole tree, or "Dừng build" stops nothing the person can
	// see. WaitDelay then bounds how long a child that ignores the kill can hold
	// the record in "running".
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killTree(cmd.Process.Pid)
	}
	cmd.WaitDelay = 10 * time.Second
	if wd := strings.TrimSpace(workdir); wd != "" && wd != "." {
		if info, err := os.Stat(wd); err == nil && info.IsDir() {
			cmd.Dir = wd
		}
	}
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1", "BUILDKIT_PROGRESS=plain")
	cmd.Stdout = lb
	cmd.Stderr = lb
	if err := cmd.Start(); err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			return fmt.Errorf("không tìm thấy %q — kiểm tra Docker đã cài và nằm trong PATH", bin)
		}
		return err
	}
	return cmd.Wait()
}

// imageExists reports whether the machine has an image under this name.
func (s *Store) imageExists(workdir, docker, ref string) bool {
	cmd := exec.Command(docker, "image", "inspect", "--format", "{{.Id}}", ref)
	hideWindow(cmd)
	if wd := strings.TrimSpace(workdir); wd != "" && wd != "." {
		if info, err := os.Stat(wd); err == nil && info.IsDir() {
			cmd.Dir = wd
		}
	}
	return cmd.Run() == nil
}

// digestForRepo picks the digest belonging to one repository out of docker's
// RepoDigests list.
//
// The list is per repository, and an image can sit in several. Taking the
// first entry blindly, as this used to, can hand back the digest of a
// completely different repository the same image ID also answers to — and a
// digest is what promote re-tags, so the wrong one points production at the
// wrong bytes.
func digestForRepo(repoDigests []string, repo string) string {
	for _, rd := range repoDigests {
		i := strings.Index(rd, "@")
		if i < 0 {
			continue
		}
		if rd[:i] == repo {
			return rd[i+1:]
		}
	}
	return ""
}

// digest is the registry digest this image carries for its own repository,
// or "" when it carries none.
//
// A locally built image has none: docker only records a RepoDigest once the
// image has travelled to a registry, by push or by pull. That makes a
// non-empty answer here evidence — see the note in Start about what it is
// evidence OF.
func (s *Store) digest(workdir, docker, ref string) string {
	cmd := exec.Command(docker, "image", "inspect", "--format", "{{json .RepoDigests}}", ref)
	hideWindow(cmd)
	if wd := strings.TrimSpace(workdir); wd != "" && wd != "." {
		if info, err := os.Stat(wd); err == nil && info.IsDir() {
			cmd.Dir = wd
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var list []string
	if err := json.Unmarshal(out, &list); err != nil {
		return ""
	}
	return digestForRepo(list, repoOf(ref))
}

func (s *Store) finish(id, status, errMsg string) {
	s.updateBuild(id, func(b *Build) {
		b.Status = status
		b.Err = errMsg
		b.FinishedAt = time.Now()
	})
}

// nearMisses lists images on this machine that carry the same image name and
// tag as want, but under a different registry.
//
// A build command that does its own tagging cannot be wrong about whether it
// produced an image — only about what DIMA expected the name to be. The two
// drift apart for exactly one reason in practice: the project's Registry
// field. Leave it blank while the script tags harbor.tech/mm/admin-mmt and
// DIMA goes looking for a bare admin-mmt, finds nothing, and reports that the
// build produced no image — which is the opposite of what happened.
//
// So when the exact name is missing, look for the near miss and name it. The
// difference between "your build produced nothing" and "your build produced
// this, under another name" is the difference between a person rerunning a
// four-minute build and a person filling in one field.
func nearMisses(images []LocalImage, want string) []string {
	repo, tag := repoOf(want), ""
	if i := strings.LastIndex(want, ":"); i > strings.LastIndex(want, "/") {
		tag = want[i+1:]
	}
	// Compare on the last path segment: that is the image name proper, the
	// part a registry and namespace are prepended to.
	name := repo
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || tag == "" {
		return nil
	}
	out := []string{}
	for _, img := range images {
		if img.Tag != tag || img.Repository == repo {
			continue
		}
		short := img.Repository
		if i := strings.LastIndex(short, "/"); i >= 0 {
			short = short[i+1:]
		}
		if short == name {
			out = append(out, img.Repository+":"+img.Tag)
		}
	}
	return out
}

// LocalImage is one tag of one image already present on this machine.
type LocalImage struct {
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	ID         string    `json:"id"`
	Size       string    `json:"size"`
	Age        string    `json:"age"`
	CreatedAt  time.Time `json:"createdAt"`
}

// LocalImages lists what `docker images` sees, so existing work can be
// pulled into DIMA instead of being retyped.
//
// It asks for tab separated fields rather than JSON: the field names of
// `docker images --format json` have moved between docker versions, while
// this template has been stable for years.
func (s *Store) LocalImages() ([]LocalImage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Config().DockerBin, "images",
		"--format", "{{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.Size}}\t{{.CreatedSince}}\t{{.CreatedAt}}")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("không đọc được danh sách image: %w", err)
	}

	list := []LocalImage{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 6 || f[0] == "" || f[0] == "<none>" || f[1] == "<none>" {
			continue
		}
		img := LocalImage{Repository: f[0], Tag: f[1], ID: f[2], Size: f[3], Age: f[4]}
		// "2026-09-22 15:45:38 +0700 +07" — the trailing zone name is not
		// part of a layout Go understands, so parse the prefix only.
		if t, err := time.Parse("2006-01-02 15:04:05 -0700", strings.Join(strings.Fields(f[5])[:3], " ")); err == nil {
			img.CreatedAt = t
		}
		list = append(list, img)
	}
	return list, nil
}

// DockerVersion reports whether the docker CLI is reachable.
//
// The UI polls the state endpoint every few seconds, so the answer is cached
// briefly: asking docker each time meant spawning a process per poll, per
// open window, for a value that almost never changes.
func (s *Store) DockerVersion() (string, bool) {
	const freshFor = 10 * time.Second

	s.mu.RLock()
	if time.Since(s.dockerCheckedAt) < freshFor {
		v, ok := s.dockerVersion, s.dockerOK
		s.mu.RUnlock()
		return v, ok
	}
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Config().DockerBin, "version", "--format", "{{.Server.Version}}")
	hideWindow(cmd)
	out, err := cmd.Output()

	v := ""
	if err == nil {
		v = strings.TrimSpace(string(out))
	}

	s.mu.Lock()
	s.dockerVersion, s.dockerOK, s.dockerCheckedAt = v, v != "", time.Now()
	s.mu.Unlock()
	return v, v != ""
}
