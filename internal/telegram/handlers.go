package telegram

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"go_bot/internal/logger"
	sifangfeature "go_bot/internal/telegram/features/sifang"
	"go_bot/internal/telegram/forward"
	"go_bot/internal/telegram/models"
	"go_bot/internal/telegram/service"

	"github.com/go-telegram/bot"
	botModels "github.com/go-telegram/bot/models"
)

// registerHandlers 注册所有命令处理器（异步执行）
func (b *Bot) registerHandlers() {
	// 普通命令 - 异步执行
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact,
		b.asyncHandler(b.handleStart))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/ping", bot.MatchTypeExact,
		b.asyncHandler(b.handlePing))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleHelp)))

	// 管理员命令（仅 Owner） - 异步执行
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/grant", bot.MatchTypePrefix,
		b.asyncHandler(b.RequireOwner(b.handleGrantAdmin)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/revoke", bot.MatchTypePrefix,
		b.asyncHandler(b.RequireOwner(b.handleRevokeAdmin)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/validate", bot.MatchTypeExact,
		b.asyncHandler(b.RequireOwner(b.handleValidateGroupsCommand)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/repair", bot.MatchTypeExact,
		b.asyncHandler(b.RequireOwner(b.handleRepairGroupsCommand)))

	// 管理员命令（Admin+） - 异步执行
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/admins", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleListAdmins)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/userinfo", bot.MatchTypePrefix,
		b.asyncHandler(b.RequireAdmin(b.handleUserInfo)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/leave", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleLeave)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/configs", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleConfigs)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/余额", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleUpstreamBalanceQuery)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/日结", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleUpstreamDailySettlement)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/set_min_balance", bot.MatchTypePrefix,
		b.asyncHandler(b.RequireAdmin(b.handleUpstreamMinBalance)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "/set_balance_alert_limit", bot.MatchTypePrefix,
		b.asyncHandler(b.RequireAdmin(b.handleUpstreamAlertLimit)))

	// 配置菜单回调查询处理器
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, "config:")
	}, b.asyncHandler(b.handleConfigCallback))

	// 四方下发确认回调处理器
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, sifangfeature.SendMoneyCallbackPrefix)
	}, b.asyncHandler(b.handleSifangSendMoneyCallback))

	// 订单联动反馈回调处理
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, orderCascadeCallbackPrefix)
	}, b.asyncHandler(b.handleOrderCascadeCallback))

	// 转发撤回回调处理器（如果转发服务已启用）
	if b.forwardService != nil {
		b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
			return update.CallbackQuery != nil &&
				(strings.HasPrefix(update.CallbackQuery.Data, "recall:") ||
					strings.HasPrefix(update.CallbackQuery.Data, "recall_confirm:") ||
					update.CallbackQuery.Data == "recall_cancel")
		}, b.asyncHandler(b.handleRecallCallback))
	}

	// 收支记账命令
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "查询记账", bot.MatchTypeExact,
		b.asyncHandler(b.handleQueryAccounting))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "删除记账记录", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleDeleteAccounting)))
	b.bot.RegisterHandler(bot.HandlerTypeMessageText, "清零记账", bot.MatchTypeExact,
		b.asyncHandler(b.RequireAdmin(b.handleClearAccounting)))

	// 收支记账删除回调处理器
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, "acc_del:")
	}, b.asyncHandler(b.handleAccountingDeleteCallback))

	// Bot 状态变化事件 (MyChatMember)
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.MyChatMember != nil
	}, b.asyncHandler(b.handleMyChatMember))

	// 消息编辑事件
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.EditedMessage != nil
	}, b.asyncHandler(b.handleEditedMessage))

	// 频道消息
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.ChannelPost != nil
	}, b.asyncHandler(b.handleChannelPost))

	// 编辑的频道消息
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.EditedChannelPost != nil
	}, b.asyncHandler(b.handleEditedChannelPost))

	// 媒体消息处理（照片、视频等）
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		if update.Message == nil {
			return false
		}
		msg := update.Message
		return msg.Photo != nil || msg.Video != nil || msg.Document != nil ||
			msg.Voice != nil || msg.Audio != nil || msg.Sticker != nil || msg.Animation != nil
	}, b.asyncHandler(b.handleMediaMessage))

	// 新成员加入
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.Message != nil && update.Message.NewChatMembers != nil
	}, b.asyncHandler(b.handleNewChatMembers))

	// 成员离开
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		return update.Message != nil && update.Message.LeftChatMember != nil
	}, b.asyncHandler(b.handleLeftChatMember))

	// 普通文本消息（放在最后，作为 fallback）
	b.bot.RegisterHandlerMatchFunc(func(update *botModels.Update) bool {
		if update.Message == nil || update.Message.Text == "" {
			return false
		}
		msg := update.Message
		// 排除命令、系统消息、媒体消息
		return !strings.HasPrefix(msg.Text, "/") &&
			msg.NewChatMembers == nil &&
			msg.LeftChatMember == nil &&
			msg.Photo == nil && msg.Video == nil && msg.Document == nil &&
			msg.Voice == nil && msg.Audio == nil && msg.Sticker == nil && msg.Animation == nil
	}, b.asyncHandler(b.handleTextMessage))

	logger.L().Debug("All handlers registered with async execution")
}

// handleStart 处理 /start 命令
func (b *Bot) handleStart(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}

	// 使用 Service 注册/更新用户
	userInfo := &service.TelegramUserInfo{
		TelegramID:   update.Message.From.ID,
		Username:     update.Message.From.Username,
		FirstName:    update.Message.From.FirstName,
		LastName:     update.Message.From.LastName,
		LanguageCode: update.Message.From.LanguageCode,
		IsPremium:    update.Message.From.IsPremium,
	}

	if err := b.userService.RegisterOrUpdateUser(ctx, userInfo); err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, "注册失败，请稍后重试")
		return
	}

	welcomeText := fmt.Sprintf(
		"👋 你好, %s!\n\n欢迎使用本 Bot。\n\n可用命令:\n/start - 开始\n/ping - 测试连接\n/admins - 查看管理员列表（需要管理员权限）",
		update.Message.From.FirstName,
	)

	b.sendMessage(ctx, update.Message.Chat.ID, welcomeText)
}

// handlePing 处理 /ping 命令
func (b *Bot) handlePing(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	// 更新用户活跃时间
	if update.Message.From != nil {
		_ = b.userService.UpdateUserActivity(ctx, update.Message.From.ID)
	}

	message := b.buildPingMessage(ctx)
	b.sendMessage(ctx, update.Message.Chat.ID, message)
}

