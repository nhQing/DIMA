package main

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestDockerBuildCarriesVersionCommitAndEnvLabels(t *testing.T) {
	p := Project{Name: "admin", Image: "mmt/admin", Context: `D:\src`}
	git := GitInfo{IsRepo: true, Branch: "release/1.2", Commit: "abc1234", Dirty: true}
	plan := planBuild("docker", p, Env{Name: "Staging", TagPrefix: "s-"}, "1.2.0", nil, git)
	line := strings.Join(plan.Argv, " ")
	for _, want := range []string{
		"--label org.opencontainers.image.version=1.2.0",
		"--label org.opencontainers.image.title=admin",
		"--label dima.build-env=Staging",
		// Sửa dở lúc build: image không khớp commit nào, và nhãn phải nói ra.
		"--label org.opencontainers.image.revision=abc1234-dirty",
		"--label dima.branch=release/1.2",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("thiếu %q trong: %s", want, line)
		}
	}
}

func TestUserLabelOverridesDIMAsOwn(t *testing.T) {
	p := Project{Name: "admin", Image: "a", Labels: []KV{{Key: "org.opencontainers.image.title", Value: "Admin MMT"}}}
	plan := planBuild("docker", p, Env{Name: "S"}, "1.0.0", nil, GitInfo{})
	line := strings.Join(plan.Argv, " ")
	if strings.Contains(line, "image.title=admin") || !strings.Contains(line, "image.title=Admin MMT") {
		t.Errorf("label tự khai phải thắng label DIMA tự gắn: %s", line)
	}
}

// Không có git thì không có revision/branch — không bịa ra giá trị.
func TestNoGitNoRevisionLabel(t *testing.T) {
	labels := autoLabels(Project{Name: "x"}, Env{Name: "S"}, "1.0.0", GitInfo{})
	for _, kv := range labels {
		if kv.Key == "org.opencontainers.image.revision" || kv.Key == "dima.branch" {
			t.Errorf("không phải git repo mà vẫn có %s", kv.Key)
		}
	}
}

// Nhánh git tên x’;calc;’ không được thoát khỏi chuỗi trong nháy: PowerShell
// coi dấu nháy cong là nháy đơn. Chạy thật qua shell để chắc chắn.
func TestShellQuoteSurvivesTypographicQuotes(t *testing.T) {
	// So khớp cả chuỗi thì không được: console Windows in dấu nháy cong thành
	// nháy thẳng. Điều cần kiểm là lệnh chèn vào KHÔNG chạy: chỉ có đúng một
	// dòng ra, và nó vẫn chứa nguyên chữ "echo INJECTED".
	for _, v := range []string{"x’; echo INJECTED; ’", "x‘; echo INJECTED; ‛", "x‚; echo INJECTED; '"} {
		bin, args := shellCommand("echo " + shellQuote(v))
		out, err := exec.Command(bin, args...).Output()
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != 1 || !strings.Contains(lines[0], "echo INJECTED") {
			t.Errorf("echo %s in ra %q — lệnh chèn đã thoát khỏi dấu nháy", shellQuote(v), lines)
		}
	}
}

func TestLabelsVarQuotesEachLabelForTheShell(t *testing.T) {
	p := Project{Name: "it's", Image: "a", BuildCommand: "docker build {labels} -t {ref} ."}
	plan := planBuild("docker", p, Env{Name: "Mặc định"}, "1.0.0", nil, GitInfo{})
	quoted := "'dima.build-env=Mặc định'"
	title := "'org.opencontainers.image.title=it''s'"
	if runtime.GOOS != "windows" {
		title = `'org.opencontainers.image.title=it'\''s'`
	}
	for _, want := range []string{"--label " + quoted, "--label " + title} {
		if !strings.Contains(plan.Command, want) {
			t.Errorf("thiếu %s trong: %s", want, plan.Command)
		}
	}
}
