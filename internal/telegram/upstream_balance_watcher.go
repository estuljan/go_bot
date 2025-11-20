package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go_bot/internal/logger"
	"go_bot/internal/telegram/models"
)

const (
	defaultAlertLimitPerHour = 3
	periodicBalanceScan      = 10 * time.Minute
)

type balanceAlertState struct {
	low         bool
	windowStart time.Time
	sentCount   int
}

type upstreamBalanceWatcher struct {
	bot     *Bot
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	states  map[int64]*balanceAlertState
	ticker  *time.Ticker
	changes <-chan *models.UpstreamBalance
}

func newUpstreamBalanceWatcher(bot *Bot) *upstreamBalanceWatcher {
	return &upstreamBalanceWatcher{
		bot:     bot,
		states:  make(map[int64]*balanceAlertState),
		changes: bot.upstreamBalanceService.BalanceChanges(),
	}
}

func (b *Bot) initUpstreamBalanceWatcher() {
	if b.upstreamBalanceService == nil {
		logger.L().Warn("Upstream balance watcher not started: balance service is nil")
		return
	}

	watcher := newUpstreamBalanceWatcher(b)
	b.upstreamBalanceWatcher = watcher
	watcher.start()
}

func (w *upstreamBalanceWatcher) start() {
	if w == nil || w.cancel != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	w.ticker = time.NewTicker(periodicBalanceScan)

	go w.run(ctx)
	logger.L().Info("Upstream balance watcher started")
}

func (w *upstreamBalanceWatcher) stop() {
	if w == nil || w.cancel == nil {
		return
	}

	w.cancel()
	<-w.done
	w.cancel = nil
	w.done = nil
	if w.ticker != nil {
		w.ticker.Stop()
		w.ticker = nil
	}
	logger.L().Info("Upstream balance watcher stopped")
}

func (w *upstreamBalanceWatcher) run(ctx context.Context) {
	defer close(w.done)

	for {
		select {
		case <-ctx.Done():
			return
		case balance, ok := <-w.changes:
			if !ok {
				return
			}
			w.evaluateBalance(ctx, balance)
		case <-w.ticker.C:
			w.scanBalances(ctx)
		}
	}
}

func (w *upstreamBalanceWatcher) scanBalances(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	balances, err := w.bot.upstreamBalanceService.List(ctx)
	if err != nil {
		logger.L().Warnf("Failed to list balances for watcher: %v", err)
		return
	}

	for _, balance := range balances {
		w.evaluateBalance(ctx, balance)
	}
}

func (w *upstreamBalanceWatcher) evaluateBalance(ctx context.Context, balance *models.UpstreamBalance) {
	if balance == nil || balance.MinBalance <= 0 {
		return
	}

	low := balance.Balance < balance.MinBalance

	w.mu.Lock()
	state := w.states[balance.ChatID]
	if state == nil {
		state = &balanceAlertState{}
		w.states[balance.ChatID] = state
	}

	now := time.Now()
	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= time.Hour {
		state.windowStart = now
		state.sentCount = 0
	}

	if !low {
		state.low = false
		w.mu.Unlock()
		return
	}

	if state.low {
		w.mu.Unlock()
		return
	}

	limit := balance.AlertLimitPerHour
	if limit <= 0 {
		limit = defaultAlertLimitPerHour
	}

	if state.sentCount >= limit {
		w.mu.Unlock()
		return
	}

	state.low = true
	state.sentCount++
	w.mu.Unlock()

	w.sendLowBalanceAlert(ctx, balance)
}

func (w *upstreamBalanceWatcher) sendLowBalanceAlert(ctx context.Context, balance *models.UpstreamBalance) {
	if balance == nil {
		return
	}

	msg := "⚠️ 上游余额不足\n"
	msg += "当前余额：" + formatAmount(balance.Balance) + "\n"
	msg += "最低余额：" + formatAmount(balance.MinBalance) + "\n"
	msg += "请尽快充值（格式：+1000）或使用 /set_min_balance <金额> 调整阈值。"

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := w.bot.sendMessageWithMarkupAndMessage(sendCtx, balance.ChatID, msg, nil); err != nil {
		if ctx.Err() != nil {
			return
		}
		logger.L().Warnf("Failed to send low balance alert: chat_id=%d err=%v", balance.ChatID, err)
	}
}

func formatAmount(amount float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", amount), "0"), ".")
}