// handleHelp 处理 /help 命令（仅 Admin+）
func (b *Bot) handleHelp(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	var text strings.Builder
	text.WriteString("<b>🆘 管理员帮助总览</b>\n\n")

	text.WriteString("<b>通用命令（所有成员）</b>\n")
	text.WriteString("/start - 与机器人建立会话并登记用户信息\n")
	text.WriteString("/ping - 测试机器人连接状态\n\n")

	text.WriteString("<b>管理员命令（Admin+）</b>\n")
	text.WriteString("/help - 查看本帮助\n")
	text.WriteString("/admins - 查看管理员列表\n")
	text.WriteString("/userinfo &lt;user_id&gt; - 查询指定用户信息\n")
	text.WriteString("/leave - 让机器人离开当前群组（仅限群组内执行）\n")
	text.WriteString("/configs - 打开群组功能配置菜单（仅限群组内执行）\n")
	text.WriteString("撤回 - 在群组中引用机器人的消息发送“撤回”以删除该消息\n\n")

	text.WriteString("<b>Owner 专属命令</b>\n")
	text.WriteString("/grant &lt;user_id&gt; - 授予管理员权限\n")
	text.WriteString("/revoke &lt;user_id&gt; - 撤销管理员权限\n\n")
	text.WriteString("/validate - 校验数据库中的群组配置状态\n")
	text.WriteString("/repair - 自动修复可识别的群组配置问题（例如缺少 tier）\n\n")

	text.WriteString("<b>商户号管理（Admin+，群组）</b>\n")
	text.WriteString("绑定 <code>[商户号]</code> - 绑定当前群组的四方商户号\n")
	text.WriteString("解绑 - 解除已绑定的商户号\n")
	text.WriteString("商户号 / 绑定状态 - 查看当前绑定情况\n\n")

	text.WriteString("<b>接口管理（Admin+，群组）</b>\n")
	text.WriteString("绑定接口 <code>[接口名称] [接口ID] [费率]</code> - 绑定上游接口并保存名称/费率，可重复执行绑定多个接口\n")
	text.WriteString("解绑接口 <code>[接口ID]</code> - 解除指定接口；仅发送“解绑接口”可清空全部\n")
	text.WriteString("接口ID / 接口状态 - 查看当前已绑定的接口列表\n\n")

	text.WriteString("<b>上游账单查询（Admin+，上游群）</b>\n")
	text.WriteString("上游账单 <code>[接口ID或名称] [可选日期]</code> - 查询指定接口的跑量、商户实收、代理收益和订单数，日期默认为当天\n\n")

	text.WriteString("<b>四方支付查询（需开启“🏦 四方支付查询”功能并完成商户号绑定）</b>\n")
	text.WriteString("余额[可选日期] - 查询余额，例如：余额、余额10月26\n")
	text.WriteString("账单[可选日期] - 查询日汇总，例如：账单2023/10/26\n")
	text.WriteString("每日00:00:05（北京时间）自动向已绑定商户号的群推送昨日账单\n")
	text.WriteString("通道账单[可选日期] - 查看通道维度汇总\n")
	text.WriteString("提款明细[可选日期] - 查看提款记录\n")
	text.WriteString("费率 - 查看通道费率\n")
	text.WriteString("自动查单 - 默认开启，自动识别文字/图片/视频标题中的订单号并异步查询，可在 /configs 的“🔍 四方自动查单”中关闭\n")
	text.WriteString("下发 <code>金额</code> [谷歌验证码] - 申请下发，支持表达式和谷歌验证码，需在 60 秒内按钮确认\n\n")

	text.WriteString("<b>USDT 价格查询（需开启“💰 USDT价格查询”功能，群组）</b>\n")
	text.WriteString("<code>[a|z|k|w][序号] [金额]</code> - a=全部、z=支付宝、k=银行卡、w=微信；示例：z3 100\n\n")

	text.WriteString("<b>计算器（需开启“🧮 计算器功能”，群组）</b>\n")
	text.WriteString("直接发送数学表达式，例如：<code>(100+20)*1.5</code>\n\n")

	text.WriteString("<b>收支记账（需开启“💳 收支记账”功能，仅 Admin+，群组）</b>\n")
	text.WriteString("查询记账 - 查看今日账单\n")
	text.WriteString("删除记账记录 - 打开最近记录删除菜单\n")
	text.WriteString("清零记账 - 清空所有记录\n")
	text.WriteString("记账输入格式示例：<code>+100U</code>、<code>-50Y</code>、<code>入100*7.2</code>、<code>出50/2Y</code>\n")

	b.sendMessage(ctx, update.Message.Chat.ID, text.String())
}

// handleGrantAdmin 处理 /grant 命令（授予管理员权限）
func (b *Bot) handleGrantAdmin(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}

	// 解析命令参数
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		b.sendErrorMessage(ctx, update.Message.Chat.ID,
			"用法: /grant <user_id>\n例如: /grant 123456789")
		return
	}

	var targetID int64
	_, err := fmt.Sscanf(parts[1], "%d", &targetID)
	if err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, "无效的用户 ID")
		return
	}

	// 使用 Service 授予管理员权限（包含业务验证）
	if err := b.userService.GrantAdminPermission(ctx, targetID, update.Message.From.ID); err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, err.Error())
		return
	}

	b.sendSuccessMessage(ctx, update.Message.Chat.ID,
		fmt.Sprintf("已授予用户 %d 管理员权限", targetID))
}

// handleRevokeAdmin 处理 /revoke 命令（撤销管理员权限）
func (b *Bot) handleRevokeAdmin(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}

	// 解析命令参数
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		b.sendErrorMessage(ctx, update.Message.Chat.ID,
			"用法: /revoke <user_id>\n例如: /revoke 123456789")
		return
	}

	var targetID int64
	_, err := fmt.Sscanf(parts[1], "%d", &targetID)
	if err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, "无效的用户 ID")
		return
	}

	// 使用 Service 撤销管理员权限（包含业务验证）
	if err := b.userService.RevokeAdminPermission(ctx, targetID, update.Message.From.ID); err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, err.Error())
		return
	}

	b.sendSuccessMessage(ctx, update.Message.Chat.ID,
		fmt.Sprintf("已撤销用户 %d 的管理员权限", targetID))
}

