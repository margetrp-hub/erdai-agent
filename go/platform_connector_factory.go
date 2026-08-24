package main

import "fmt"

func newPlatformConnector(runtime *AgentRuntime, platform mgmtPlatform) (platformConnector, error) {
	switch platform.Type {
	case "qq_official":
		return newQQOfficialConnector(runtime, platform)
	case "qq_official_webhook":
		return newQQOfficialWebhookConnector(runtime, platform)
	case "aiocqhttp":
		return newOneBotConnector(runtime, platform)
	case "telegram":
		return newTelegramConnector(runtime, platform)
	case "telegram_user":
		return newTelegramUserConnector(runtime, platform)
	case "discord":
		return newDiscordConnector(runtime, platform)
	case "kook":
		return newKookConnector(runtime, platform)
	case "mattermost":
		return newMattermostConnector(runtime, platform)
	case "misskey":
		return newMisskeyConnector(runtime, platform)
	case "satori":
		return newSatoriConnector(runtime, platform)
	case "line":
		return newLineConnector(runtime, platform)
	case "wecom":
		return newWecomConnector(runtime, platform)
	case "weixin_official_account":
		return newWeixinOfficialAccountConnector(runtime, platform)
	case "slack":
		return newSlackConnector(runtime, platform)
	case "webchat":
		return newWebchatConnector(runtime, platform)
	case "lark":
		return newLarkConnector(runtime, platform)
	case "dingtalk":
		return newDingTalkConnector(runtime, platform)
	case "wecom_ai_bot":
		return newWecomAIBotConnector(runtime, platform)
	case "weixin_oc":
		return newWeixinOCConnector(runtime, platform)
	default:
		return nil, fmt.Errorf("%s native connector is not implemented", platform.Type)
	}
}
