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
	// Keep single-character cues out of this list.  A bare "牛" is common in
	// ordinary words (for example, "牛奶") and is not praise by itself.
	"厉害", "真棒", "好聪明", "太强", "可爱", "喜欢你", "爱你", "谢谢", "辛苦",
	"靠谱", "真行", "666", "nb", "好用", "真好",
}

var botMoodPraisePhraseHints = []string{"牛啊", "真牛", "太牛", "牛逼", "牛批", "牛！", "牛!"}

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
	// Strip quoted and attributed clauses before looking for cues.  A member
	// quoting another person's insult or compliment must not change the bot's
	// own mood.  socialOwnClauses also keeps punctuation-separated direct
	// address clauses intact for the existing negation helper.
	clauses := socialOwnClauses(message)
	if len(clauses) == 0 {
		return botMoodNeutral
	}
	for _, clause := range clauses {
		clause = strings.ToLower(strings.TrimSpace(clause))
		if clause == "" || moodClauseTargetsSomeoneElse(clause) {
			continue
		}
		for _, hint := range botMoodTeaseHints {
			if moodCuePresent(clause, hint) {
				return botMoodTeased
			}
		}
		for _, hint := range botMoodPraiseHints {
			if moodCuePresent(clause, hint) {
				return botMoodCheerful
			}
		}
		for _, hint := range botMoodPraisePhraseHints {
			if moodCuePresent(clause, hint) {
				return botMoodCheerful
			}
		}
	}
	return botMoodNeutral
}

// moodCuePresent requires the cue to be in an un-negated span.  This keeps
// corrections such as "你一点也不笨" from making the bot sulk and avoids
// treating "不谢谢"/"不用谢谢" as praise.
func moodCuePresent(clause, cue string) bool {
	for offset := 0; offset < len(clause); {
		index := strings.Index(clause[offset:], cue)
		if index < 0 {
			return false
		}
		index += offset
		offset = index + len(cue)
		prefix := clause[:index]
		if !moodCueOwned(clause, index, cue) {
			continue
		}
		// "是不是傻/笨" is a rhetorical tease, not a negated cue.  Keep
		// the normal negation handling for "是不是不笨" and similar forms.
		rhetorical := strings.HasSuffix(prefix, "是不是") || strings.HasSuffix(prefix, "难道是")
		if rhetorical || !socialNegated(prefix) {
			return true
		}
	}
	return false
}

// moodCueOwned rejects lexical matches and cues whose grammatical subject is
// somebody else.  The message is only sampled after an explicit wake/mention,
// so a nearby "你" or the bot name is sufficient evidence of direct address.
func moodCueOwned(clause string, index int, cue string) bool {
	suffix := clause[index+len(cue):]
	if cue == "菜" && strings.HasSuffix(clause[:index], "点") {
		return false // 点菜
	}
	if cue == "滚" && strings.HasPrefix(suffix, "筒") {
		return false // 滚筒洗衣机
	}
	if cue == "笨" && strings.HasPrefix(suffix, "重") {
		return false // 笨重
	}
	prefix := clause[:index]
	// In comparisons such as "你比他厉害", the third-person name is the
	// comparator, while the grammatical subject remains the addressed bot.
	if strings.Contains(prefix, "比") && strings.Contains(prefix, "你") {
		return true
	}
	for _, target := range []string{"他", "她", "他们", "她们", "别人", "有人"} {
		if targetIndex := strings.LastIndex(prefix, target); targetIndex >= 0 {
			between := prefix[targetIndex+len(target):]
			if !strings.ContainsAny(between, "你豆包") {
				return false
			}
		}
	}
	if targetIndex := strings.LastIndex(prefix, "我"); targetIndex >= 0 {
		between := prefix[targetIndex+len("我"):]
		if !strings.ContainsAny(between, "你豆包") {
			return false
		}
	}
	return true
}

// moodClauseTargetsSomeoneElse filters the common third-person forms that
// survive socialOwnClauses when they are not explicitly attributed.  Direct
// "你" address and bot-name mentions remain eligible.
func moodClauseTargetsSomeoneElse(clause string) bool {
	for _, prefix := range []string{"他", "她", "他们", "她们", "别人", "有人", "朋友", "同事"} {
		if strings.HasPrefix(clause, prefix) {
			return true
		}
	}
	return false
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
