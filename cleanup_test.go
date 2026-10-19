package main

import (
	"testing"
	"time"
)

func TestPlanCleanupKeepsWhatMatters(t *testing.T) {
	p := Project{ID: "p", Registry: "harbor.tech", Image: "mmt/admin", Envs: []Env{
		{ID: "stg", Name: "Staging", TagPrefix: "v"},
		{ID: "prd", Name: "Production", TagPrefix: "prod-v"},
	}}
	day := func(n int) time.Time { return time.Date(2026, 10, n, 9, 0, 0, 0, time.UTC) }
	repo := "harbor.tech/mmt/admin"
	images := []LocalImage{
		{Repository: repo, Tag: "v1.5.0", CreatedAt: day(15)},
		{Repository: repo, Tag: "v1.4.0", CreatedAt: day(14)},
		{Repository: repo, Tag: "v1.3.0", CreatedAt: day(13)}, // container dùng
		{Repository: repo, Tag: "v1.2.0", CreatedAt: day(12)}, // cũ → xoá
		{Repository: repo, Tag: "v1.1.0", CreatedAt: day(11)}, // cũ → xoá
		{Repository: repo, Tag: "prod-v1.0.0", CreatedAt: day(1)}, // bản hiện tại của prod
		{Repository: repo, Tag: "prod-v0.9.0", CreatedAt: day(2)}, // trong 2 bản mới nhất của prod
		{Repository: repo, Tag: "cache", CreatedAt: day(1)},
		{Repository: "postgres", Tag: "16", CreatedAt: day(1)}, // không thuộc dự án
	}
	history := []Build{
		{ProjectID: "p", EnvID: "prd", Ref: repo + ":prod-v1.0.0", Status: "success"},
	}
	containers := []Container{{Name: "web", Image: repo + ":v1.3.0"}}

	items := planCleanup(p, images, history, containers, 2)
	got := map[string]CleanupItem{}
	for _, it := range items {
		got[it.Tag] = it
	}
	if _, ok := got["16"]; ok {
		t.Error("image của repository khác không được lọt vào kế hoạch")
	}
	wantRemove := map[string]bool{"v1.2.0": true, "v1.1.0": true}
	for tag, it := range got {
		if it.Remove != wantRemove[tag] {
			t.Errorf("%s: remove=%v (%s), muốn %v", tag, it.Remove, it.Why, wantRemove[tag])
		}
	}
	if got["v1.3.0"].Why != "container web đang dùng" {
		t.Errorf("v1.3.0 why = %q", got["v1.3.0"].Why)
	}
	if got["prod-v1.0.0"].EnvID != "prd" || got["prod-v1.0.0"].Why != "bản hiện tại của Production" {
		t.Errorf("prod-v1.0.0 = %+v", got["prod-v1.0.0"])
	}
	if got["cache"].Remove {
		t.Error("tag không phải phiên bản không bao giờ bị xoá")
	}
}

// Hai môi trường cùng repo, cùng không tiền tố: mọi tag bị xếp vào một bên,
// nhưng bản hiện tại của bên kia vẫn phải được giữ.
func TestPlanCleanupKeepsCurrentOfAnEnvSharingThePrefix(t *testing.T) {
	p := Project{ID: "p", Image: "app", Envs: []Env{
		{ID: "stg", Name: "Staging"},
		{ID: "dev", Name: "Dev", IsOnlyLocal: true},
	}}
	day := func(n int) time.Time { return time.Date(2026, 10, n, 9, 0, 0, 0, time.UTC) }
	images := []LocalImage{
		{Repository: "app", Tag: "1.0.9", CreatedAt: day(19)},
		{Repository: "app", Tag: "1.0.8", CreatedAt: day(18)},
		{Repository: "app", Tag: "1.0.4", CreatedAt: day(4)}, // bản hiện tại của Dev
	}
	history := []Build{
		{ProjectID: "p", EnvID: "stg", Ref: "app:1.0.9", Status: "success"},
		{ProjectID: "p", EnvID: "dev", Ref: "app:1.0.4", Status: "success"},
	}
	for _, it := range planCleanup(p, images, history, nil, 1) {
		if it.Tag == "1.0.4" && (it.Remove || it.Why != "bản hiện tại của Dev") {
			t.Errorf("1.0.4 = %+v — bản hiện tại của Dev bị đem xoá", it)
		}
		if it.Tag == "1.0.8" && !it.Remove {
			t.Errorf("1.0.8 phải bị xoá khi chỉ giữ 1 bản: %+v", it)
		}
	}
}

func TestPlanCleanupNeverKeepsZero(t *testing.T) {
	p := Project{ID: "p", Image: "a", Envs: []Env{{ID: "e", Name: "E", TagPrefix: ""}}}
	items := planCleanup(p, []LocalImage{{Repository: "a", Tag: "1.0.0"}}, nil, nil, 0)
	if len(items) != 1 || items[0].Remove {
		t.Errorf("giữ 0 bản sẽ xoá cả bản đang dùng: %+v", items)
	}
}
