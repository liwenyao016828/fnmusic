package config

import (
	"encoding/json"
	"testing"
)

// 「音源页逐个启停生效」这条验收要求，卡在一个 nil 与 [] 的区别上。
//
// 用 len==0 判「没配过」会把**用户显式全关**也当成「没配过」，于是悄悄回到默认源
// （mg）—— 用户明明关了，搜索里还有咪咕的结果。真机验收时抓到的就是这个。
func TestMusicDLActiveSourcesDistinguishesNeverSetFromAllOff(t *testing.T) {
	var never AppConfig
	if got := never.MusicDLActiveSources(); len(got) != 1 || got[0] != "mg" {
		t.Fatalf("从没配过（nil）该用默认源：%v", got)
	}
	allOff := AppConfig{MusicDLSources: []string{}}
	if got := allOff.MusicDLActiveSources(); len(got) != 0 {
		t.Fatalf("⚠️ 显式全关就该一个都不搜，得到 %v", got)
	}
	some := AppConfig{MusicDLSources: []string{"MG", " bq ", "", "mg"}}
	got := some.MusicDLActiveSources()
	if len(got) != 2 || got[0] != "mg" || got[1] != "bq" {
		t.Fatalf("该归一化（小写/去空白/去重）：%v", got)
	}
}

// 「全关」必须能**活过重启**：序列化时不能把 [] 省成缺字段，否则读回来是 nil，
// 又变回默认源 —— 用户会以为自己的设置在重启后自己跑回去了。
func TestMusicDLSourcesAllOffSurvivesJSONRoundTrip(t *testing.T) {
	allOff := AppConfig{MusicDLSources: []string{}}
	raw, err := json.Marshal(allOff)
	if err != nil {
		t.Fatal(err)
	}
	var back AppConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.MusicDLSources == nil {
		t.Fatalf("⚠️ 显式全关序列化后丢了（raw=%s）—— 重启会变回默认源", raw)
	}
	if got := back.MusicDLActiveSources(); len(got) != 0 {
		t.Fatalf("往返之后仍该是「全关」：%v", got)
	}

	// 反过来：从没配过（nil）往返之后仍是 nil（仍走默认源）
	never := AppConfig{}
	raw2, _ := json.Marshal(never)
	var back2 AppConfig
	if err := json.Unmarshal(raw2, &back2); err != nil {
		t.Fatal(err)
	}
	if back2.MusicDLSources != nil {
		t.Fatalf("从没配过往返后该仍是 nil：%#v", back2.MusicDLSources)
	}
	if got := back2.MusicDLActiveSources(); len(got) != 1 {
		t.Fatalf("从没配过仍该用默认源：%v", got)
	}
}

// 走一遍 ConfigManager：显式全关 → 重新加载配置 → 仍然是全关。
func TestMusicDLAllOffPersistsThroughManager(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := cm.Get()
	c.MusicDLSources = []string{}
	if err := cm.Update(c); err != nil {
		t.Fatal(err)
	}
	if got := cm.Get().MusicDLActiveSources(); len(got) != 0 {
		t.Fatalf("更新之后就该是全关：%v", got)
	}
	cm2, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := cm2.Get().MusicDLActiveSources(); len(got) != 0 {
		t.Fatalf("⚠️ 重启（重新加载）之后仍是全关才对，得到 %v", got)
	}
}
