package organize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Journal 一次整理的完整撤销记录（落盘版）。
//
// 为什么落盘而不是只放内存：整理是「搬文件」这种不可逆动作，用户很可能
// 「整理完发现不对 → 关掉页面 → 想起来要撤回」。只放内存的话重启应用就没了，
// 用户只能自己去 NAS 上把文件搬回来 —— 那比没有撤销功能还糟。
type Journal struct {
	CreatedAt time.Time `json:"created_at"`
	Root      string    `json:"root"`
	// Template 当时的模板：告诉用户「这批文件是被哪套规则搬的」。
	Template Template `json:"template"`
	// Moves 反向移动记录（From = 现在的位置，To = 原来的位置）。
	Moves []Move `json:"moves"`
	// Moved 当时搬动的文件数（含附属文件），给界面显示。
	Moved int `json:"moved"`
}

// journalFileName 撤销记录的文件名（放在数据目录里）。
const journalFileName = "organize_journal.json"

// journalMaxRuns 撤销记录最多保留几批。
//
// 撤销是「一次撤一批」，用户可能连着整理好几轮都改主意。老做法只留最后一次，
// 改主意两次就没得撤了；这里留一串（最新的在最后），可以一轮一轮往回撤。
// 上限是为了不让这个文件无限长 —— 每批几万个 Move 的记录攒起来能有几十兆。
const journalMaxRuns = 20

// journalFile 是撤销记录文件的内容：一条条整理批次，**最新的在最后**。
type journalFile struct {
	Runs []Journal `json:"runs"`
}

// SaveJournal 把撤销记录追加到数据目录里的撤销链末尾。
//
// 写法：先写临时文件再 rename —— 中途断电/被杀不会留下半截 JSON
// （半截 JSON 读出来是解析错误，用户会以为「撤销功能坏了」）。
func SaveJournal(dataDir string, j Journal) error {
	chain := loadJournalChain(dataDir)
	return writeJournalChain(dataDir, append(chain, j))
}

// ReplaceLastJournal 用新的记录覆盖链上的最后一条。
//
// 执行整理时是「先写计划版、成功后换成真实结果」，两次写的是同一批，
// 所以第二次要覆盖而不是追加（否则链里会留下两批一模一样的记录）。
func ReplaceLastJournal(dataDir string, j Journal) error {
	chain := loadJournalChain(dataDir)
	if len(chain) == 0 {
		return SaveJournal(dataDir, j)
	}
	chain[len(chain)-1] = j
	return writeJournalChain(dataDir, chain)
}

// DropLastJournal 丢掉链上的最后一条（比如计划写了但一个文件都没搬成）。
func DropLastJournal(dataDir string) error {
	chain := loadJournalChain(dataDir)
	if len(chain) == 0 {
		return nil
	}
	chain = chain[:len(chain)-1]
	if len(chain) == 0 {
		return ClearJournal(dataDir)
	}
	return writeJournalChain(dataDir, chain)
}

// LoadJournal 读取最近一次整理的撤销记录。
//
// 没有记录时返回 ok=false（不是错误 —— 用户还没整理过而已）。
func LoadJournal(dataDir string) (Journal, bool) {
	chain := loadJournalChain(dataDir)
	if len(chain) == 0 {
		return Journal{}, false
	}
	last := chain[len(chain)-1]
	if len(last.Moves) == 0 {
		return Journal{}, false
	}
	return last, true
}

// LoadJournals 读取全部可撤销的批次，**最新的在最后**。
func LoadJournals(dataDir string) []Journal {
	chain := loadJournalChain(dataDir)
	out := make([]Journal, 0, len(chain))
	for _, j := range chain {
		if len(j.Moves) > 0 {
			out = append(out, j)
		}
	}
	return out
}

// PopJournal 取出并移除最近一次整理的撤销记录。
//
// 撤销成功后这一批就不该再撤第二次（文件已经回到原位，再撤一次会把它们
// 搬去「原来的位置」——而那已经是原位，等于没撤但记录没了）。
func PopJournal(dataDir string) (Journal, bool) {
	chain := loadJournalChain(dataDir)
	if len(chain) == 0 {
		return Journal{}, false
	}
	last := chain[len(chain)-1]
	chain = chain[:len(chain)-1]
	if len(chain) == 0 {
		if err := ClearJournal(dataDir); err != nil {
			return Journal{}, false
		}
	} else if err := writeJournalChain(dataDir, chain); err != nil {
		return Journal{}, false
	}
	if len(last.Moves) == 0 {
		return Journal{}, false
	}
	return last, true
}

// ClearJournal 清空全部撤销记录。
func ClearJournal(dataDir string) error {
	if dataDir == "" {
		return os.ErrInvalid
	}
	err := os.Remove(filepath.Join(dataDir, journalFileName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loadJournalChain 读出撤销链（读不出来就当没有）。
//
// 兼容旧格式：早期版本写的是**一条**记录（顶层就有 moves 字段），不是数组。
// 读的时候按「有没有 runs 字段」区分，老文件照样能用。
func loadJournalChain(dataDir string) []Journal {
	if dataDir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, journalFileName))
	if err != nil {
		return nil
	}
	var file journalFile
	if err := json.Unmarshal(raw, &file); err == nil && file.Runs != nil {
		return file.Runs
	}
	var old Journal
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil
	}
	if len(old.Moves) == 0 {
		return nil
	}
	return []Journal{old}
}

// writeJournalChain 把撤销链写回文件（原子写）。
func writeJournalChain(dataDir string, chain []Journal) error {
	if dataDir == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	for i := range chain {
		if chain[i].CreatedAt.IsZero() {
			chain[i].CreatedAt = time.Now()
		}
	}
	if len(chain) > journalMaxRuns {
		chain = chain[len(chain)-journalMaxRuns:]
	}
	raw, err := json.MarshalIndent(journalFile{Runs: chain}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, journalFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PlannedJournal 生成「假设这份方案全部搬成功」的撤销记录。
//
// 为什么需要它：Apply 是逐文件搬的，正式记录在**全部搬完之后**才写；
// 中途崩溃（断电、进程被杀、跨设备复制校验失败后用户强退）时那份记录根本
// 不存在，用户就失去了撤回依据 —— 只能自己去 NAS 上人工搬几千个文件。
// 所以执行前先按计划写一份「反向记录」，成功后再用真实结果覆盖它。
// 计划里凡是没搬成的条目，撤销时会因为源文件还在原位而自然跳过（计入失败）。
func PlannedJournal(plan Plan) Journal {
	j := Journal{
		CreatedAt: time.Now(),
		Root:      plan.Root,
		Template:  plan.Template,
		Moves:     make([]Move, 0, len(plan.Items)),
	}
	for _, item := range plan.Items {
		if item.Status != "move" {
			continue
		}
		for _, mv := range item.Moves {
			j.Moves = append(j.Moves, Move{From: mv.To, To: mv.From, Kind: mv.Kind})
			j.Moved++
		}
	}
	return j
}
