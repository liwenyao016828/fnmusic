package search

import (
	"context"
	"reflect"
	"testing"
)

func TestParseIncludeNormalizesAndDeduplicates(t *testing.T) {
	got := parseInclude(" LX, musicdl, lx, , MUSICDL ")
	want := []string{"lx", "musicdl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseInclude() = %#v, want %#v", got, want)
	}
}

func TestExtraSourceRegistry(t *testing.T) {
	const name = "test-extra-source"
	UnregisterExtraSource(name)
	t.Cleanup(func() { UnregisterExtraSource(name) })

	RegisterExtraSource(" TEST-EXTRA-SOURCE ", func(_ context.Context, keyword string) []UnifiedSong {
		return []UnifiedSong{{Name: keyword, Source: name}}
	})
	if got := ExtraSourceNames(); !containsString(got, name) {
		t.Fatalf("registered extra source missing from names: %#v", got)
	}

	fns := extraSourcesFor(parseInclude("TEST-EXTRA-SOURCE"))
	if len(fns) != 1 {
		t.Fatalf("got %d registered functions, want 1", len(fns))
	}
	got := fns[0](context.Background(), "夜曲")
	if len(got) != 1 || got[0].Name != "夜曲" || got[0].Source != name {
		t.Fatalf("unexpected extra source results: %#v", got)
	}

	UnregisterExtraSource(name)
	if got := extraSourcesFor(parseInclude(name)); len(got) != 0 {
		t.Fatalf("unregistered source should not be returned: %#v", got)
	}
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
