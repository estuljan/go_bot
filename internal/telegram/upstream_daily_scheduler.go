package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"go_bot/internal/logger"
	"go_bot/internal/telegram/models"
	"go_bot/internal/telegram/service"
)

type upstreamDailyScheduler struct {
	bot      *Bot
	cancel   context.CancelFunc
	done     chan struct{}
	location *time.Location
}

func newUpstreamDailyScheduler(bot *Bot) *upstreamDailyScheduler {
	return &upstreamDailyScheduler{
		bot:      bot,
		location: mustLoadChinaLocation(),
	}
}

func (b *Bot) initUpstreamDailyScheduler() {
	if b.upstreamBalanceService == nil {
		logger.L().Warn("Upstream daily scheduler not started: balance service is nil")
		return
	}
	scheduler := newUpstreamDailyScheduler(b)
	b.upstreamDailyScheduler = scheduler
	scheduler.start()
}

func (s *upstreamDailyScheduler) start() {
	if s == nil || s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})

	go s.run(ctx)
	logger.L().Info("Upstream daily scheduler started")
}

func (s *upstreamDailyScheduler) stop() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel = nil
	s.done = nil
	logger.L().Info("Upstream daily scheduler stopped")
}

func (s *upstreamDailyScheduler) run(ctx context.Context) {
	defer close(s.done)

	for {
		now := time.Now().In(s.location)
		next := nextDailyRun(now, s.location)
		wait := time.Until(next)
		if wait <= 0 {
			wait = time.Second
		}

		timer := time.NewTimer(wait)
		logger.L().Debugf("Upstream daily settlement waiting %s until %s", wait, next.Format(time.RFC3339))

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.dispatch(ctx)
		}
	}
}

func (s *upstreamDailyScheduler) dispatch(parent context.Context) {
	if parent.Err() != nil {
		return
	}

	targetDate := previousBillingDate(time.Now().In(s.location), s.location)
	groups, err := s.bot.groupService.ListActiveGroups(parent)
	if err != nil {
		logger.L().Errorf("Upstream daily settlement failed to list groups: %v", err)
		return
	}

	eligible := filterUpstreamGroups(groups)
	if len(eligible) == 0 {
		logger.L().Infof("Upstream daily settlement skipped: no eligible groups for %s", targetDate.Format("2006-01-02"))
		return
	}

	logger.L().Infof("Upstream daily settlement started for %d groups, target_date=%s", len(eligible), targetDate.Format("2006-01-02"))

	runner, ctx := errgroup.WithContext(parent)
	runner.SetLimit(6)

	for _, group := range eligible {
		group := group
		runner.Go(func() error {
			return s.settleForGroup(ctx, group, targetDate)
		})
	}

	if err := runner.Wait(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		logger.L().Warnf("Upstream daily settlement finished with error: %v", err)
	}
}

func (s *upstreamDailyScheduler) settleForGroup(ctx context.Context, group *models.Group, targetDate time.Time) error {
	attempts := 0
	for attempts < 3 {
		attempts++

		runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		result, err := s.bot.upstreamBalanceService.SettleDaily(runCtx, group, targetDate)
		cancel()

		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			logger.L().Warnf("Upstream daily settlement failed: chat_id=%d attempt=%d err=%v", group.TelegramID, attempts, err)
			continue
		}

		message := formatUpstreamSettlementMessage(result)
		sendCtx, cancelSend := context.WithTimeout(ctx, 10*time.Second)
		_, sendErr := s.bot.sendMessageWithMarkupAndMessage(sendCtx, group.TelegramID, message, nil)
		cancelSend()

		if sendErr != nil {
			if errors.Is(sendErr, context.Canceled) || errors.Is(sendErr, context.DeadlineExceeded) {
				return sendErr
			}
			logger.L().Warnf("Upstream daily settlement send failed: chat_id=%d attempt=%d err=%v", group.TelegramID, attempts, sendErr)
			continue
		}

		logger.L().Infof("Upstream daily settlement sent: chat_id=%d target=%s attempts=%d", group.TelegramID, targetDate.Format("2006-01-02"), attempts)
		return nil
	}

	return fmt.Errorf("upstream settlement failed after %d attempts", attempts)
}

func filterUpstreamGroups(groups []*models.Group) []*models.Group {
	result := make([]*models.Group, 0, len(groups))
	for _, g := range groups {
		if g == nil {
			continue
		}
		if models.NormalizeGroupTier(g.Tier) != models.GroupTierUpstream {
			continue
		}
		if len(g.Settings.InterfaceBindings) == 0 {
			continue
		}
		result = append(result, g)
	}
	return result
}

func formatUpstreamSettlementMessage(result *service.SettlementResult) string {
	if result == nil || result.Balance == nil {
		return "ℹ️ 暂无日结数据"
	}

	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("📊 上游日结 - %s\n", result.Target.Format("2006-01-02")))

	for _, item := range result.Items {
		name := strings.TrimSpace(item.InterfaceName)
		if name == "" {
			name = item.InterfaceID
		}
		builder.WriteString(fmt.Sprintf("• %s：跑量 %.2f × 费率 %.2f%% = 扣减 %.2f\n", name, item.GrossAmount, item.RatePercent, item.Deduction))
	}

	builder.WriteString(fmt.Sprintf("合计扣减：%.2f\n", result.Deduction))
	builder.WriteString(fmt.Sprintf("当前余额：%.2f\n", result.Balance.Balance))

	if result.Balance.MinBalance > 0 {
		builder.WriteString(fmt.Sprintf("最低余额：%.2f\n", result.Balance.MinBalance))
		if result.Balance.Balance < result.Balance.MinBalance {
			builder.WriteString("⚠️ 已低于最低余额阈值，请尽快补足。\n")
		}
	}

	return strings.TrimSpace(builder.String())
}