// handleValidateGroupsCommand 处理 Owner 的「校验」命令
func (b *Bot) handleValidateGroupsCommand(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	result, err := b.groupService.ValidateGroups(ctx)
	if err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, fmt.Sprintf("校验失败：%v", err))
		return
	}

	var text strings.Builder
	text.WriteString("📋 群组数据校验完成\n")
	text.WriteString(fmt.Sprintf("总群组数：%d\n", result.TotalGroups))
	text.WriteString(fmt.Sprintf("发现问题：%d\n", len(result.Issues)))

	if len(result.Issues) == 0 {
		text.WriteString("\n✅ 所有群组均已通过校验")
		b.sendMessage(ctx, update.Message.Chat.ID, text.String())
		return
	}

	text.WriteString("\n⚠️ 以下群组需要处理：\n")
	maxDetails := 10
	if len(result.Issues) < maxDetails {
		maxDetails = len(result.Issues)
	}

	for i := 0; i < maxDetails; i++ {
		issue := result.Issues[i]
		text.WriteString(fmt.Sprintf("%d. %s (%d)\n", i+1, html.EscapeString(issue.Title), issue.GroupID))

		tier := "(未设置)"
		if issue.StoredTier != "" {
			tier = string(issue.StoredTier)
		}

		text.WriteString(fmt.Sprintf("   tier=%s, status=%s\n",
			html.EscapeString(tier), html.EscapeString(issue.BotStatus)))

		for _, problem := range issue.Problems {
			text.WriteString(fmt.Sprintf("   - %s\n", html.EscapeString(problem)))
		}
	}

	if len(result.Issues) > maxDetails {
		text.WriteString(fmt.Sprintf("... 还有 %d 个群组存在问题，建议登录数据库继续排查\n",
			len(result.Issues)-maxDetails))
	}

	b.sendMessage(ctx, update.Message.Chat.ID, text.String())
}

// handleRepairGroupsCommand 处理 Owner 的「修复」命令
func (b *Bot) handleRepairGroupsCommand(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	result, err := b.groupService.RepairGroups(ctx)
	if err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, fmt.Sprintf("修复失败：%v", err))
		return
	}

	var text strings.Builder
	text.WriteString("🔧 群组数据修复完成\n")
	text.WriteString(fmt.Sprintf("扫描群组：%d\n", result.TotalGroups))
	text.WriteString(fmt.Sprintf("成功写入：%d\n", result.UpdatedGroups))
	text.WriteString(fmt.Sprintf("跳过：%d\n\n", result.SkippedGroups))
	text.WriteString(fmt.Sprintf("✅ 修复 tier：%d\n", result.TierFixed))
	text.WriteString(fmt.Sprintf("✅ 关闭冲突的四方查单：%d\n", result.AutoLookupDisabled))
	text.WriteString("\n如需查看详细列表，请先执行“校验”命令。")

	b.sendMessage(ctx, update.Message.Chat.ID, text.String())
}

// handleListAdmins 处理 /admins 命令（列出所有管理员）
func (b *Bot) handleListAdmins(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	// 使用 Service 获取管理员列表
	admins, err := b.userService.ListAllAdmins(ctx)
	if err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, "查询失败")
		return
	}

	if len(admins) == 0 {
		b.sendMessage(ctx, update.Message.Chat.ID, "📝 暂无管理员")
		return
	}

	var text strings.Builder
	text.WriteString("👥 管理员列表:\n\n")
	for i, admin := range admins {
		roleEmoji := "👤"
		if admin.Role == models.RoleOwner {
			roleEmoji = "👑"
		}
		text.WriteString(fmt.Sprintf("%d. %s %s (@%s) - ID: %d\n",
			i+1,
			roleEmoji,
			admin.FirstName,
			admin.Username,
			admin.TelegramID,
		))
	}

	b.sendMessage(ctx, update.Message.Chat.ID, text.String())
}

// handleUserInfo 处理 /userinfo 命令（查看用户信息）
func (b *Bot) handleUserInfo(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	// 解析命令参数
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		b.sendErrorMessage(ctx, update.Message.Chat.ID,
			"用法: /userinfo <user_id>\n例如: /userinfo 123456789")
		return
	}

	var targetID int64
	_, err := fmt.Sscanf(parts[1], "%d", &targetID)
	if err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, "无效的用户 ID")
		return
	}

	// 使用 Service 查询用户信息
	user, err := b.userService.GetUserInfo(ctx, targetID)
	if err != nil {
		b.sendErrorMessage(ctx, update.Message.Chat.ID, "用户不存在或查询失败")
		return
	}

	var roleEmoji string
	switch user.Role {
	case models.RoleOwner:
		roleEmoji = "👑"
	case models.RoleAdmin:
		roleEmoji = "⭐"
	default:
		roleEmoji = "👤"
	}

	premiumBadge := ""
	if user.IsPremium {
		premiumBadge = " 💎"
	}

	text := fmt.Sprintf(
		"👤 用户信息\n\n"+
			"ID: %d\n"+
			"姓名: %s %s%s\n"+
			"用户名: @%s\n"+
			"角色: %s %s\n"+
			"语言: %s\n"+
			"创建时间: %s\n"+
			"最后活跃: %s",
		user.TelegramID,
		user.FirstName,
		user.LastName,
		premiumBadge,
		user.Username,
		roleEmoji,
		user.Role,
		user.LanguageCode,
		user.CreatedAt.Format("2006-01-02 15:04:05"),
		user.LastActiveAt.Format("2006-01-02 15:04:05"),
	)

	b.sendMessage(ctx, update.Message.Chat.ID, text)
}

// handleLeave 处理 /leave 命令（让 Bot 离开群组）
func (b *Bot) handleLeave(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID

	// 只能在群组中使用
	if update.Message.Chat.Type != "group" && update.Message.Chat.Type != "supergroup" {
		b.sendErrorMessage(ctx, chatID, "此命令只能在群组中使用")
		return
	}

	// 发送离别消息
	b.sendMessage(ctx, chatID, "👋 再见！我将离开这个群组。")

	// 标记 Bot 离开并删除群组记录
	if err := b.groupService.LeaveGroup(ctx, chatID); err != nil {
		logger.L().Errorf("Failed to mark group as left: chat_id=%d, error=%v", chatID, err)
	}

	// 让 Bot 离开群组
	_, err := botInstance.LeaveChat(ctx, &bot.LeaveChatParams{
		ChatID: chatID,
	})
	if err != nil {
		logger.L().Errorf("Failed to leave chat: chat_id=%d, error=%v", chatID, err)
	}
}

