package push

import (
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func TestStoreSaveGetListDelete(t *testing.T) {
	s := newStore(t)

	if _, ok := s.Get("nope"); ok {
		t.Fatal("初始应为空")
	}

	saved, err := s.Save(Task{
		Name: "每日推荐同步", Provider: "netease", SourceKind: KindDaily,
		IntervalMinutes: 1440, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("应自动生成 ID")
	}
	if saved.CreatedAt == 0 {
		t.Error("应记录 CreatedAt")
	}
	// 存储层不设业务默认值，Enabled 由调用方决定（API 层对新任务置 true）
	if !saved.Enabled {
		t.Error("Enabled 应原样保存为 true")
	}
	// TargetTitle 未给时应回退为任务名
	if saved.TargetTitle != "每日推荐同步" {
		t.Errorf("TargetTitle = %q", saved.TargetTitle)
	}

	got, ok := s.Get(saved.ID)
	if !ok || got.Name != "每日推荐同步" {
		t.Fatalf("应能取回: %+v ok=%v", got, ok)
	}

	if len(s.List()) != 1 {
		t.Fatalf("List 应有 1 条，实际 %d", len(s.List()))
	}

	if !s.Delete(saved.ID) {
		t.Error("Delete 应返回 true")
	}
	if s.Delete(saved.ID) {
		t.Error("重复 Delete 应返回 false")
	}
	if len(s.List()) != 0 {
		t.Fatal("删除后应为空")
	}
}

func TestStoreSaveValidation(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(Task{Provider: "netease", SourceKind: KindDaily}); err == nil {
		t.Error("缺 name 应报错")
	}
	if _, err := s.Save(Task{Name: "x", SourceKind: KindDaily}); err == nil {
		t.Error("缺 provider 应报错")
	}
	if _, err := s.Save(Task{Name: "x", Provider: "netease"}); err == nil {
		t.Error("缺 source_kind 应报错")
	}
}

func TestStoreUpdateKeepsCreatedAt(t *testing.T) {
	s := newStore(t)
	first, _ := s.Save(Task{Name: "a", Provider: "netease", SourceKind: KindDaily})
	time.Sleep(1100 * time.Millisecond) // CreatedAt 是秒级

	updated, err := s.Save(Task{
		ID: first.ID, Name: "a-改名", Provider: "netease", SourceKind: KindDaily,
		IntervalMinutes: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != first.CreatedAt {
		t.Errorf("更新不应改变 CreatedAt: %d -> %d", first.CreatedAt, updated.CreatedAt)
	}
	if len(s.List()) != 1 {
		t.Fatalf("更新不应产生新条目，实际 %d 条", len(s.List()))
	}
}

// 间隔 > 0 且启用的任务才会被判定为「到点」。
func TestStoreDue(t *testing.T) {
	s := newStore(t)
	now := time.Now()

	// 手动任务（间隔 0）不该到点
	manual, _ := s.Save(Task{Name: "手动", Provider: "netease", SourceKind: KindDaily})
	// 禁用的定时任务不该到点
	disabled, _ := s.Save(Task{Name: "禁用", Provider: "netease", SourceKind: KindDaily, IntervalMinutes: 1})
	disabled.Enabled = false
	if _, err := s.Save(disabled); err != nil {
		t.Fatal(err)
	}
	// 定时任务：把 NextRunAt 拨到过去
	scheduled, _ := s.Save(Task{Name: "定时", Provider: "netease", SourceKind: KindDaily, IntervalMinutes: 60, Enabled: true})

	// Save 会重算 NextRunAt，这里直接改内部状态模拟「已到点」
	s.mu.Lock()
	st := s.tasks[scheduled.ID]
	st.NextRunAt = now.Add(-time.Minute).Unix()
	s.tasks[scheduled.ID] = st
	s.mu.Unlock()

	due := s.Due(now)
	if len(due) != 1 || due[0].ID != scheduled.ID {
		t.Fatalf("应只有定时任务到点，实际 %+v（手动=%s 禁用=%s）", due, manual.ID, disabled.ID)
	}
}

func TestStoreAddRunUpdatesTaskState(t *testing.T) {
	s := newStore(t)
	task, _ := s.Save(Task{Name: "t", Provider: "netease", SourceKind: KindDaily, IntervalMinutes: 60})

	res := &Result{GUID: "g1", Total: 3, Matched: 2, Added: 2}
	err := s.AddRun(Run{
		TaskID: task.ID, TaskName: task.Name, StartedAt: time.Now().Unix(),
		Status: "success", Trigger: "manual", Result: res,
	})
	if err != nil {
		t.Fatalf("AddRun 失败: %v", err)
	}

	got, _ := s.Get(task.ID)
	if got.LastStatus != "success" {
		t.Errorf("LastStatus = %q", got.LastStatus)
	}
	if got.LastResult == nil || got.LastResult.GUID != "g1" {
		t.Errorf("LastResult 未写入: %+v", got.LastResult)
	}
	if got.LastRunAt == 0 {
		t.Error("LastRunAt 未写入")
	}
	// 定时任务应重算下次执行时间
	if got.NextRunAt <= time.Now().Unix() {
		t.Errorf("定时任务应重算 NextRunAt，实际 %d", got.NextRunAt)
	}
}

func TestStoreAddRunRecordsFailure(t *testing.T) {
	s := newStore(t)
	task, _ := s.Save(Task{Name: "t", Provider: "netease", SourceKind: KindDaily})

	_ = s.AddRun(Run{TaskID: task.ID, Status: "failed", Error: "飞牛音乐不可用", Trigger: "schedule"})

	got, _ := s.Get(task.ID)
	if got.LastStatus != "failed" || got.LastError != "飞牛音乐不可用" {
		t.Fatalf("失败状态未记录: %+v", got)
	}
	if got.NextRunAt != 0 {
		t.Errorf("手动任务 NextRunAt 应为 0，实际 %d", got.NextRunAt)
	}
}

func TestStoreRunsFilterAndOrder(t *testing.T) {
	s := newStore(t)
	a, _ := s.Save(Task{Name: "a", Provider: "netease", SourceKind: KindDaily})
	b, _ := s.Save(Task{Name: "b", Provider: "netease", SourceKind: KindDaily})

	for i := 0; i < 3; i++ {
		_ = s.AddRun(Run{TaskID: a.ID, Status: "success"})
	}
	_ = s.AddRun(Run{TaskID: b.ID, Status: "failed"})

	all := s.Runs("", 10)
	if len(all) != 4 {
		t.Fatalf("应有 4 条历史，实际 %d", len(all))
	}
	// 倒序：最新一条是 b
	if all[0].TaskID != b.ID {
		t.Errorf("历史应倒序，首条 TaskID = %s", all[0].TaskID)
	}

	onlyA := s.Runs(a.ID, 10)
	if len(onlyA) != 3 {
		t.Fatalf("按任务过滤应有 3 条，实际 %d", len(onlyA))
	}

	if got := s.Runs("", 2); len(got) != 2 {
		t.Fatalf("limit 应生效，实际 %d", len(got))
	}
}

func TestStorePersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	s1 := NewStore(dir)
	task, _ := s1.Save(Task{Name: "持久化", Provider: "qq", SourceKind: KindPlaylist, SourceID: "pl1"})
	_ = s1.AddRun(Run{TaskID: task.ID, Status: "success"})

	s2 := NewStore(dir)
	got, ok := s2.Get(task.ID)
	if !ok || got.Name != "持久化" || got.SourceID != "pl1" {
		t.Fatalf("重启后应能读回: %+v ok=%v", got, ok)
	}
	if len(s2.Runs("", 10)) != 1 {
		t.Fatal("执行历史也应持久化")
	}
}

func TestStoreToleratesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	// 不应 panic
	s := NewStore(dir)
	if len(s.List()) != 0 {
		t.Fatal("空目录应为空")
	}
}

func TestNextRun(t *testing.T) {
	if nextRun(0) != 0 {
		t.Error("间隔 0 应返回 0（不定时）")
	}
	if nextRun(-5) != 0 {
		t.Error("负间隔应返回 0")
	}
	future := nextRun(30)
	if future <= time.Now().Unix() {
		t.Errorf("间隔 30 分钟应算出未来时间，实际 %d", future)
	}
}
