package main

import (
	"context"
	"crypto/rand"
	"math/big"
	"strings"
	"time"
)

// 拟人化节奏与状态。目标是把"秒回、无状态"的机器感换成
// "读完消息、想一下、打字"的真人节奏,以及跨消息延续的情绪底色。
// 全部行为可由 message_policy / group_chat_policy 关闭,且绝不影响
// 媒体产物、失败提示和管理命令的即时性。

const (
	humanPacingMinMillis     = 900
	humanPacingDefaultMaxSec = 5
	humanPacingReadPerRune   = 28  // 读对方消息的速度
	humanPacingTypePerRune   = 75  // 打自己第一段的速度
	humanPacingThinkMillis   = 350 // 固定的"想一下"
)

// humanPacingEnabled 默认开启;显式 false 才关闭。
func humanPacingEnabled(policy runtimeMessagePolicy) bool {
	return policy.HumanPacingEnabled == nil || *policy.HumanPacingEnabled
}

// moodContinuityEnabled 读取 message_policy 的情绪连续性开关,默认开启。
func (a *AgentRuntime) moodContinuityEnabled(ctx context.Context) bool {
	var policy runtimeMessagePolicy
	if err := a.integrationConfig(ctx, "message_policy", &policy); err != nil {
		return true
	}
	return policy.MoodContinuityEnabled == nil || *policy.MoodContinuityEnabled
}

// humanTypingDelayMillis 返回这条纯文本回复从"事件发生"到"群里可见"
// 的理想总耗时(毫秒)。调用方负责扣除已经真实消耗的模型时间。
func humanTypingDelayMillis(message, firstSegment string, policy runtimeMessagePolicy) int {
	if !humanPacingEnabled(policy) {
		return 0
	}
	firstSegment = strings.TrimSpace(firstSegment)
	if firstSegment == "" {
		return 0
	}
	read := humanPacingReadPerRune * runeCount(strings.TrimSpace(message))
	if read > 1400 {
		read = 1400
	}
	typing := humanPacingTypePerRune * runeCount(firstSegment)
	desired := read + typing + humanPacingThinkMillis
	maxMillis := policy.HumanPacingMaxSeconds * 1000
	if maxMillis <= 0 {
		maxMillis = humanPacingDefaultMaxSec * 1000
	}
	if desired > maxMillis {
		desired = maxMillis
	}
	if desired < humanPacingMinMillis {
		desired = humanPacingMinMillis
	}
	// ±18% 抖动:同样长度的回复不该每次都等同样的毫秒数。
	if jitter, err := rand.Int(rand.Reader, big.NewInt(37)); err == nil {
		desired = desired * (100 - 18 + int(jitter.Int64())) / 100
	}
	return desired
}

// --- 时段感知 ---

// timeOfDayLabel 把时钟翻译成中文时段,用于动态状态注入。
func timeOfDayLabel(now time.Time) string {
	switch hour := now.Hour(); {
	case hour >= 5 && hour < 8:
		return "清晨"
	case hour >= 8 && hour < 12:
		return "上午"
	case hour >= 12 && hour < 14:
		return "中午"
	case hour >= 14 && hour < 18:
		return "下午"
	case hour >= 18 && hour < 23:
		return "晚上"
	default:
		return "深夜"
	}
}

// --- 机器人自身情绪连续性 ---

// 情绪是短寿命的对话级底色:被夸会亮一点,被怼会呛一点,任务砸了
// 会蔫一会儿。它只影响语气,45 分钟无新线索自动回到平静。
const (
	botMoodNeutral  = ""
	botMoodCheerful = "被夸过,心情不错"
	botMoodTeased   = "刚被怼过,带点不服气"
	botMoodDeflated = "刚办砸过事,有点蔫"
	botMoodTTL      = 45 * time.Minute
)

var botMoodPraiseHints = []string{
	"厉害", "牛", "真棒", "好聪明", "太强", "可爱", "喜欢你", "爱你", "谢谢", "辛苦",
	"靠谱", "真行", "666", "nb", "好用", "真好",
}

var botMoodTeaseHints = []string{
	"笨", "傻", "菜", "垃圾", "废物", "没用", "智障", "闭嘴", "滚", "烦人",
	"骗人", "放鸽子", "水平不行", "退群吧",
}

// detectInboundBotMood 从一条对着机器人说的话里提取情绪线索。
// 只在明确叫到机器人时调用,群友互相说话不改变机器人状态。
func detectInboundBotMood(message string) string {
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" || runeCount(message) > 80 {
		return botMoodNeutral
	}
	for _, hint := range botMoodTeaseHints {
		if strings.Contains(message, hint) {
			return botMoodTeased
		}
	}
	for _, hint := range botMoodPraiseHints {
		if strings.Contains(message, hint) {
			return botMoodCheerful
		}
	}
	return botMoodNeutral
}

// failureDeflatesMood 判断一次失败是否应该让机器人蔫一会儿。
// 合并/被取代/过期废弃是正常调度,不算办砸;真正的生成/媒体失败才算。
func failureDeflatesMood(errorCode string) bool {
	switch errorCode {
	case "", "superseded_by_newer_dialogue", "coalesced_by_newer_dialogue",
		"stale_terminal_discarded", "generation_cancelled":
		return false
	}
	return true
}

// moodStillFresh 判断存储的情绪是否仍在有效期内。
func moodStillFresh(updatedAt string, now time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(updatedAt))
	if err != nil {
		return false
	}
	age := now.Sub(parsed)
	return age >= 0 && age <= botMoodTTL
}

// compileDynamicMoodLine 组装注入 §6 的一行状态;空串表示不注入。
func compileDynamicMoodLine(mood, timeOfDay string) string {
	parts := []string{}
	if timeOfDay != "" {
		parts = append(parts, "现在是"+timeOfDay)
	}
	if strings.TrimSpace(mood) != "" {
		parts = append(parts, "你自己的状态:"+mood)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ";") + "。让语气自然带出这个状态,不要明说这些词,也不要每句都体现。"
}