// handleMyChatMember 处理 Bot 状态变化（被添加到群组/被踢出群组）
func (b *Bot) handleMyChatMember(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.MyChatMember == nil {
		return
	}

	chatMember := update.MyChatMember
	chat := chatMember.Chat
	oldStatus := chatMember.OldChatMember.Type
	newStatus := chatMember.NewChatMember.Type

	logger.L().Infof("Bot status change: chat_id=%d, old=%s, new=%s", chat.ID, oldStatus, newStatus)

	// Bot 被添加到群组
	if (oldStatus == botModels.ChatMemberTypeLeft || oldStatus == botModels.ChatMemberTypeBanned) &&
		(newStatus == botModels.ChatMemberTypeMember || newStatus == botModels.ChatMemberTypeAdministrator) {
		group := &models.Group{
			TelegramID: chat.ID,
			Type:       string(chat.Type),
			Title:      chat.Title,
			Username:   chat.Username,
			BotStatus:  models.BotStatusActive,
		}

		if err := b.groupService.HandleBotAddedToGroup(ctx, group); err != nil {
			logger.L().Errorf("Failed to handle bot added to group: %v", err)
			return
		}

		// 发送欢迎消息（频道除外）
		if chat.Type != "channel" {
			welcomeText := fmt.Sprintf(
				"👋 你好！我是 Bot，感谢邀请我加入 %s！\n\n"+
					"使用 /configs 查看可用配置命令。",
				chat.Title,
			)
			b.sendMessage(ctx, chat.ID, welcomeText)
		}
	}

	// Bot 被踢出或离开群组
	if (oldStatus == botModels.ChatMemberTypeMember || oldStatus == botModels.ChatMemberTypeAdministrator) &&
		(newStatus == botModels.ChatMemberTypeLeft || newStatus == botModels.ChatMemberTypeBanned) {
		reason := "left"
		if newStatus == botModels.ChatMemberTypeBanned {
			reason = "kicked"
		}

		if err := b.groupService.HandleBotRemovedFromGroup(ctx, chat.ID, reason); err != nil {
			logger.L().Errorf("Failed to handle bot removed from group: %v", err)
		}
	}
}

// handleTextMessage 处理普通文本消息
func (b *Bot) handleTextMessage(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil || update.Message.Text == "" {
		return
	}

	msg := update.Message

	if msg.From == nil {
		return
	}

	if msg.From.IsBot {
		return
	}

	b.registerUserFromTelegram(ctx, msg.From)

	// 排除命令消息（以 / 开头）
	if strings.HasPrefix(msg.Text, "/") {
		return
	}

	// 排除系统消息（NewChatMembers、LeftChatMember 等）
	if msg.NewChatMembers != nil || msg.LeftChatMember != nil {
		return
	}

	// 处理管理员撤回命令
	if b.tryHandleRecallCommand(ctx, botInstance, msg) {
		return
	}

	// 优先检查用户输入状态（用于配置菜单输入）
	if msg.From != nil && b.configMenuService != nil {
		// 先检查是否有待处理状态
		state := b.configMenuService.GetUserState(msg.Chat.ID, msg.From.ID)
		if state != nil {
			// 有状态，获取或创建群组记录
			chatInfo := &service.TelegramChatInfo{
				ChatID:   msg.Chat.ID,
				Type:     string(msg.Chat.Type),
				Title:    msg.Chat.Title,
				Username: msg.Chat.Username,
			}
			group, err := b.groupService.GetOrCreateGroup(ctx, chatInfo)
			if err != nil {
				b.sendErrorMessage(ctx, msg.Chat.ID, "获取群组信息失败")
				return
			}

			items := b.getConfigItems()
			responseMsg, err := b.configMenuService.ProcessUserInput(ctx, group, msg.From.ID, msg.Text, items)

			// 如果有响应消息（无论成功或失败），说明这是配置输入
			if responseMsg != "" {
				if err != nil {
					b.sendErrorMessage(ctx, msg.Chat.ID, responseMsg)
				} else {
					b.sendSuccessMessage(ctx, msg.Chat.ID, responseMsg)
				}
				return // 处理完配置输入，不再记录为普通消息
			}
		}
	}

	// 尝试处理记账输入
	if b.handleAccountingInput(ctx, botInstance, update) {
		return // 记账已处理，不再记录为普通消息
	}

	// 使用 Feature Manager 处理功能插件
	// 这里替代了原来硬编码的计算器功能检测
	response, handled, err := b.featureManager.Process(ctx, msg)
	if handled {
		sendFeatureResponse := func() {
			if response == nil || response.Text == "" {
				return
			}

			var sendFunc func(context.Context, int64, string, botModels.ReplyMarkup, ...int) (*botModels.Message, error)
			if response.Temporary {
				sendFunc = b.sendTemporaryMessageWithMarkup
			} else {
				sendFunc = b.sendMessageWithMarkupAndMessage
			}

			sent, sendErr := sendFunc(ctx, msg.Chat.ID, response.Text, response.ReplyMarkup, msg.ID)
			if sendErr == nil {
				b.tryScheduleSifangSendMoneyExpiration(sent, response.ReplyMarkup)
			}
		}

		if err != nil {
			if response != nil && response.Text != "" {
				sendFeatureResponse()
			} else {
				b.sendErrorMessage(ctx, msg.Chat.ID, "处理失败，请稍后重试", msg.ID)
			}
		} else {
			sendFeatureResponse()
		}
		return // 功能已处理，不再记录为普通消息
	}

	// 构造消息信息
	replyToID := int64(0)
	if msg.ReplyToMessage != nil {
		replyToID = int64(msg.ReplyToMessage.ID)
	}

	textMsg := &service.TextMessageInfo{
		TelegramMessageID: int64(msg.ID),
		ChatID:            msg.Chat.ID,
		UserID:            msg.From.ID,
		Text:              msg.Text,
		ReplyToMessageID:  replyToID,
		SentAt:            time.Unix(int64(msg.Date), 0),
	}

	// 记录消息
	if err := b.messageService.HandleTextMessage(ctx, textMsg); err != nil {
		logger.L().Errorf("Failed to handle text message: %v", err)
	}

	b.tryTriggerSifangAutoLookup(ctx, msg)
}

