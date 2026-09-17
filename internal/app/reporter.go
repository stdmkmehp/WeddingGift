package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/weddinggift/wedding/internal/report"
	"github.com/weddinggift/wedding/internal/store"
)

// 登录失败内存缓冲的容量与溢出清理策略（2026-09-17 项目所有者确认）：
// 上限 100 条，超过即发告警邮件并清理最早 50 条，保证最新失败始终可记录。
const (
	failureLogLimit = 100
	failureLogEvict = 50
)

// reportWindow 是日报的滚动统计窗口宽度。
const reportWindow = 24 * time.Hour

// beijing 是固定的 UTC+8 调度时区（scratch 镜像无 tzdata，国内无夏令时）。
var beijing = time.FixedZone("CST", 8*3600)

// parseReportTime 解析 "HH:MM" 日报发送时刻（北京时间，要求零填充两位）。
func parseReportTime(s string) (hour, minute int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("日报发送时刻格式应为 HH:MM，实际 %q", s)
	}
	if hour, err = strconv.Atoi(parts[0]); err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("日报发送时刻小时非法: %q", s)
	}
	if minute, err = strconv.Atoi(parts[1]); err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("日报发送时刻分钟非法: %q", s)
	}
	return hour, minute, nil
}

// nextReportTime 计算 now 之后最近一次北京时间 hour:minute 的时刻
// （若 now 已过时则取明天）。返回 UTC 时刻。
func nextReportTime(now time.Time, hour, minute int) time.Time {
	bjNow := now.In(beijing)
	next := time.Date(bjNow.Year(), bjNow.Month(), bjNow.Day(), hour, minute, 0, 0, beijing)
	if !next.After(bjNow) {
		next = next.AddDate(0, 0, 1)
	}
	return next.UTC()
}

// startDailyReporter 启动 serve 进程内的日报调度：每天北京时间 hour:minute
// 收集滚动窗口统计与累积数据、取出内存缓冲中的全部登录失败，渲染并发送
// 日报邮件。日报必须随 serve 进程运行——登录失败缓冲只在内存中，
// 独立进程（cron + CLI）读不到，这是调度放在进程内的根本原因。
//
// 发送成功后才 Drain 缓冲（每条失败只在一封日报里汇报）；发送失败保留
// 缓冲并记错误日志，次日重试。进程重启/崩溃缓冲即清空（项目所有者明确接受）。
func startDailyReporter(ctx context.Context, db *store.DB, failLog *report.FailureLog, smtp report.SMTPConfig, at string, logger *slog.Logger) {
	hour, minute, err := parseReportTime(at)
	if err != nil {
		logger.Error("daily_report_disabled", slog.String("reason", err.Error()))
		return
	}
	go func() {
		for {
			next := nextReportTime(time.Now(), hour, minute)
			logger.Info("daily_report_scheduled", slog.String("next_at", next.In(beijing).Format("2006-01-02 15:04")))
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			summary, err := report.Collect(ctx, db, time.Now(), reportWindow)
			if err != nil {
				logger.Error("daily_report_failed", slog.String("stage", "collect"), slog.String("error", err.Error()))
				continue
			}
			// 先快照渲染再发送，成功后才 Drain：发送失败时记录保留到次日。
			failures := failLog.Snapshot()
			subject, body := report.RenderDaily(summary, failures)
			if err := report.SendMail(ctx, smtp, subject, body); err != nil {
				logger.Error("daily_report_failed", slog.String("stage", "send"), slog.String("error", err.Error()))
				continue
			}
			failLog.Drain()
			logger.Info("daily_report_sent",
				slog.String("subject", subject),
				slog.Int("failures", len(failures)),
			)
		}
	}()
}

// newOverflowHandler 构造登录失败缓冲的溢出告警回调：缓冲超过上限时
// 立即发送告警邮件（当前全部条目），发送失败只记日志（缓冲清理照常，
// 项目所有者接受内存数据可丢失）。
func newOverflowHandler(smtp report.SMTPConfig, logger *slog.Logger) func([]report.LoginFailure) {
	return func(snapshot []report.LoginFailure) {
		subject, body := report.RenderOverflowAlert(snapshot, failureLogLimit, failureLogEvict)
		// 溢出回调在独立 goroutine 中运行，无请求 ctx；给独立超时。
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := report.SendMail(ctx, smtp, subject, body); err != nil {
			logger.Error("login_failure_alert_failed", slog.String("error", err.Error()))
			return
		}
		logger.Info("login_failure_alert_sent", slog.Int("failures", len(snapshot)))
	}
}
