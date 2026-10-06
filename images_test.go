package main

import (
	"testing"
	"time"
)

func TestSplitRepositoryFindsTheRegistryHost(t *testing.T) {
	cases := []struct{ repo, reg, img string }{
		{"harbor.tech/mm/mai-money-website", "harbor.tech", "mm/mai-money-website"},
		{"registry.local:5000/team/api", "registry.local:5000", "team/api"},
		{"localhost/thu-nghiem", "localhost", "thu-nghiem"},
		// Docker Hub: the first segment is a user, not a host.
		{"moby/buildkit", "", "moby/buildkit"},
		{"postgres", "", "postgres"},
		{"maimoney-builder", "", "maimoney-builder"},
	}
	for _, c := range cases {
		reg, img := splitRepository(c.repo)
		if reg != c.reg || img != c.img {
			t.Errorf("splitRepository(%q) = %q/%q, muốn %q/%q", c.repo, reg, img, c.reg, c.img)
		}
	}
}

func TestClassifyTagSeparatesPrefixFromVersion(t *testing.T) {
	cases := []struct {
		tag, prefix, version string
		isVersion            bool
	}{
		{"v1.2.3", "", "v1.2.3", true},
		{"1.2.3", "", "1.2.3", true},
		{"prod-1.2.3", "prod-", "1.2.3", true},
		{"staging_2024.10", "staging_", "2024.10", true},
		{"20260922-1530", "", "20260922-1530", true},
		{"16-alpine", "", "16-alpine", true},
		// Không phải phiên bản: không có chữ số nào.
		{"latest", "", "latest", false},
		{"cache", "", "cache", false},
		{"local", "", "local", false},
		{"stable", "", "stable", false},
	}
	for _, c := range cases {
		p, v, ok := classifyTag(c.tag)
		if p != c.prefix || v != c.version || ok != c.isVersion {
			t.Errorf("classifyTag(%q) = %q/%q/%v, muốn %q/%q/%v",
				c.tag, p, v, ok, c.prefix, c.version, c.isVersion)
		}
	}
}

func TestGroupImagesFoldsTagsUnderOneRepository(t *testing.T) {
	repos := GroupImages([]LocalImage{
		{Repository: "harbor.tech/mm/web", Tag: "v1.2.2", ID: "a", Age: "19 minutes ago"},
		{Repository: "harbor.tech/mm/web", Tag: "v1.2.1", ID: "b", Age: "33 minutes ago"},
		{Repository: "harbor.tech/mm/web", Tag: "cache", ID: "c", Age: "34 minutes ago"},
		{Repository: "postgres", Tag: "16-alpine", ID: "d", Age: "2 months ago"},
	})

	if len(repos) != 2 {
		t.Fatalf("có %d repository, muốn 2", len(repos))
	}
	web := repos[0]
	if web.Registry != "harbor.tech" || web.Image != "mm/web" || web.Name != "web" {
		t.Errorf("tách sai: %+v", web)
	}
	if !web.Private {
		t.Error("repo có registry riêng phải được đánh dấu private")
	}
	if len(web.Tags) != 3 {
		t.Errorf("có %d tag, muốn 3", len(web.Tags))
	}
	if web.Age != "19 minutes ago" {
		t.Errorf("tuổi = %q, muốn lấy theo tag mới nhất", web.Age)
	}
	if repos[1].Private {
		t.Error("postgres trên Docker Hub không được coi là repo riêng")
	}
}

func TestGroupImagesCollectsEnvPrefixes(t *testing.T) {
	repos := GroupImages([]LocalImage{
		{Repository: "reg.local/app", Tag: "prod-1.0.0"},
		{Repository: "reg.local/app", Tag: "staging-1.1.0"},
		{Repository: "reg.local/app", Tag: "staging-1.0.0"},
		{Repository: "reg.local/app", Tag: "latest"},
	})

	got := repos[0].Prefixes
	if len(got) != 2 || got[0] != "prod-" || got[1] != "staging-" {
		t.Errorf("tiền tố = %v, muốn [prod- staging-] không trùng lặp", got)
	}
}

func TestEnvsForBuildsOneEnvironmentPerPrefix(t *testing.T) {
	envs := ImageRepo{Prefixes: []string{"prod-", "staging-"}}.EnvsFor()

	if len(envs) != 2 {
		t.Fatalf("có %d môi trường, muốn 2", len(envs))
	}
	if envs[0].Name != "Production" || envs[0].TagPrefix != "prod-" {
		t.Errorf("môi trường đầu = %q/%q", envs[0].Name, envs[0].TagPrefix)
	}
	if envs[1].Name != "Staging" || envs[1].TagPrefix != "staging-" {
		t.Errorf("môi trường hai = %q/%q", envs[1].Name, envs[1].TagPrefix)
	}
	for _, e := range envs {
		if e.ID == "" {
			t.Error("môi trường phải được cấp id")
		}
	}
}

func TestEnvsForWithoutPrefixesMakesASingleEnvironment(t *testing.T) {
	envs := ImageRepo{}.EnvsFor()

	if len(envs) != 1 {
		t.Fatalf("có %d môi trường, muốn 1", len(envs))
	}
	if envs[0].TagPrefix != "" {
		t.Errorf("tiền tố = %q, muốn để trống vì tag không có tiền tố", envs[0].TagPrefix)
	}
}

func TestEnvNameForKnownAndUnknownPrefixes(t *testing.T) {
	cases := map[string]string{
		"prod-": "Production", "production-": "Production",
		"stg-": "Staging", "staging_": "Staging",
		"dev-": "Development", "qa-": "Test", "uat-": "UAT",
		"beta-": "Beta", // không nằm trong danh sách thì viết hoa chữ đầu
	}
	for in, want := range cases {
		if got := envNameFor(in); got != want {
			t.Errorf("envNameFor(%q) = %q, muốn %q", in, got, want)
		}
	}
}

func TestGroupImagesKeepsDockerOrder(t *testing.T) {
	// docker images trả về mới nhất trước; thứ tự đó phải được giữ.
	now := time.Now()
	repos := GroupImages([]LocalImage{
		{Repository: "b", Tag: "1", CreatedAt: now},
		{Repository: "a", Tag: "1", CreatedAt: now.Add(-time.Hour)},
	})
	if repos[0].Repository != "b" || repos[1].Repository != "a" {
		t.Errorf("thứ tự = %q, %q", repos[0].Repository, repos[1].Repository)
	}
}
