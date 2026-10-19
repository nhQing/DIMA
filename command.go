package main

import (
	"regexp"
	"slices"
	"strings"
)

// BuildVars are the placeholders a custom build command can use. They are
// listed here rather than scattered through the code so the settings screen
// can show exactly what is available, with no chance of drifting apart.
var BuildVars = []struct{ Name, About string }{
	{"tag", "tag thật trên registry, gồm cả tiền tố của môi trường"},
	{"ref", "tên image đầy đủ kèm tag — lệnh của bạn cần gắn tag thành cái này"},
	{"repo", "tên image đầy đủ, không có tag"},
	{"registry", "chỉ phần registry"},
	{"image", "chỉ phần tên image"},
	{"env", "tên môi trường"},
	{"project", "tên dự án"},
	{"context", "thư mục mã nguồn"},
	{"dockerfile", "tên Dockerfile đang khai"},
	{"envfile", "đường dẫn file .env đã khai cho môi trường này"},
	{"labels", "các cờ --label (version, commit, môi trường, labels đã khai) — đặt vào lệnh docker build trong script của bạn"},
}

// versionVar used to stand for the bare version number, without the
// environment's tag prefix. It is gone on purpose.
//
// A custom command is what actually tags the image, so whatever DIMA hands it
// has to be the exact string DIMA will later look for, push, digest and
// promote. {version} could never be that string. An environment with prefix
// "v" expects admin-mmt:v1.2.2 while {version} passes 1.2.2, so the script
// tags something DIMA cannot find — and DIMA then pushes a name that either
// does not exist or, worse, still points at an older image.
//
// That is not hypothetical: it is exactly how the admin-mmt push broke, and
// the same split between "prefix" and "version" is what once turned prefix
// "v" plus a version typed as "v1.1.0" into the tag vv1.1.0.
//
// Keeping the name but expanding it to the prefixed value would be worse: the
// name would then lie about its own contents. Removing it makes the mistake
// impossible to express, and lets the error say what to use instead.
const versionVar = "{version}"

// CheckBuildCommand reports why a custom build command cannot be run, or "".
//
// Only {version} is rejected. Other unknown placeholders are still left alone
// by expandVars, because a command may legitimately contain braces — a Go
// template in docker inspect, for one — and guessing there would break
// working commands to catch a typo.
func CheckBuildCommand(command string) string {
	if !strings.Contains(command, versionVar) {
		return ""
	}
	return "lệnh build còn dùng biến {version}. Biến này đã bỏ vì nó không kèm tiền tố tag, " +
		"nên lệnh của bạn sẽ gắn tag khác với tag DIMA đi tìm lúc push. " +
		"Đổi thành {tag} (chỉ phần tag, ví dụ v1.2.2) hoặc {ref} (tên image đầy đủ kèm tag)"
}

// buildVarValues collects what each placeholder stands for, for one build.
// labels are every label the image should carry, DIMA's own first.
func buildVarValues(p Project, target Env, env Env, version, ref string, labels []KV) map[string]string {
	flags := make([]string, 0, len(labels))
	for _, kv := range labels {
		flags = append(flags, "--label "+shellQuote(kv.Key+"="+kv.Value))
	}
	return map[string]string{
		"labels":     strings.Join(flags, " "),
		"tag":        env.Tag(version),
		"ref":        ref,
		"repo":       env.Repo(),
		"registry":   strings.TrimSpace(env.Registry),
		"image":      strings.TrimSpace(env.Image),
		"env":        target.Name,
		"project":    p.Name,
		"context":    strings.TrimSpace(env.Context),
		"dockerfile": dockerfileOr(env.Dockerfile),
		"envfile":    envFileOr(env.EnvFile),
	}
}

// Kinds of a user-defined parameter, which decide how the build dialog asks
// for it.
const (
	// ParamText is a free text box.
	ParamText = "text"
	// ParamChoice is a dropdown over Param.Options.
	ParamChoice = "choice"
	// ParamFlag is a checkbox: ticked puts Param.Options into the command,
	// unticked puts nothing. That is how an optional switch like "-push" is
	// written without leaving a dangling "-push " behind when it is off.
	ParamFlag = "flag"
)

// Param describes a placeholder of the user's own in a custom build command
// — {cache} in `./build.ps1 -Ref {ref} -Cache {cache}` — so the build dialog
// can ask for it in a table instead of the command being edited by hand
// before every build.
//
// A placeholder that appears in the command but has no Param row is still a
// parameter: it is asked for as plain, optional text. The row only adds what
// the command alone cannot say.
type Param struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Default fills the box the first time. After that the value used in the
	// environment's last build wins, because that is what the person most
	// likely wants again.
	Default string `json:"default"`
	// Options is comma-separated for ParamChoice, and the text put into the
	// command when ticked for ParamFlag.
	Options  string `json:"options"`
	Required bool   `json:"required"`
	About    string `json:"about"`
}

