package intercept

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"fn-lx-player/pkg/search"
)

// 「参与搜索合并的平台」现在可以**现取**（`Config.PlatformsFunc`，v2.1.85）。
//
// 为什么需要：musicdl 外挂平台是用户开关决定的 —— 打开之后咪咕 / 千千 / B站
// 也该出现在飞牛官方页面的搜索里。构造时定死的话，用户开完开关还得重启应用。
//
// 这两条用例盯的是「现取真的生效了」与「没给函数时的回落」。

func TestSearchMergesPlatformsFromFunc(t *testing.T) {
	h := newHarness(t)

	// 记录搜索器被哪些平台问过（harness 默认的 searcher 忽略平台参数）
	var mu sync.Mutex
	seen := map[string]int{}
	h.it.searcher = func(_ string, platform string, _, _ int) []search.UnifiedSong {
		mu.Lock()
		seen[platform]++
		mu.Unlock()
		return nil
	}

	// 静态列表只有 wy；函数给出 wy + mg → 合并时要按**函数**那串来
	h.it.cfg.PlatformsFunc = func() []string { return []string{"wy", "mg"} }
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	h.do(http.MethodGet, "/music/api/v1/search/track?keyword=测试&page=1&size=50", "", nil)

	mu.Lock()
	defer mu.Unlock()
	if seen["mg"] != 1 {
		t.Fatalf("外挂平台该被搜到（开关打开就该出现在官方页搜索里），实际 %v", seen)
	}
	if seen["wy"] != 1 {
		t.Fatalf("默认平台也该照常搜，实际 %v", seen)
	}
	if seen["tx"] != 0 {
		t.Fatalf("函数没给的平台不该被搜，实际 %v", seen)
	}
}

func TestSearchPlatformsFallBackWhenFuncEmpty(t *testing.T) {
	h := newHarness(t)

	var mu sync.Mutex
	seen := map[string]int{}
	h.it.searcher = func(_ string, platform string, _, _ int) []search.UnifiedSong {
		mu.Lock()
		seen[platform]++
		mu.Unlock()
		return nil
	}
	// 返回空 → 回落到静态列表（harness 给的是 wy）
	h.it.cfg.PlatformsFunc = func() []string { return nil }
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	h.do(http.MethodGet, "/music/api/v1/search/track?keyword=测试&page=1&size=50", "", nil)

	mu.Lock()
	defer mu.Unlock()
	if seen["wy"] != 1 {
		t.Fatalf("函数返回空时该回落到静态列表，实际 %v", seen)
	}
	if seen["mg"] != 0 {
		t.Fatalf("不该凭空多出平台，实际 %v", seen)
	}
}

func TestDefaultPlatformsIsACopy(t *testing.T) {
	// main.go 会在这份默认之上追加外挂平台 —— 必须给拷贝，否则改到全局默认，
	// 下一个调用方（或别的用例）就跟着变。
	got := DefaultPlatforms()
	if len(got) == 0 {
		t.Fatal("默认平台不该是空的")
	}
	got[0] = "mutated"
	if DefaultPlatforms()[0] == "mutated" {
		t.Fatal("DefaultPlatforms 必须返回拷贝，不能漏出内部切片")
	}
}

func TestSearchDoesNotWaitForSlowPlatform(t *testing.T) {
	// 这条守的是 v2.1.85 真机上暴露的问题：合并是 `wg.Wait()` —— **等所有平台**，
	// 而 musicdl 的千千实测会跑满 sidecar 的单源预算（十几秒）。没有这道闸，
	// 打开外挂音源之后飞牛里每次搜索都要等二十多秒。
	//
	// 把「等搜索」的上限调小来测（真值 3 秒，干等不值当）。
	oldWait := onlineSearchWait
	onlineSearchWait = 300 * time.Millisecond
	t.Cleanup(func() { onlineSearchWait = oldWait })

	h := newHarness(t)
	// 登记「可解析」，否则合并那一步会把它过滤掉（合并只收能播的在线曲目）
	h.setOnline(map[string]bool{"1": true}, song("1", "快的平台", "甲"))
	h.it.cfg.PlatformsFunc = func() []string { return []string{"wy", "mg"} }
	h.it.searcher = func(_ string, platform string, _, _ int) []search.UnifiedSong {
		if platform == "mg" {
			time.Sleep(3 * time.Second) // 假装千千
		}
		return []search.UnifiedSong{song("1", "快的平台", "甲")}
	}
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	start := time.Now()
	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=测试&page=1&size=50", "", nil)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("慢平台不该拖住整次搜索（等搜索上限 300ms），实际等了 %v", elapsed)
	}
	// 快平台的结果照常进列表 —— 超时只丢慢的那个，不是整批不要
	d := dataOf(t, w)
	if list, _ := d["list"].([]any); len(list) == 0 {
		t.Fatal("快平台的结果该照常返回")
	}
}

func TestSearchStillResolvesAfterBudgetExpires(t *testing.T) {
	// ⚠️ 这条是 v2.1.86 真机上那个「一首在线歌都不出」的回归测试。
	//
	// 现象：搜索 8 秒后返回，本地歌在、**在线歌一首都没有**。根因是等搜索把
	// `onlineSearchBudget` 用光了，紧接着拿那个**已取消**的 ctx 去 `ResolveMany`
	// —— 解析全军覆没，能播的一个都筛不出来。修法是解析用自己的 context。
	//
	// 复现条件：总预算比「等搜索」先到期。这里把两者都调小，让慢平台把总预算耗掉。
	// ⚠️ 参数要选准：**等搜索必须比总预算晚到期**，解析才会撞上已过期的 ctx。
	// （第一次写成 wait 150ms / budget 300ms，等搜索在预算内就返回了 —— 解析
	//  用的 ctx 还好好的，于是这条用例连变异都杀不掉，等于白写。）
	oldBudget, oldWait := onlineSearchBudget, onlineSearchWait
	onlineSearchBudget = 300 * time.Millisecond
	onlineSearchWait = 400 * time.Millisecond
	t.Cleanup(func() {
		onlineSearchBudget, onlineSearchWait = oldBudget, oldWait
	})

	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true}, song("1", "快的平台", "甲"))
	h.it.cfg.PlatformsFunc = func() []string { return []string{"wy", "mg"} }
	h.it.searcher = func(_ string, platform string, _, _ int) []search.UnifiedSong {
		if platform == "mg" {
			time.Sleep(600 * time.Millisecond) // 拖到总预算过期
		}
		return []search.UnifiedSong{song("1", "快的平台", "甲")}
	}
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=测试&page=1&size=50", "", nil)
	d := dataOf(t, w)
	list, _ := d["list"].([]any)
	if len(list) == 0 {
		t.Fatal("总预算过期后，快平台的可播曲目仍该出现在结果里（解析必须用自己的 context）")
	}
}
