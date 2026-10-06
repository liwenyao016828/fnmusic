package config

import (
	"sync/atomic"
	"testing"
	"time"
)

// `ConfigManager.OnUpdate` 是「保存之后」的通知钩子（v2.1.83 新增）。
//
// 加它的原因：有些能力不是「读一下配置就行」，而是要在配置变化时**动起来** ——
// musicdl sidecar 要按开关拉起 / 停掉进程。没有钩子，那些能力只能「改完重启」，
// 而用户明确的偏好是「开关点了就该立刻生效」。
//
// 这一组钉三件事，都是**真踩过**的：
//  1. 钩子真的被调，且拿到的是**新**配置；
//  2. 钩子里再读 / 再写配置**不能死锁**（Go 的 RWMutex 不可重入 —— 第一版把钩子
//     放在持锁时调用，`pkg/config` 整包 60 秒超时）；
//  3. 钩子里 panic 不能把保存弄坏（此刻函数正在返回，panic 会直接崩进程）。

func TestOnUpdateFiresWithNewConfig(t *testing.T) {
	cm, err := NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatal(err)
	}

	var got atomic.Value
	var calls atomic.Int32
	cm.OnUpdate(func(c AppConfig) {
		calls.Add(1)
		got.Store(c.VisualizerMode)
	})

	if err := cm.Update(AppConfig{VisualizerMode: "bars"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("钩子该被调 1 次，实际 %d", calls.Load())
	}
	if v, _ := got.Load().(string); v != "bars" {
		t.Fatalf("钩子拿到的该是**新**配置，得到 %q", v)
	}

	// 多次保存 → 多次通知
	if err := cm.Update(AppConfig{VisualizerMode: "wave"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("每次保存都该通知，实际 %d 次", calls.Load())
	}
}

func TestOnUpdateHookCanReadAndWriteConfig(t *testing.T) {
	// ⚠️ 这条是那次死锁的回归测试。
	//
	// 钩子在持锁时被调 → 钩子里 `cm.Get()` 想拿读锁 → 永远等不到（写锁是自己的）。
	// 用超时兜底：真死锁时这条用例会失败而不是把整包挂到超时。
	cm, err := NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var reentered atomic.Bool
	cm.OnUpdate(func(AppConfig) {
		// 读：这是最常见的用法（「新配置是什么」→ 决定要不要起进程）
		_ = cm.Get()
		// 写：只做一次，否则会无限递归（每次写又触发钩子）
		if reentered.CompareAndSwap(false, true) {
			_ = cm.Update(AppConfig{PreferQuality: "flac"})
		}
		close(done)
	})

	if err := cm.Update(AppConfig{VisualizerMode: "bars"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("钩子里读/写配置时死锁了（回调必须在解锁之后才调）")
	}
	if cm.Get().PreferQuality != "flac" {
		t.Fatalf("钩子里的那次保存该生效，得到 %q", cm.Get().PreferQuality)
	}
}

func TestOnUpdateHookPanicDoesNotBreakSave(t *testing.T) {
	cm, err := NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatal(err)
	}
	cm.OnUpdate(func(AppConfig) { panic("钩子炸了") })

	// 保存本身必须成功（配置已经写进文件了，那才是这次调用的承诺），
	// 而且进程不能因为这个 panic 崩掉。
	if err := cm.Update(AppConfig{VisualizerMode: "bars"}); err != nil {
		t.Fatalf("钩子 panic 不该让保存失败：%v", err)
	}
	if cm.Get().VisualizerMode != "bars" {
		t.Fatal("配置该已经生效")
	}

	// 一个钩子炸了，后面的钩子仍要被调（一个坏订阅者不该拖垮别的）
	second := false
	cm.OnUpdate(func(AppConfig) { second = true })
	if err := cm.Update(AppConfig{VisualizerMode: "wave"}); err != nil {
		t.Fatal(err)
	}
	if !second {
		t.Fatal("前一个钩子 panic 之后，后面的钩子仍该被调")
	}
}

func TestOnUpdateIgnoresNilHook(t *testing.T) {
	cm, err := NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatal(err)
	}
	cm.OnUpdate(nil) // 不该 panic
	if err := cm.Update(AppConfig{VisualizerMode: "bars"}); err != nil {
		t.Fatal(err)
	}
}