// tryHandleRecallCommand 处理管理员引用撤回命令
func (b *Bot) tryHandleRecallCommand(ctx context.Context, botInstance *bot.Bot, msg *botModels.Message) bool {
	if strings.TrimSpace(msg.Text) != "撤回" {
		return false
	}

	isAdmin, err := b.userService.CheckAdminPermission(ctx, msg.From.ID)
	if err != nil {
		logger.L().Errorf("Failed to check admin permission: user=%d err=%v", msg.From.ID, err)
		b.sendErrorMessage(ctx, msg.Chat.ID, "权限检查失败，请稍后重试", msg.ID)
		return true
	}

	if !isAdmin {
		b.sendErrorMessage(ctx, msg.Chat.ID, "此命令需要管理员权限", msg.ID)
		return true
	}

	if msg.ReplyToMessage == nil {
		b.sendErrorMessage(ctx, msg.Chat.ID, "请引用需要撤回的机器人消息", msg.ID)
		return true
	}

	target := msg.ReplyToMessage
	if target.From == nil || target.From.ID != botInstance.ID() {
		b.sendErrorMessage(ctx, msg.Chat.ID, "只能撤回本机器人的消息", msg.ID)
		return true
	}

	_, err = botInstance.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    msg.Chat.ID,
		MessageID: target.ID,
	})
	if err != nil {
		logger.L().Errorf("Failed to delete recalled message: chat=%d target_msg=%d err=%v",
			msg.Chat.ID, target.ID, err)
		b.sendErrorMessage(ctx, msg.Chat.ID, "撤回失败，请稍后重试", msg.ID)
		return true
	}

	_, err = botInstance.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    msg.Chat.ID,
		MessageID: msg.ID,
	})
	if err != nil {
		logger.L().Warnf("Failed to delete recall command message: chat=%d msg=%d err=%v",
			msg.Chat.ID, msg.ID, err)
	}

	return true
}

func (b *Bot) handleSifangSendMoneyCallback(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}

	if b.sifangFeature == nil {
		b.answerCallback(ctx, botInstance, query.ID, "功能未启用", true)
		return
	}

	data := strings.TrimPrefix(query.Data, sifangfeature.SendMoneyCallbackPrefix)
	parts := strings.SplitN(data, ":", 2)
	if len(parts) != 2 {
		b.answerCallback(ctx, botInstance, query.ID, "无效的操作", true)
		return
	}

	action := parts[0]
	token := parts[1]

	result, err := b.sifangFeature.HandleSendMoneyCallback(ctx, query, action, token)
	if err != nil {
		logger.L().Errorf("handle sifang send money callback failed: action=%s token=%s err=%v", action, token, err)
		b.answerCallback(ctx, botInstance, query.ID, "处理失败，请稍后重试", true)
		return
	}

	if result != nil && result.ShouldEdit {
		if msg := query.Message.Message; msg != nil {
			b.editMessage(ctx, msg.Chat.ID, msg.ID, result.Text, result.Markup)
		}
	}

	if result != nil {
		b.answerCallback(ctx, botInstance, query.ID, result.Answer, result.ShowAlert)
	} else {
		b.answerCallback(ctx, botInstance, query.ID, "", false)
	}
}

func (b *Bot) handleOrderCascadeCallback(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	query := update.CallbackQuery
	if query == nil || query.Data == "" {
		return
	}

	trimmed := strings.TrimPrefix(query.Data, orderCascadeCallbackPrefix)
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) != 2 {
		b.answerCallback(ctx, botInstance, query.ID, "无效的操作", true)
		return
	}

	action := parts[0]
	token := parts[1]

	state, ok := b.getOrderCascadeState(token)
	if !ok || state == nil {
		b.answerCallback(ctx, botInstance, query.ID, "操作已过期", true)
		return
	}

	now := time.Now()
	feedback := buildOrderCascadeFeedbackMessage(state, action, &query.From, now)
	if strings.TrimSpace(feedback) == "" {
		b.answerCallback(ctx, botInstance, query.ID, "暂无法处理", true)
		return
	}

	var replyTo []int
	if state.MerchantMessageID > 0 {
		replyTo = append(replyTo, state.MerchantMessageID)
	}

	if _, err := b.sendMessageWithMarkupAndMessage(ctx, state.MerchantChatID, feedback, nil, replyTo...); err != nil {
		logger.L().Errorf("Failed to relay cascade feedback: merchant_chat=%d order_no=%s err=%v",
			state.MerchantChatID, state.OrderNo, err)
		b.answerCallback(ctx, botInstance, query.ID, "反馈发送失败", true)
		return
	}

	var cascadeMsg *botModels.Message
	if query.Message.Message != nil {
		cascadeMsg = query.Message.Message
	}
	b.editCascadeMessage(ctx, state, cascadeMsg, action, &query.From, now)
	b.answerCallback(ctx, botInstance, query.ID, "反馈已同步", false)
}

func (b *Bot) tryScheduleSifangSendMoneyExpiration(sentMsg *botModels.Message, markup botModels.ReplyMarkup) {
	if b.sifangFeature == nil || sentMsg == nil || markup == nil {
		return
	}

	inline, ok := markup.(*botModels.InlineKeyboardMarkup)
	if !ok {
		return
	}

	var token string
	for _, row := range inline.InlineKeyboard {
		for _, button := range row {
			if !strings.HasPrefix(button.CallbackData, sifangfeature.SendMoneyCallbackPrefix) {
				continue
			}
			rest := strings.TrimPrefix(button.CallbackData, sifangfeature.SendMoneyCallbackPrefix)
			parts := strings.SplitN(rest, ":", 2)
			if len(parts) == 2 {
				token = parts[1]
				break
			}
		}
		if token != "" {
			break
		}
	}

	if token == "" {
		return
	}

	b.scheduleSifangSendMoneyExpiration(sentMsg.Chat.ID, sentMsg.ID, token)
}

