package online

import "testing"

func newSharedStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func track(id, title string) Track {
	return Track{Platform: "wy", PlatformID: id, Title: title, Artists: []string{"周杰伦"}}
}

func item(id, title string) PlaylistItem {
	return PlaylistItem{GUID: "guid-" + id, Track: track(id, title)}
}

func TestSharedPlaylistAddIsIdempotent(t *testing.T) {
	s := newSharedStore(t)
	n1, err := s.AddSharedPlaylistTracks("pl1", []PlaylistItem{item("1", "晴天"), item("2", "双截棍")})
	if err != nil || n1 != 2 {
		t.Fatalf("first add = %d, %v; want 2, nil", n1, err)
	}
	n2, err := s.AddSharedPlaylistTracks("pl1", []PlaylistItem{item("1", "晴天"), item("3", "七里香")})
	if err != nil || n2 != 1 {
		t.Fatalf("second add = %d, %v; want 1, nil", n2, err)
	}
	got := s.SharedPlaylistTracks("pl1")
	if len(got) != 3 {
		t.Fatalf("shared tracks = %d; want 3", len(got))
	}
	if c := s.SharedPlaylistTrackCounts()["pl1"]; c != 3 {
		t.Fatalf("count = %d; want 3", c)
	}
	// 另一个歌单不受影响
	if c := s.SharedPlaylistTrackCounts()["pl2"]; c != 0 {
		t.Fatalf("pl2 count = %d; want 0", c)
	}
}

func TestSharedPlaylistRemoveAndNotReadded(t *testing.T) {
	s := newSharedStore(t)
	if _, err := s.AddSharedPlaylistTracks("pl1", []PlaylistItem{item("1", "a"), item("2", "b")}); err != nil {
		t.Fatal(err)
	}
	n, err := s.RemoveSharedPlaylistTracks("pl1", []string{"guid-1"})
	if err != nil || n != 1 {
		t.Fatalf("remove = %d, %v; want 1, nil", n, err)
	}
	if got := s.SharedPlaylistTracks("pl1"); len(got) != 1 || got[0].GUID != "guid-2" {
		t.Fatalf("after remove = %+v; want only guid-2", got)
	}
	// 下一轮推送又塞回来 —— 用户删过的那条要被挡住
	if _, err := s.AddSharedPlaylistTracks("pl1", []PlaylistItem{item("1", "a"), item("3", "c")}); err != nil {
		t.Fatal(err)
	}
	if got := s.SharedPlaylistTracks("pl1"); len(got) != 2 {
		t.Fatalf("after re-add = %d; want 2 (removed one must stay out)", len(got))
	}
	if !s.SharedPlaylistRemoved("pl1")["guid-1"] {
		t.Fatal("removed set missing guid-1")
	}
}

func TestDropSharedPlaylist(t *testing.T) {
	s := newSharedStore(t)
	if _, err := s.AddSharedPlaylistTracks("pl1", []PlaylistItem{item("1", "a")}); err != nil {
		t.Fatal(err)
	}
	if err := s.DropSharedPlaylist("pl1"); err != nil {
		t.Fatal(err)
	}
	if got := s.SharedPlaylistTracks("pl1"); len(got) != 0 {
		t.Fatalf("after drop = %d; want 0", len(got))
	}
	// 删掉之后重新加同一条应该可以（Removed 也一起清掉了）
	if n, err := s.AddSharedPlaylistTracks("pl1", []PlaylistItem{item("1", "a")}); err != nil || n != 1 {
		t.Fatalf("re-add after drop = %d, %v; want 1, nil", n, err)
	}
}

func TestAllTracksIncludesSharedBucket(t *testing.T) {
	s := newSharedStore(t)
	if _, err := s.AddSharedPlaylistTracks("pl1", []PlaylistItem{item("1", "a")}); err != nil {
		t.Fatal(err)
	}
	all := s.AllTracks()
	if len(all) != 1 || all[0].RealID() != track("1", "a").RealID() {
		t.Fatalf("AllTracks = %+v; want the shared track", all)
	}
}
