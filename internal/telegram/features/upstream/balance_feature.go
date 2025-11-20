package upstream

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go_bot/internal/logger"
	"go_bot/internal/telegram/features/types"
	"go_bot/internal/telegram/models"
	"go_bot/internal/telegram/service"

	botModels "github.com/go-telegram/bot/models"
)

var balanceCommandPattern = regexp.MustCompile(`^[+-]\d+(?:\.\d+)?$`)

// BalanceFeature 处理上游群余额加减
type BalanceFeature struct {
	balanceService service.UpstreamBalanceService
	userService    service.UserService
}

// NewBalanceFeature 创建实例
func NewBalanceFeature(balanceSvc service.UpstreamBalanceService, userSvc service.UserService) *BalanceFeature {
	return &BalanceFeature{
		balanceService: balanceSvc,
		userService:    userSvc,
	}
}

// Name 功能名称
func (f *BalanceFeature) Name() string {
	return "upstream_balance_adjust"
}

// Priority 功能优先级
func (f *BalanceFeature) Priority() int {
	return 19
}

// AllowedGroupTiers 仅上游群
func (f *BalanceFeature) AllowedGroupTiers() []models.GroupTier {
	return []models.GroupTier{models.GroupTierUpstream}
}

// Enabled 功能开关
func (f *BalanceFeature) Enabled(ctx context.Context, group *models.Group) bool {
	return len(group.Settings.InterfaceBindings) > 0
}

// Match 匹配 +100 或 -50
func (f *BalanceFeature) Match(ctx context.Context, msg *botModels.Message) bool {
	if msg == nil || msg.Text == "" {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	return balanceCommandPattern.MatchString(text)
}

// Process 执行加减
func (f *BalanceFeature) Process(ctx context.Context, msg *botModels.Message, group *models.Group) (*types.Response, bool, error) {
	isAdmin, err := f.userService.CheckAdminPermission(ctx, msg.From.ID)
	if err != nil {
		logger.L().Errorf("Failed to check admin permission: user_id=%d err=%v", msg.From.ID, err)
		return respond("❌ 权限检查失败"), true, nil
	}
	if !isAdmin {
		return respond("❌ 仅管理员可以调整余额"), true, nil
	}

	text := strings.TrimSpace(msg.Text)
	amount, _ := strconv.ParseFloat(text[1:], 64)
	if amount <= 0 {
		return respond("❌ 金额需大于0"), true, nil
	}

	var updated *models.UpstreamBalance
	var shouldWarn bool
	if strings.HasPrefix(text, "+") {
		updated, shouldWarn, err = f.balanceService.Add(ctx, msg.Chat.ID, msg.From.ID, amount)
	} else {
		updated, shouldWarn, err = f.balanceService.Subtract(ctx, msg.Chat.ID, msg.From.ID, amount)
	}
	if err != nil {
		logger.L().Errorf("Failed to adjust balance: chat_id=%d user=%d amount=%s err=%v", msg.Chat.ID, msg.From.ID, text, err)
		return respond("❌ 余额调整失败，请稍后重试"), true, nil
	}

	message := fmt.Sprintf("✅ 操作成功，当前余额：%.2f", updated.Balance)
	if shouldWarn {
		message = fmt.Sprintf("%s\n⚠️ 余额已低于最低阈值（%.2f）", message, updated.MinBalance)
	}
	return respond(message), true, nil
}