func (b *Bot) scheduleSifangSendMoneyExpiration(chatID int64, messageID int, token string) {
	go func() {
		timer := time.NewTimer(sifangfeature.SendMoneyConfirmTTL)
		defer timer.Stop()

		<-timer.C

		if b.sifangFeature == nil {
			return
		}

		if !b.sifangFeature.ExpirePending(token) {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		b.editMessage(ctx, chatID, messageID, "⚠️ 由于 60 秒内没有操作，下发请求已失效，请重新下发。", nil)
	}()
}

// handleMediaMessage 处理媒体消息
func (b *Bot) handleMediaMessage(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	msg := update.Message

	if msg.From == nil {
		return
	}

	if msg.From.IsBot {
		return
	}

	b.registerUserFromTelegram(ctx, msg.From)
	var messageType, fileID, mimeType string
	var fileSize int64
	var fileNames []string

	// 判断媒体类型并提取信息
	if len(msg.Photo) > 0 {
		messageType = models.MessageTypePhoto
		photo := msg.Photo[len(msg.Photo)-1] // 取最大尺寸
		fileID = photo.FileID
		fileSize = int64(photo.FileSize)
	} else if msg.Video != nil {
		messageType = models.MessageTypeVideo
		fileID = msg.Video.FileID
		fileSize = int64(msg.Video.FileSize)
		mimeType = msg.Video.MimeType
		if msg.Video.FileName != "" {
			fileNames = append(fileNames, msg.Video.FileName)
		}
	} else if msg.Document != nil {
		messageType = models.MessageTypeDocument
		fileID = msg.Document.FileID
		fileSize = int64(msg.Document.FileSize)
		mimeType = msg.Document.MimeType
		if msg.Document.FileName != "" {
			fileNames = append(fileNames, msg.Document.FileName)
		}
	} else if msg.Voice != nil {
		messageType = models.MessageTypeVoice
		fileID = msg.Voice.FileID
		fileSize = int64(msg.Voice.FileSize)
		mimeType = msg.Voice.MimeType
	} else if msg.Audio != nil {
		messageType = models.MessageTypeAudio
		fileID = msg.Audio.FileID
		fileSize = int64(msg.Audio.FileSize)
		mimeType = msg.Audio.MimeType
		if msg.Audio.FileName != "" {
			fileNames = append(fileNames, msg.Audio.FileName)
		}
	} else if msg.Sticker != nil {
		messageType = models.MessageTypeSticker
		fileID = msg.Sticker.FileID
		fileSize = int64(msg.Sticker.FileSize)
	} else if msg.Animation != nil {
		messageType = models.MessageTypeAnimation
		fileID = msg.Animation.FileID
		fileSize = int64(msg.Animation.FileSize)
		mimeType = msg.Animation.MimeType
		if msg.Animation.FileName != "" {
			fileNames = append(fileNames, msg.Animation.FileName)
		}
	} else {
		return // 不是支持的媒体类型
	}

	// 构造媒体消息信息
	mediaMsg := &service.MediaMessageInfo{
		TelegramMessageID: int64(msg.ID),
		ChatID:            msg.Chat.ID,
		UserID:            msg.From.ID,
		MessageType:       messageType,
		Caption:           msg.Caption,
		MediaFileID:       fileID,
		MediaFileSize:     fileSize,
		MediaMimeType:     mimeType,
		SentAt:            time.Unix(int64(msg.Date), 0),
	}

	// 记录消息
	if err := b.messageService.HandleMediaMessage(ctx, mediaMsg); err != nil {
		logger.L().Errorf("Failed to handle media message: %v", err)
	}

	b.tryTriggerSifangAutoLookup(ctx, msg, fileNames...)
}

// handleEditedMessage 处理消息编辑事件
func (b *Bot) handleEditedMessage(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.EditedMessage == nil || update.EditedMessage.Text == "" {
		return
	}

	msg := update.EditedMessage
	editedAt := time.Unix(int64(msg.EditDate), 0)

	// 更新消息编辑信息
	if err := b.messageService.HandleEditedMessage(ctx, int64(msg.ID), msg.Chat.ID, msg.Text, editedAt); err != nil {
		logger.L().Errorf("Failed to handle edited message: %v", err)
	}
}

// handleChannelPost 处理频道消息
func (b *Bot) handleChannelPost(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.ChannelPost == nil {
		return
	}

	post := update.ChannelPost
	messageType := models.MessageTypeChannelPost
	text := post.Text
	fileID := ""

	// 如果是媒体消息，提取 file_id
	if len(post.Photo) > 0 {
		fileID = post.Photo[len(post.Photo)-1].FileID
	} else if post.Video != nil {
		fileID = post.Video.FileID
	} else if post.Document != nil {
		fileID = post.Document.FileID
	}

	channelPost := &service.ChannelPostInfo{
		TelegramMessageID: int64(post.ID),
		ChatID:            post.Chat.ID,
		MessageType:       messageType,
		Text:              text,
		MediaFileID:       fileID,
		SentAt:            time.Unix(int64(post.Date), 0),
	}

	// 记录频道消息
	if err := b.messageService.RecordChannelPost(ctx, channelPost); err != nil {
		logger.L().Errorf("Failed to handle channel post: %v", err)
	}

	// 触发转发功能
	if b.forwardService != nil {
		if err := b.forwardService.HandleChannelMessage(ctx, botInstance, update); err != nil {
			logger.L().Errorf("Failed to handle channel message for forwarding: %v", err)
		}
	}
}

// handleEditedChannelPost 处理编辑的频道消息
func (b *Bot) handleEditedChannelPost(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.EditedChannelPost == nil || update.EditedChannelPost.Text == "" {
		return
	}

	post := update.EditedChannelPost
	editedAt := time.Unix(int64(post.EditDate), 0)

	// 更新频道消息编辑信息
	if err := b.messageService.HandleEditedMessage(ctx, int64(post.ID), post.Chat.ID, post.Text, editedAt); err != nil {
		logger.L().Errorf("Failed to handle edited channel post: %v", err)
	}
}

func (b *Bot) handleUpstreamBalanceQuery(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID
	group, err := b.groupService.GetGroupInfo(ctx, chatID)
	if err != nil {
		logger.L().Errorf("Failed to load group info for balance query: chat_id=%d err=%v", chatID, err)
		b.sendErrorMessage(ctx, chatID, "获取群组信息失败")
		return
	}
	if models.NormalizeGroupTier(group.Tier) != models.GroupTierUpstream {
		b.sendErrorMessage(ctx, chatID, "仅上游群支持该命令")
		return
	}

	balance, err := b.upstreamBalanceService.Get(ctx, chatID)
	if err != nil {
		logger.L().Errorf("Failed to query upstream balance: chat_id=%d err=%v", chatID, err)
		b.sendErrorMessage(ctx, chatID, "查询余额失败")
		return
	}

	current := 0.0
	minBalance := 0.0
	if balance != nil {
		current = balance.Balance
		minBalance = balance.MinBalance
	}

	message := fmt.Sprintf("当前余额：%.2f", current)
	if minBalance > 0 {
		message = fmt.Sprintf("%s\n最低余额：%.2f", message, minBalance)
		if current < minBalance {
			message = fmt.Sprintf("%s\n⚠️ 已低于最低余额阈值，请尽快补足。", message)
		}
	}

	b.sendMessage(ctx, chatID, message)
}

func (b *Bot) handleUpstreamDailySettlement(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID
	group, err := b.groupService.GetGroupInfo(ctx, chatID)
	if err != nil {
		logger.L().Errorf("Failed to load group info for settlement: chat_id=%d err=%v", chatID, err)
		b.sendErrorMessage(ctx, chatID, "获取群组信息失败")
		return
	}
	if models.NormalizeGroupTier(group.Tier) != models.GroupTierUpstream {
		b.sendErrorMessage(ctx, chatID, "仅上游群支持日结")
		return
	}

	loc := mustLoadChinaLocation()
	targetDate := previousBillingDate(time.Now().In(loc), loc)

	result, err := b.upstreamBalanceService.SettleDaily(ctx, group, targetDate)
	if err != nil {
		logger.L().Errorf("Upstream manual settlement failed: chat_id=%d err=%v", chatID, err)
		b.sendErrorMessage(ctx, chatID, fmt.Sprintf("日结失败：%v", err))
		return
	}

	message := formatUpstreamSettlementMessage(result)
	b.sendMessageWithMarkupAndMessage(ctx, chatID, message, nil)
}

func (b *Bot) handleUpstreamMinBalance(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID
	parts := strings.Fields(strings.TrimSpace(update.Message.Text))
	if len(parts) < 2 {
		b.sendErrorMessage(ctx, chatID, "请提供最低余额金额，例如 /set_min_balance 100")
		return
	}
	value, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || value < 0 {
		b.sendErrorMessage(ctx, chatID, "最低余额需为非负数")
		return
	}

	balance, err := b.upstreamBalanceService.SetMinBalance(ctx, chatID, update.Message.From.ID, value)
	if err != nil {
		logger.L().Errorf("Failed to set min balance: chat_id=%d err=%v", chatID, err)
		b.sendErrorMessage(ctx, chatID, "配置最低余额失败")
		return
	}

	message := fmt.Sprintf("✅ 最低余额已设置为 %.2f\n当前余额：%.2f", balance.MinBalance, balance.Balance)
	if balance.Balance < balance.MinBalance {
		message = fmt.Sprintf("%s\n⚠️ 当前余额已低于阈值，请及时补足。", message)
	}

	b.sendSuccessMessage(ctx, chatID, message)
}

func (b *Bot) handleUpstreamAlertLimit(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID
	parts := strings.Fields(strings.TrimSpace(update.Message.Text))
	if len(parts) < 2 {
		b.sendErrorMessage(ctx, chatID, "请提供每小时告警次数上限，例如 /set_balance_alert_limit 3")
		return
	}

	limit, err := strconv.Atoi(parts[1])
	if err != nil || limit <= 0 {
		b.sendErrorMessage(ctx, chatID, "告警次数需为大于0的整数")
		return
	}

	balance, err := b.upstreamBalanceService.SetAlertLimit(ctx, chatID, update.Message.From.ID, limit)
	if err != nil {
		logger.L().Errorf("Failed to set balance alert limit: chat_id=%d err=%v", chatID, err)
		b.sendErrorMessage(ctx, chatID, "配置告警次数失败")
		return
	}

	message := fmt.Sprintf("✅ 告警频率已更新，每小时最多 %d 条\n当前余额：%.2f\n最低余额：%.2f", balance.AlertLimitPerHour, balance.Balance, balance.MinBalance)
	if balance.MinBalance <= 0 {
		message = fmt.Sprintf("%s\n提示：请先使用 /set_min_balance 配置最低余额。", message)
	}

	b.sendSuccessMessage(ctx, chatID, message)
}

// handleNewChatMembers 处理新成员加入系统消息
func (b *Bot) handleNewChatMembers(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil || update.Message.NewChatMembers == nil {
		return
	}

	for i := range update.Message.NewChatMembers {
		member := update.Message.NewChatMembers[i]
		if member.IsBot {
			continue
		}
		b.registerUserFromTelegram(ctx, &member)
	}
}

// handleLeftChatMember 处理成员离开系统消息
func (b *Bot) handleLeftChatMember(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil || update.Message.LeftChatMember == nil {
		return
	}

	msg := update.Message
	leftMember := msg.LeftChatMember

	// 记录日志
	logger.L().Infof("Member left: chat_id=%d, user_id=%d, username=%s",
		msg.Chat.ID, leftMember.ID, leftMember.Username)

	// 这里可以添加更多逻辑，例如：
	// - 发送离别消息
	// - 更新成员统计
	// - 记录离开事件到数据库
}

// handleRecallCallback 处理转发撤回回调
func (b *Bot) handleRecallCallback(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.CallbackQuery == nil {
		return
	}

	query := update.CallbackQuery
	data := query.Data

	// 获取 forwardService（类型断言为具体类型以访问 Handler 方法）
	forwardSvc, ok := b.forwardService.(*forward.Service)
	if !ok {
		logger.L().Error("Failed to cast forwardService to *forward.Service")
		return
	}

	// 根据 callback data 调用相应的处理方法
	if strings.HasPrefix(data, "recall_confirm:") {
		forwardSvc.HandleRecallConfirmCallback(ctx, botInstance, query)
	} else if data == "recall_cancel" {
		forwardSvc.HandleRecallCancelCallback(ctx, botInstance, query)
	} else if strings.HasPrefix(data, "recall:") {
		forwardSvc.HandleRecallCallback(ctx, botInstance, query)
	}
}

func (b *Bot) registerUserFromTelegram(ctx context.Context, tgUser *botModels.User) {
	if tgUser == nil {
		return
	}

	if tgUser.IsBot {
		return
	}

	userInfo := &service.TelegramUserInfo{
		TelegramID:   tgUser.ID,
		Username:     tgUser.Username,
		FirstName:    tgUser.FirstName,
		LastName:     tgUser.LastName,
		LanguageCode: tgUser.LanguageCode,
		IsPremium:    tgUser.IsPremium,
	}

	if err := b.userService.RegisterOrUpdateUser(ctx, userInfo); err != nil {
		logger.L().Warnf("Failed to auto register user %d: %v", tgUser.ID, err)
	}
}

// ==================== 收支记账相关 Handlers ====================

// handleAccountingInput 处理记账输入（私有函数，由 handleTextMessage 调用）
func (b *Bot) handleAccountingInput(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) bool {
	if update.Message == nil || update.Message.From == nil {
		return false
	}

	chatID := update.Message.Chat.ID
	chat := update.Message.Chat
	userID := update.Message.From.ID
	text := strings.TrimSpace(update.Message.Text)

	// 获取或创建群组记录
	chatInfo := &service.TelegramChatInfo{
		ChatID:   chat.ID,
		Type:     string(chat.Type),
		Title:    chat.Title,
		Username: chat.Username,
	}
	group, err := b.groupService.GetOrCreateGroup(ctx, chatInfo)
	if err != nil || !group.Settings.AccountingEnabled {
		return false
	}

	// 检查用户权限（仅管理员）
	isAdmin, err := b.userService.CheckAdminPermission(ctx, userID)
	if err != nil || !isAdmin {
		return false
	}

	// 尝试添加记账记录
	if err := b.accountingService.AddRecord(ctx, chatID, userID, text); err != nil {
		// 如果是格式错误，返回 false（让后续 handler 处理）
		if strings.Contains(err.Error(), "输入格式错误") {
			return false
		}
		// 其他错误，显示错误消息
		b.sendErrorMessage(ctx, chatID, err.Error())
		return true
	}

	// 添加成功，自动查询并显示最新账单
	report, err := b.accountingService.QueryRecords(ctx, chatID)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, "记录成功，但查询账单失败")
		return true
	}

	b.sendMessage(ctx, chatID, report)
	return true
}

