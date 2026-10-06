package main

import (
	"sort"
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
func buildVarValues(p Project, target Env, env Env, version, ref string) map[string]string {
	return map[string]string{
		"tag":        env.Tag(version),
		"ref":        ref,
		"repo":       env.Repo(),
		"registry":   strings.TrimSpace(env.Registry),
		"image":      strings.TrimSpace(env.Image),
		"env":        target.Name,
		"project":    p.Name,
		"context":    strings.TrimSpace(env.Context),
		"dockerfile": dockerfileOr(env.Dockerfile),
	}
}

// expandVars replaces {name} with its value.
//
// Longer names are substituted first so that {version} is never damaged by
// a shorter name that happens to be a prefix of it. Unknown placeholders are
// left exactly as written: silently blanking them would turn a typo into a
// command that runs and does the wrong thing.
func expandVars(template string, values map[string]string) string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	out := template
	for _, name := range names {
		out = strings.ReplaceAll(out, "{"+name+"}", values[name])
	}
	return out
}
