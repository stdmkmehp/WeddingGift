package report

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMaskKeyInput(t *testing.T) {
	cases := map[string]string{
		"20260001": "20****01",
		"12345":    "12****45",
		"1234":     "****",
		"1":        "****",
		"":         "（空）",
		"2026-001": "20****01", // 非纯数字输入同样只露首尾
	}
	for in, want := range cases {
		got := MaskKeyInput(in)
		if got != want {
			t.Errorf("MaskKeyInput(%q) = %q, want %q", in, got, want)
		}
		if in != "" && strings.Contains(got, in) {
			t.Errorf("MaskKeyInput(%q) = %q 不应完整包含原始输入", in, got)
		}
	}
}

func failureAt(sec int) LoginFailure {
	return LoginFailure{At: time.Unix(1700000000+int64(sec), 0).UTC(), Outcome: FailureKeyUnknown}
}

// TestFailureLogOverflow 覆盖核心语义：上限 100；插入第 101 条触发一次溢出
// 回调（携带全部 101 条快照），随后清理最早 50 条、保留最新 51 条。
func TestFailureLogOverflow(t *testing.T) {
	l := NewFailureLog(100, 50)

	var mu sync.Mutex
	var snapshots [][]LoginFailure
	l.OnOverflow = func(snapshot []LoginFailure) {
		mu.Lock()
		snapshots = append(snapshots, snapshot)
		mu.Unlock()
	}

	for i := 0; i < 100; i++ {
		l.Add(failureAt(i))
	}
	if l.Len() != 100 {
		t.Fatalf("100 条时不应溢出，实际 %d", l.Len())
	}

	l.Add(failureAt(100)) // 第 101 条触发溢出
	// 溢出回调异步执行，等待其完成。
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(snapshots)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("溢出回调未被触发")
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	snapshot := snapshots[0]
	mu.Unlock()
	if len(snapshot) != 101 {
		t.Errorf("溢出快照应为全部 101 条，实际 %d", len(snapshot))
	}
	if l.Len() != 51 {
		t.Fatalf("溢出后应保留最新 51 条（清理最早 50 条），实际 %d", l.Len())
	}
	// 保留的应是最新的 51 条（原第 50~100 条）。
	kept := l.Snapshot()
	first := kept[0].At.Unix() - 1700000000
	last := kept[len(kept)-1].At.Unix() - 1700000000
	if first != 50 || last != 100 {
		t.Errorf("清理应保留最新条目，实际范围为 %d ~ %d", first, last)
	}
}

// TestFailureLogDrain 日报成功发送后 Drain 取出全部并清空（每条失败只汇报一次）。
func TestFailureLogDrain(t *testing.T) {
	l := NewFailureLog(100, 50)
	l.Add(failureAt(1))
	l.Add(failureAt(2))

	out := l.Drain()
	if len(out) != 2 || l.Len() != 0 {
		t.Fatalf("Drain 应取出全部并清空，实际取出 %d、剩余 %d", len(out), l.Len())
	}
	if got := l.Drain(); len(got) != 0 {
		t.Errorf("空缓冲 Drain 应为空，实际 %d", len(got))
	}
}

// TestFailureLogNoHandler 未设置回调时溢出只清理、不 panic。
func TestFailureLogNoHandler(t *testing.T) {
	l := NewFailureLog(3, 1)
	for i := 0; i < 4; i++ {
		l.Add(failureAt(i))
	}
	if l.Len() != 3 {
		t.Errorf("溢出后应为 limit-evict=2+1=3... 实际 %d", l.Len())
	}
}

func TestFailureLabelAndRender(t *testing.T) {
	for outcome, want := range map[string]string{
		FailureInvalidInput:      "输入格式不正确",
		FailureNameUnknown:       "名字无效（无匹配）",
		FailureKeyUnknown:        "密钥无效或已停用",
		FailureKeyBoundElsewhere: "密钥已绑定其他同行组",
	} {
		if got := failureLabel(outcome); got != want {
			t.Errorf("failureLabel(%q) = %q, want %q", outcome, got, want)
		}
	}
}
