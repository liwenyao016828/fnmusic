package assistant

import "testing"

// enabled 是「用户在界面关掉的能力」黑名单：nil 与空 map 都必须表示
// 「一项都没关」。早先写成 `enabled != nil && !enabled[name]`，
// 传空 map 时所有能力都被判 Blocked —— 这正是「点确认没反应」的元凶之一。
func TestDecideEmptyDisabledMapMeansNothingDisabled(t *testing.T) {
	cap := FindCapability("download_song")
	if cap == nil {
		t.Fatal("download_song 能力缺失")
	}
	if d := Decide(cap, ModeConfirmWrites, true, map[string]bool{}); d != DecisionAllowed {
		t.Errorf("空黑名单 + 已确认 应放行，实际 %q", d)
	}
	if d := Decide(cap, ModeConfirmWrites, true, nil); d != DecisionAllowed {
		t.Errorf("nil 黑名单 + 已确认 应放行，实际 %q", d)
	}
	if d := Decide(cap, ModeConfirmWrites, false, map[string]bool{}); d != DecisionConfirmationNeeded {
		t.Errorf("未确认应挂起等确认卡，实际 %q", d)
	}
	if d := Decide(cap, ModeConfirmWrites, true, map[string]bool{"download_song": true}); d != DecisionBlocked {
		t.Errorf("用户显式关掉后应拦截，实际 %q", d)
	}
}

// 只读模式仍然要拦住写入类能力（确认过也不行）。
func TestDecideReadOnlyStillBlocksWrites(t *testing.T) {
	cap := FindCapability("download_song")
	if d := Decide(cap, ModeReadOnly, true, map[string]bool{}); d != DecisionBlocked {
		t.Errorf("只读模式下写入类应拦截，实际 %q", d)
	}
	read := FindCapability("health_status")
	if read == nil {
		t.Skip("health_status 能力缺失")
	}
	if d := Decide(read, ModeReadOnly, false, map[string]bool{}); d != DecisionAllowed {
		t.Errorf("只读能力在只读模式下应放行，实际 %q", d)
	}
}
