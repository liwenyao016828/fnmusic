package assistant

// Exposure 一个能力对助手的暴露方式。
//
// 为什么区分「能否被调用」与「调用前要不要确认」：两者是不同的问题。
// 前者是**界面策略**（用户可能只想要一个只读查询助手），后者是**操作风险**
// （用户不可能期待每次下载都被问一遍）。
type Exposure string

const (
	// ExposureAuto 自动调用：只读、可恢复，或用户显式开启 FullAuto 时的写入。
	ExposureAuto Exposure = "auto"

	// ExposureConfirm 调用前需要用户确认。
	// 用户在助手里看到一张确认卡，点击后才真正执行。
	ExposureConfirm Exposure = "confirm"

	// ExposureProtected 不向助手暴露（既不自动调用，也不出现在可调用清单里）。
	// 用于凭证类配置（API Key 等）——助手**永远**不能读写它们。
	ExposureProtected Exposure = "protected"

	// ExposureInternal 内部标记：可见但不可调用（只读界面参考，不属于对话范围）。
	ExposureInternal Exposure = "internal"
)

// Risk 一个能力操作的风险等级。
type Risk string

const (
	// RiskRead 纯读取，无副作用。
	RiskRead Risk = "read"

	// RiskReversibleWrite 有副作用但可逆（改名、下载、开关配置）。
	RiskReversibleWrite Risk = "reversible_write"

	// RiskDestructiveWrite 不可逆写入（删除、清空历史、改凭证）。
	RiskDestructiveWrite Risk = "destructive_write"

	// RiskSensitive 涉及凭证/密钥等敏感信息。
	RiskSensitive Risk = "sensitive"
)

// ExecutionMode 一次对话（或整个助手）的执行模式。
type ExecutionMode string

const (
	// ModeReadOnly 只读模式：只有 ExposureAuto + RiskRead 的能力可自动调用。
	ModeReadOnly ExecutionMode = "read_only"

	// ModeConfirmWrites 默认：写入类能力需要确认卡。
	ModeConfirmWrites ExecutionMode = "confirm_writes"

	// ModeFullAuto 全自动：允许自动调用全部未被 Protected 的能力。
	// ⚠️ 用户主动开启，界面要写清后果。
	ModeFullAuto ExecutionMode = "full_auto"
)

// Decision 策略裁决结果。
type Decision string

const (
	// DecisionAllowed 直接执行。
	DecisionAllowed Decision = "allowed"

	// DecisionConfirmationNeeded 需要确认卡。
	DecisionConfirmationNeeded Decision = "confirmation_needed"

	// DecisionBlocked 不允许执行（Protected 能力 / 不在目录里 / 能力被界面关掉）。
	DecisionBlocked Decision = "blocked"
)

// ValidMode 执行模式合法性。
func ValidMode(m ExecutionMode) bool {
	switch m {
	case ModeReadOnly, ModeConfirmWrites, ModeFullAuto:
		return true
	}
	return false
}

// APICall 一次能力调用请求。
//
// Name 是 catalog 里登记的能力名（工具名），Args 是模型给出的参数
// （OpenAI tool_calls 的 arguments），Confirmed 表示用户是否已通过确认卡放行。
type APICall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Args      map[string]any `json:"args"`
	Confirmed bool           `json:"confirmed"`
}