// mergeParams overlays an environment's parameter rows on its project's,
// matching on name, the same way mergeKV treats build args: a row the
// environment declares replaces the project's row of that name whole.
func mergeParams(base, over []Param) []Param {
	out := []Param{}
	seen := map[string]int{}
	for _, list := range [][]Param{base, over} {
		for _, p := range list {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				continue
			}
			p.Name = name
			if i, ok := seen[name]; ok {
				out[i] = p
				continue
			}
			seen[name] = len(out)
			out = append(out, p)
		}
	}
	return out
}

// placeholderRe matches {name}. A name has to start with a letter, so the
// {{.Id}} of a Go template in a docker inspect call is never taken for one.
var placeholderRe = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_-]*)\}`)

// isBuiltinVar reports whether DIMA itself supplies a placeholder's value.
// {version} counts as taken: it is gone on purpose (see versionVar), and
// letting it come back as a user parameter would bring back the exact
// mismatch its removal fixed.
func isBuiltinVar(name string) bool {
	if "{"+name+"}" == versionVar {
		return true
	}
	for _, v := range BuildVars {
		if v.Name == name {
			return true
		}
	}
	return false
}

// commandParams lists the user's own placeholders in a command, in the order
// they first appear, each once.
func commandParams(command string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range placeholderRe.FindAllStringSubmatch(command, -1) {
		name := m[1]
		if isBuiltinVar(name) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// CheckParams reports why a list of parameter rows cannot be saved, or "".
func CheckParams(params []Param) string {
	for _, p := range params {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		if !placeholderRe.MatchString("{" + name + "}") {
			return "tên tham số “" + name + "” chỉ được gồm chữ, số, - và _, và phải bắt đầu bằng chữ"
		}
		if isBuiltinVar(name) {
			return "tên tham số “" + name + "” trùng với biến DIMA tự điền — đặt tên khác"
		}
		switch p.Kind {
		case "", ParamText, ParamChoice, ParamFlag:
		default:
			return "tham số “" + name + "” có kiểu không hợp lệ: " + p.Kind
		}
		if p.Kind == ParamChoice && len(splitOptions(p.Options)) == 0 {
			return "tham số “" + name + "” là danh sách chọn nhưng chưa có lựa chọn nào"
		}
	}
	return ""
}

func splitOptions(s string) []string {
	out := []string{}
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// ParamField is one row of the build dialog's parameter table: the
// definition plus the value it starts with.
type ParamField struct {
	Param
	Value string `json:"value"`
}

// paramFields lists every parameter an effective environment's command asks
// for, each with the value the dialog should show. Given values win, then
// the value used in this environment's last build, then the default.
//
// A flag's value is "true" or "false" here — the state of the checkbox —
// and only becomes Options when the command is expanded.
func paramFields(env Env, given map[string]string) []ParamField {
	defs := map[string]Param{}
	for _, p := range env.Params {
		defs[p.Name] = p
	}
	out := []ParamField{}
	for _, name := range commandParams(env.BuildCommand) {
		def, ok := defs[name]
		if !ok {
			def = Param{Name: name}
		}
		if def.Kind == "" {
			def.Kind = ParamText
		}
		value, ok := given[name]
		if !ok {
			value, ok = env.LastParams[name]
		}
		if !ok {
			value = def.Default
		}
		if def.Kind == ParamFlag {
			value = flagState(value)
		}
		out = append(out, ParamField{Param: def, Value: value})
	}
	return out
}

// flagState reads anything that means "on" as on. Defaults are typed by
// hand, so "1", "yes" and "có" are as likely as "true".
func flagState(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on", "có", "x":
		return "true"
	}
	return "false"
}

// paramValues turns the dialog's rows into what each placeholder expands
// to, and lists the required ones left empty or holding a value the
// dropdown never offered.
func paramValues(fields []ParamField) (map[string]string, []string) {
	values := map[string]string{}
	problems := []string{}
	for _, f := range fields {
		v := strings.TrimSpace(f.Value)
		switch f.Kind {
		case ParamFlag:
			v = ""
			if flagState(f.Value) == "true" {
				v = strings.TrimSpace(f.Options)
			}
		case ParamChoice:
			if v != "" && !slices.Contains(splitOptions(f.Options), v) {
				problems = append(problems, f.Name+" (giá trị “"+v+"” không có trong danh sách chọn)")
				continue
			}
		}
		if f.Required && f.Kind != ParamFlag && v == "" {
			problems = append(problems, f.Name)
		}
		values[f.Name] = v
	}
	return values, problems
}

// expandVars replaces {name} with its value, in a single pass over the
// template.
//
// One pass, not one ReplaceAll per name: a substituted value is never read
// again. Otherwise a branch named fix{ref} inside {labels}, or a parameter
// typed as {tag}, would be expanded a second time — and a quote inside a
// later value could break out of the quoting {labels} put around an earlier
// one. Unknown placeholders are left exactly as written: silently blanking
// them would turn a typo into a command that runs and does the wrong thing.
func expandVars(template string, values map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(template, func(m string) string {
		if v, ok := values[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}
