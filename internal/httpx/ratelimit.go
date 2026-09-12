package httpx

import (
	"sync"
	"time"
)

// RateLimiter 是技术架构 §10.3 要求的简单有界内存限流：
// 按来源 IP 与失败密钥摘要双维度统计失败次数，滑动窗口内超过阈值即拒绝。
// 应用重启后状态清空是已接受的语义；不引入 Redis。
type RateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	ipLimit  int
	keyLimit int
	now      func() time.Time

	ipHits  map[string][]time.Time
	keyHits map[string][]time.Time
}

// NewRateLimiter 构造限流器。window 为滑动窗口；ipLimit/keyLimit 为窗口内
// 允许的最大失败次数（达到即拒绝后续尝试）。
func NewRateLimiter(window time.Duration, ipLimit, keyLimit int) *RateLimiter {
	return &RateLimiter{
		window:   window,
		ipLimit:  ipLimit,
		keyLimit: keyLimit,
		now:      time.Now,
		ipHits:   make(map[string][]time.Time),
		keyHits:  make(map[string][]time.Time),
	}
}

// maxEntries 是单维度 map 的硬上限，防止恶意构造的键撑爆内存；
// 触顶时清空该维度（接受误伤，服务规模下极不可能触达）。
const maxEntries = 4096

// AllowLogin 报告当前 IP + 密钥摘要组合是否允许继续尝试登录。
// keyDigest 可空（格式非法的密钥没有摘要，只按 IP 统计）。
func (l *RateLimiter) AllowLogin(ip string, keyDigest []byte) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if count(l.prune(l.ipHits, ip, now)) >= l.ipLimit {
		return false
	}
	if len(keyDigest) > 0 && count(l.prune(l.keyHits, string(keyDigest), now)) >= l.keyLimit {
		return false
	}
	return true
}

// RecordLoginFailure 记录一次登录失败。调用方只在失败时调用；
// 成功不清零计数（婚礼现场共享 NAT 下阈值已足够宽松，无需成功豁免）。
func (l *RateLimiter) RecordLoginFailure(ip string, keyDigest []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.ipHits[ip] = append(l.prune(l.ipHits, ip, now), now)
	if len(keyDigest) > 0 {
		k := string(keyDigest)
		l.keyHits[k] = append(l.prune(l.keyHits, k, now), now)
	}
	if len(l.ipHits) > maxEntries {
		l.ipHits = make(map[string][]time.Time)
	}
	if len(l.keyHits) > maxEntries {
		l.keyHits = make(map[string][]time.Time)
	}
}

// prune 惰性剔除窗口外的历史记录；键无剩余记录时删除键，保持 map 有界。
func (l *RateLimiter) prune(m map[string][]time.Time, key string, now time.Time) []time.Time {
	hits := m[key]
	keep := hits[:0]
	for _, t := range hits {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(m, key)
		return nil
	}
	m[key] = keep
	return keep
}

func count(hits []time.Time) int { return len(hits) }