// handleQueryAccounting 处理"查询记账"命令
func (b *Bot) handleQueryAccounting(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID
	chat := update.Message.Chat

	// 获取或创建群组记录
	chatInfo := &service.TelegramChatInfo{
		ChatID:   chat.ID,
		Type:     string(chat.Type),
		Title:    chat.Title,
		Username: chat.Username,
	}
	group, err := b.groupService.GetOrCreateGroup(ctx, chatInfo)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, "查询失败")
		return
	}

	if !group.Settings.AccountingEnabled {
		b.sendErrorMessage(ctx, chatID, "收支记账功能未启用")
		return
	}

	// 查询账单
	report, err := b.accountingService.QueryRecords(ctx, chatID)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, err.Error())
		return
	}

	b.sendMessage(ctx, chatID, report)
}

// handleDeleteAccounting 处理"删除记账记录"命令（显示删除界面）
func (b *Bot) handleDeleteAccounting(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID
	chat := update.Message.Chat

	// 获取或创建群组记录
	chatInfo := &service.TelegramChatInfo{
		ChatID:   chat.ID,
		Type:     string(chat.Type),
		Title:    chat.Title,
		Username: chat.Username,
	}
	group, err := b.groupService.GetOrCreateGroup(ctx, chatInfo)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, "查询失败")
		return
	}

	if !group.Settings.AccountingEnabled {
		b.sendErrorMessage(ctx, chatID, "收支记账功能未启用")
		return
	}

	// 获取最近2天的记录
	records, err := b.accountingService.GetRecentRecordsForDeletion(ctx, chatID)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, err.Error())
		return
	}

	if len(records) == 0 {
		b.sendMessage(ctx, chatID, "没有可删除的记录")
		return
	}

	// 构建删除界面
	var keyboard [][]botModels.InlineKeyboardButton
	for _, record := range records {
		// 格式：MM-DD HH:MM | ±金额 货币 [删除]
		dateStr := record.RecordedAt.Format("01-02 15:04")
		amountStr := formatRecordAmount(record.Amount, record.Currency)
		buttonText := fmt.Sprintf("%s | %s", dateStr, amountStr)

		keyboard = append(keyboard, []botModels.InlineKeyboardButton{
			{
				Text:         buttonText,
				CallbackData: fmt.Sprintf("acc_del:%s", record.ID.Hex()),
			},
		})
	}

	params := &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "🗑️ 点击按钮删除对应记录：",
		ReplyMarkup: &botModels.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	}

	if _, err := botInstance.SendMessage(ctx, params); err != nil {
		logger.L().Errorf("Failed to send delete menu: %v", err)
	}
}

