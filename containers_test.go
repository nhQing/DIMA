package main

import "testing"

func TestMatchContainers(t *testing.T) {
	cfg := Config{Projects: []Project{{ID: "p", Registry: "harbor.tech", Image: "mmt/admin",
		Envs: []Env{{ID: "stg", TagPrefix: "s-"}, {ID: "prd", TagPrefix: "p-"}}}}}
	history := []Build{ // mới nhất trước
		{ID: "b3", ProjectID: "p", EnvID: "stg", Version: "1.2.0", Ref: "harbor.tech/mmt/admin:s-1.2.0", Status: "failed"},
		{ID: "b2", ProjectID: "p", EnvID: "stg", Version: "1.2.0", Ref: "harbor.tech/mmt/admin:s-1.2.0", Status: "success"},
		{ID: "b1", ProjectID: "p", EnvID: "prd", Version: "1.1.1", Ref: "harbor.tech/mmt/admin:p-1.1.1", Status: "success", Digest: "sha256:aaa"},
	}
	list := []Container{
		{Name: "by-tag", Image: "harbor.tech/mmt/admin:s-1.2.0"},
		{Name: "by-digest", Image: "harbor.tech/mmt/admin@sha256:aaa"},
		{Name: "latest", Image: "harbor.tech/mmt/admin:latest"},
		{Name: "moved-tag", Image: "3f2a9c1b7d00"},
		{Name: "other", Image: "postgres:16"},
	}
	got := map[string]Container{}
	for _, c := range matchContainers(cfg, history, list) {
		got[c.Name] = c
	}
	// Bản build hỏng không đổi được tag trên máy: container chạy bản thành công.
	if c := got["by-tag"]; c.BuildID != "b2" || c.EnvID != "stg" || c.Version != "1.2.0" {
		t.Errorf("by-tag = %+v", c)
	}
	if c := got["by-digest"]; c.BuildID != "b1" || c.EnvID != "prd" {
		t.Errorf("by-digest = %+v", c)
	}
	// Tag DIMA không ghi nhận: vẫn thuộc dự án, nhưng không bịa version.
	if c := got["latest"]; c.ProjectID != "p" || c.EnvID != "" || c.Version != "" {
		t.Errorf("latest = %+v", c)
	}
	// docker ps báo bằng ID khi tag đã bị kéo sang image khác: không đoán.
	if c := got["moved-tag"]; c.ProjectID != "" || c.Version != "" {
		t.Errorf("moved-tag = %+v", c)
	}
	if c := got["other"]; c.ProjectID != "" {
		t.Errorf("other = %+v", c)
	}
}
