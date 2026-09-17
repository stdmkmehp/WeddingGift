package report

import (
	"strings"
	"sync"
	"time"
)

// 登录失败类别（内存缓冲与日报/告警邮件用）。
const (
	// FailureInvalidInput 是输入格式不满足公开规则（空/超长名字、密钥非 8 位数字）。
	FailureInvalidInput = "invalid_input"
	// FailureNameUnknown 是登录名称无匹配（含成员不可登录、组已停用）。
	FailureNameUnknown = "name_unknown"
	// FailureKeyUnknown 是密钥摘要无匹配或已停用。
	FailureKeyUnknown = "key_unknown"
	// FailureKeyBoundElsewhere 是名称与密钥各自有效，但密钥已绑定其他同行组。
	FailureKeyBoundElsewhere = "key_bound_elsewhere"
)

// failureLabel 把失败类别渲染为面向项目所有者的中文说明。
func failureLabel(outcome string) string {
	switch outcome {
	case FailureInvalidInput:
		return "输入格式不正确"
	case FailureNameUnknown:
		return "名字无效（无匹配）"
	case FailureKeyUnknown:
		return "密钥无效或已停用"
	case FailureKeyBoundElsewhere:
		return "密钥已绑定其他同行组"
	default:
		return outcome
	}
}

// LoginFailure 是一条登录失败记录：当时的输入情况与失败类别。
// 内存数据，不落库；密钥只保留掩码，不保留明文。
type LoginFailure struct {
	// At 是失败发生时刻（UTC）。
	At time.Time
	// Outcome 是失败类别（FailureXxx 常量）。
	Outcome string
	// NameInput 是用户输入的原始名字（未规范化）。
	NameInput string
	// NameGroup 是该名字（规范化后）对应的启用同行组展示（如 "G001 王芳"）；
	// 无匹配为空串。
	NameGroup string
	// KeyMasked 是用户输入密钥的掩码（MaskKeyInput），永不保存明文。
	KeyMasked string
	// KeyLookedUp 表示输入密钥格式合法、已做摘要归属查询；
	// false（如密钥非 8 位数字）时 KeyRef/KeyBoundGroup 无意义。
	KeyLookedUp bool
	// KeyRef 是输入密钥摘要命中的真实密钥 external_ref；未命中为空串。
	KeyRef string
	// KeyBoundGroup 是命中密钥已绑定同行组的展示（如 "G003 陈强 & 刘洋 一家"）；
	// 未绑定/未命中为空串。
	KeyBoundGroup string
}

// MaskKeyInput 把用户输入的密钥渲染为可读但不泄漏完整值的掩码：
// 长度 ≥5 时保留首尾各 2 个字符（8 位密钥形如 12****78），更短则全部掩码。
// 空输入返回 "（空）"。永不返回原始输入。
func MaskKeyInput(input string) string {
	r := []rune(input)
	switch {
	case len(r) == 0:
		return "（空）"
	case len(r) < 5:
		return "****"
	default:
		return string(r[:2]) + "****" + string(r[len(r)-2:])
	}
}

// FailureLog 是登录失败的内存环形缓冲（不持久化，进程重启/崩溃即清空，
// 项目所有者明确接受该丢失风险）。
//
// 语义：容量上限 limit 条；Add 使条目数超过 limit 时触发一次溢出——
// 立即回调 OnOverflow（异步执行，携带当前全部条目快照，供发送告警邮件），
// 随后清理最早的 evict 条，保证最新失败始终能被记录。
// 日报成功发送后调用 Drain 取出并清空全部条目（每条失败只汇报一次）。
type FailureLog struct {
	mu    sync.Mutex
	items []LoginFailure

	limit int
	evict int

	// OnOverflow 是溢出回调；nil 时溢出只清理不通知。
	// 回调在独立 goroutine 中执行且彼此串行（overflowMu 保证），
	// 不阻塞登录请求路径。
	OnOverflow func(snapshot []LoginFailure)
	overflowMu sync.Mutex
}

// NewFailureLog 构造容量 limit、溢出后清理最早 evict 条的内存缓冲。
func NewFailureLog(limit, evict int) *FailureLog {
	return &FailureLog{limit: limit, evict: evict}
}

// Add 追加一条失败记录；触发溢出时异步回调 OnOverflow 并清理最早条目。
func (l *FailureLog) Add(f LoginFailure) {
	l.mu.Lock()
	l.items = append(l.items, f)
	if len(l.items) > l.limit {
		snapshot := make([]LoginFailure, len(l.items))
		copy(snapshot, l.items)
		l.items = append([]LoginFailure(nil), l.items[l.evict:]...)
		handler := l.OnOverflow
		l.mu.Unlock()
		if handler != nil {
			go func() {
				// 串行化溢出回调，避免并发溢出邮件交错。
				l.overflowMu.Lock()
				defer l.overflowMu.Unlock()
				handler(snapshot)
			}()
		}
		return
	}
	l.mu.Unlock()
}

// Snapshot 返回当前全部条目的副本（按时间升序），不清空。
func (l *FailureLog) Snapshot() []LoginFailure {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LoginFailure, len(l.items))
	copy(out, l.items)
	return out
}

// Drain 返回当前全部条目并清空缓冲（日报成功发送后调用：
// 每条失败只在一封日报里汇报，不重复占用次日版面）。
func (l *FailureLog) Drain() []LoginFailure {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.items
	l.items = nil
	return out
}

// Len 返回当前条目数（测试与日志用）。
func (l *FailureLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}

// renderFailures 把失败记录渲染为日报/告警邮件共用的明细行（北京时间，升序）。
func renderFailures(b *strings.Builder, failures []LoginFailure) {
	for _, f := range failures {
		name := f.NameInput
		if name == "" {
			name = "（空）"
		}
		if f.NameGroup != "" {
			name += "（属 " + f.NameGroup + "）"
		} else {
			name += "（无匹配）"
		}

		key := f.KeyMasked
		if f.KeyLookedUp {
			if f.KeyRef != "" {
				note := "有效 " + f.KeyRef
				if f.KeyBoundGroup != "" {
					note += "，已绑定 " + f.KeyBoundGroup
				} else {
					note += "，未绑定"
				}
				key += "（" + note + "）"
			} else {
				key += "（无匹配）"
			}
		}

		b.WriteString("  - ")
		b.WriteString(f.At.In(beijing).Format("01-02 15:04"))
		b.WriteString("｜名字 ")
		b.WriteString(name)
		b.WriteString("｜密钥 ")
		b.WriteString(key)
		b.WriteString("｜")
		b.WriteString(failureLabel(f.Outcome))
		b.WriteString("\n")
	}
}
