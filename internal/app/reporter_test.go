package app

import (
	"testing"
	"time"
)

func TestParseReportTime(t *testing.T) {
	h, m, err := parseReportTime("07:17")
	if err != nil || h != 7 || m != 17 {
		t.Errorf("07:17 解析 = (%d, %d, %v)", h, m, err)
	}
	h, m, err = parseReportTime("23:59")
	if err != nil || h != 23 || m != 59 {
		t.Errorf("23:59 解析 = (%d, %d, %v)", h, m, err)
	}
	for _, bad := range []string{"", "7:17", "07-17", "24:00", "07:60", "ab:cd", "07:17:00"} {
		if _, _, err := parseReportTime(bad); err == nil {
			t.Errorf("%q 应解析失败", bad)
		}
	}
}

// TestNextReportTime 调度时刻按北京时间计算：当天未过取当天，已过取明天。
func TestNextReportTime(t *testing.T) {
	// 北京 2026-09-17 06:00（UTC 09-16 22:00）：当天 07:17 未到。
	got := nextReportTime(time.Date(2026, 9, 16, 22, 0, 0, 0, time.UTC), 7, 17)
	want := time.Date(2026, 9, 16, 23, 17, 0, 0, time.UTC) // 北京 09-17 07:17
	if !got.Equal(want) {
		t.Errorf("当天时刻 = %v, want %v", got, want)
	}

	// 北京 2026-09-17 07:17 整点（UTC 09-16 23:17）：恰不"之后"，取明天。
	got = nextReportTime(time.Date(2026, 9, 16, 23, 17, 0, 0, time.UTC), 7, 17)
	want = time.Date(2026, 9, 17, 23, 17, 0, 0, time.UTC) // 北京 09-18 07:17
	if !got.Equal(want) {
		t.Errorf("整点应取明天 = %v, want %v", got, want)
	}

	// 北京 2026-09-17 23:30（UTC 09-17 15:30）：已过，取明天。
	got = nextReportTime(time.Date(2026, 9, 17, 15, 30, 0, 0, time.UTC), 7, 17)
	want = time.Date(2026, 9, 17, 23, 17, 0, 0, time.UTC) // 北京 09-18 07:17
	if !got.Equal(want) {
		t.Errorf("已过应取明天 = %v, want %v", got, want)
	}
}