// formatRecordAmount 格式化记录金额（用于删除界面）
func formatRecordAmount(amount float64, currency string) string {
	var currencySymbol string
	if currency == models.CurrencyUSD {
		currencySymbol = "U"
	} else {
		currencySymbol = "Y"
	}

	if amount == float64(int64(amount)) {
		// 整数
		if amount >= 0 {
			return fmt.Sprintf("+%d%s", int64(amount), currencySymbol)
		}
		return fmt.Sprintf("%d%s", int64(amount), currencySymbol)
	}
	// 小数
	if amount >= 0 {
		return fmt.Sprintf("+%.2f%s", amount, currencySymbol)
	}
	return fmt.Sprintf("%.2f%s", amount, currencySymbol)
}

// handleAccountingDeleteCallback 处理删除按钮回调
func (b *Bot) handleAccountingDeleteCallback(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.CallbackQuery == nil {
		return
	}

	query := update.CallbackQuery
	chatID := query.Message.Message.Chat.ID
	data := query.Data

	// 解析 callback data: acc_del:<record_id>
	if !strings.HasPrefix(data, "acc_del:") {
		return
	}

	recordID := strings.TrimPrefix(data, "acc_del:")

	// 删除记录
	if err := b.accountingService.DeleteRecord(ctx, recordID); err != nil {
		// 回答 callback query
		if _, err := botInstance.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "删除失败",
			ShowAlert:       true,
		}); err != nil {
			logger.L().Errorf("Failed to answer callback query: %v", err)
		}
		return
	}

	// 回答 callback query
	if _, err := botInstance.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: query.ID,
		Text:            "删除成功",
	}); err != nil {
		logger.L().Errorf("Failed to answer callback query: %v", err)
	}

	// 删除成功，自动发送最新账单
	report, err := b.accountingService.QueryRecords(ctx, chatID)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, "删除成功，但查询账单失败")
		return
	}

	b.sendMessage(ctx, chatID, report)
}

// handleClearAccounting 处理"清零记账"命令
func (b *Bot) handleClearAccounting(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
	if update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID
	chat := update.Message.Chat

	// 获取或创建群组记录
	chatInfo := &service.TelegramChatInfo{
		ChatID:   chat.ID,
		Type:     string(chat.Type),
		Title:    chat.Title,
		Username: chat.Username,
	}
	group, err := b.groupService.GetOrCreateGroup(ctx, chatInfo)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, "查询失败")
		return
	}

	if !group.Settings.AccountingEnabled {
		b.sendErrorMessage(ctx, chatID, "收支记账功能未启用")
		return
	}

	// 清空所有记录
	count, err := b.accountingService.ClearAllRecords(ctx, chatID)
	if err != nil {
		b.sendErrorMessage(ctx, chatID, err.Error())
		return
	}

	b.sendSuccessMessage(ctx, chatID, fmt.Sprintf("已清空 %d 条记账记录", count))
}
