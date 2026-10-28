package assistant

// Policy 策略裁决：判断一次能力调用在当前执行模式下能否执行。
//
// 移植自 msf 的 assistant/policy.go，并按曲率的能力目录调整了措辞与规则。
//
// 核心规则：
//   - 能力不在目录里 / 被标记 Protected → Blocked（永不放行）；
//   - 能力被用户在界面关掉（enabled 黑名单命中）→ Blocked；
//   - 只读模式（ReadOnly）只放行 RiskRead 的能力，写入类一律拦；
//   - ConfirmWrites（默认）放行 Auto；Confirm 类能力必须 Confirmed=true，
//     否则 ConfirmationNeeded（挂起等确认卡）；
//   - FullAuto 放行全部（Protected 除外）。
//
// 两个曾经把助手卡死的语义坑，改这个函数前务必先读：
//  1. enabled 是「用户在界面关掉的能力」黑名单。nil 与空 map 都表示
//     「一项都没关」。早先写成 `enabled != nil && !enabled[name]`，
//     调用方传空 map（最自然的写法）时所有能力都被判 Blocked，
//     表现就是「点了确认毫无反应、界面也不报错」。
//  2. Confirm 类能力必须在「只读模式」和「确认检查」之前判定模式限制，
//     否则只读模式下点确认反而能执行写入类操作。
func Decide(capability *Capability, mode ExecutionMode, approved bool, enabled map[string]bool) Decision {
	if capability == nil {
		return DecisionBlocked
	}
	if capability.Exposure == ExposureProtected {
		return DecisionBlocked
	}
	if enabled[capability.Name] {
		// 用户在界面关掉了这项能力
		return DecisionBlocked
	}
	// 只读模式：除了纯读能力，其余一律拒绝（已确认也不行）
	if mode == ModeReadOnly && capability.Risk != RiskRead {
		return DecisionBlocked
	}
	switch capability.Exposure {
	case ExposureConfirm:
		if approved {
			return DecisionAllowed
		}
		return DecisionConfirmationNeeded
	case ExposureAuto:
		return DecisionAllowed
	default:
		return DecisionBlocked
	}
}
