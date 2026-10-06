package main

import (
	"strings"
	"testing"
)

func sampleVars() map[string]string {
	p := Project{Name: "web"}
	target := Env{Name: "Production"}
	env := Env{
		Name: "Production", TagPrefix: "prod-",
		Registry: "harbor.tech", Image: "mm/web",
		Context: `D:\work\web`, Dockerfile: "",
	}
	return buildVarValues(p, target, env, "1.2.3", env.Ref("1.2.3"))
}

func TestBuildVarValuesCoverEveryDocumentedName(t *testing.T) {
	values := sampleVars()
	for _, v := range BuildVars {
		if _, ok := values[v.Name]; !ok {
			t.Errorf("biến {%s} được nêu trong tài liệu nhưng không có giá trị", v.Name)
		}
	}
	if len(values) != len(BuildVars) {
		t.Errorf("có %d giá trị nhưng tài liệu nêu %d biến", len(values), len(BuildVars))
	}
}

func TestBuildVarValues(t *testing.T) {
	values := sampleVars()
	want := map[string]string{

		"tag":        "prod-1.2.3",
		"ref":        "harbor.tech/mm/web:prod-1.2.3",
		"repo":       "harbor.tech/mm/web",
		"registry":   "harbor.tech",
		"image":      "mm/web",
		"env":        "Production",
		"project":    "web",
		"context":    `D:\work\web`,
		"dockerfile": "Dockerfile", // để trống thì mặc định là Dockerfile
	}
	for name, w := range want {
		if values[name] != w {
			t.Errorf("{%s} = %q, muốn %q", name, values[name], w)
		}
	}
}

func TestExpandVarsSubstitutesEveryOccurrence(t *testing.T) {
	got := expandVars("./build.sh {tag} && docker tag app:{tag} {ref}", sampleVars())
	want := "./build.sh prod-1.2.3 && docker tag app:prod-1.2.3 harbor.tech/mm/web:prod-1.2.3"
	if got != want {
		t.Errorf("= %q\nmuốn %q", got, want)
	}
}

func TestExpandVarsLeavesUnknownPlaceholdersAlone(t *testing.T) {
	// Gõ sai tên biến thì phải thấy được, chứ không được âm thầm thành rỗng.
	got := expandVars("./build.sh {verison}", sampleVars())
	if !strings.Contains(got, "{verison}") {
		t.Errorf("= %q, muốn giữ nguyên biến gõ sai để còn nhận ra", got)
	}
}

func TestExpandVarsIsNotConfusedByShorterNames(t *testing.T) {
	// {version} chứa "{ver..." — thay biến ngắn trước sẽ phá biến dài.
	values := map[string]string{"version": "1.2.3", "ver": "XX", "v": "Y"}
	if got := expandVars("{version}", values); got != "1.2.3" {
		t.Errorf("= %q, muốn 1.2.3", got)
	}
}

func TestExpandVarsWithoutPlaceholders(t *testing.T) {
	if got := expandVars("make release", sampleVars()); got != "make release" {
		t.Errorf("= %q, lệnh không có biến thì phải giữ nguyên", got)
	}
}

func TestShellCommandWrapsTheWholeLine(t *testing.T) {
	bin, args := shellCommand("./build.ps1 1.2.3 && echo xong")

	if bin == "" {
		t.Fatal("phải chỉ ra shell để chạy")
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "./build.ps1 1.2.3 && echo xong") {
		t.Errorf("tham số = %v, muốn giữ nguyên cả dòng lệnh", args)
	}
}

func TestBuildCommandIsInherited(t *testing.T) {
	p := Project{BuildCommand: "./build.sh {tag}"}

	if got := p.Effective(Env{}).BuildCommand; got != "./build.sh {tag}" {
		t.Errorf("= %q, môi trường bỏ trống thì phải kế thừa của dự án", got)
	}
	if got := p.Effective(Env{BuildCommand: "make {tag}"}).BuildCommand; got != "make {tag}" {
		t.Errorf("= %q, môi trường khai riêng thì phải thắng", got)
	}
}

// Biến {version} đã bỏ. Nó trả về số version trần, không kèm tiền tố tag, nên
// một lệnh build dùng nó sẽ gắn tag khác với tag DIMA đi tìm lúc push — đúng
// thứ đã làm hỏng lần push của admin-mmt.
func TestVersionVarNoLongerExists(t *testing.T) {
	for _, v := range BuildVars {
		if v.Name == "version" {
			t.Fatal("{version} vẫn còn trong danh sách biến — nó phải bị bỏ hẳn")
		}
	}
	if _, ok := sampleVars()["version"]; ok {
		t.Error("{version} vẫn có giá trị thay thế — lệnh cũ sẽ chạy im mà sai tag")
	}
}

func TestExpandVarsLeavesVersionPlaceholderIntact(t *testing.T) {
	// Không được âm thầm thành rỗng: lệnh phải hỏng thấy được, và
	// CheckBuildCommand mới là chỗ nói ra lý do.
	got := expandVars("./build.sh {version}", sampleVars())
	if !strings.Contains(got, "{version}") {
		t.Errorf("= %q, muốn giữ nguyên {version} để lỗi không bị giấu đi", got)
	}
}

func TestCheckBuildCommandRejectsVersionVar(t *testing.T) {
	msg := CheckBuildCommand("./build-and-push.bat {version}")
	if msg == "" {
		t.Fatal("phải từ chối lệnh còn dùng {version}")
	}
	if !strings.Contains(msg, "{tag}") {
		t.Errorf("lời báo lỗi phải chỉ ra dùng gì thay thế, hiện là: %s", msg)
	}
}

func TestCheckBuildCommandAcceptsTagAndRef(t *testing.T) {
	for _, cmd := range []string{
		"./build-and-push.bat {tag}",
		"./build.ps1 {ref}",
		"make release",
		"",                                  // không khai lệnh riêng
		`docker inspect -f '{{.Id}}' {ref}`, // dấu ngoặc của Go template
	} {
		if msg := CheckBuildCommand(cmd); msg != "" {
			t.Errorf("lệnh %q bị từ chối nhầm: %s", cmd, msg)
		}
	}
}
