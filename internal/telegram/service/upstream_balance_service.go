package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go_bot/internal/logger"
	paymentservice "go_bot/internal/payment/service"
	"go_bot/internal/telegram/models"
	"go_bot/internal/telegram/repository"
)

const (
	upstreamLogTypeManualAdd      = "manual_add"
	upstreamLogTypeManualSubtract = "manual_subtract"
	upstreamLogTypeDaily          = "daily_settlement"
	upstreamLogTypeThreshold      = "set_min_balance"
)

// UpstreamBalanceService 管理上游群余额与日结
type UpstreamBalanceService interface {
	Add(ctx context.Context, chatID, userID int64, amount float64) (*models.UpstreamBalance, bool, error)
	Subtract(ctx context.Context, chatID, userID int64, amount float64) (*models.UpstreamBalance, bool, error)
	Get(ctx context.Context, chatID int64) (*models.UpstreamBalance, error)
	SetMinBalance(ctx context.Context, chatID int64, min float64) (*models.UpstreamBalance, error)
	SettleDaily(ctx context.Context, group *models.Group, targetDate time.Time) (*SettlementResult, error)
}

// SettlementResult 聚合日结结果
type SettlementResult struct {
	Balance   *models.UpstreamBalance
	Items     []models.UpstreamSettlementItem
	Deduction float64
	Target    time.Time
}

type upstreamBalanceService struct {
	repo           repository.UpstreamBalanceRepository
	paymentService paymentservice.Service
	nowFunc        func() time.Time
}

// NewUpstreamBalanceService 创建服务
func NewUpstreamBalanceService(repo repository.UpstreamBalanceRepository, paymentSvc paymentservice.Service) UpstreamBalanceService {
	return &upstreamBalanceService{
		repo:           repo,
		paymentService: paymentSvc,
		nowFunc: func() time.Time {
			return time.Now().In(mustLoadChinaLocation())
		},
	}
}

func (s *upstreamBalanceService) Add(ctx context.Context, chatID, userID int64, amount float64) (*models.UpstreamBalance, bool, error) {
	return s.adjust(ctx, chatID, userID, amount, upstreamLogTypeManualAdd)
}

func (s *upstreamBalanceService) Subtract(ctx context.Context, chatID, userID int64, amount float64) (*models.UpstreamBalance, bool, error) {
	return s.adjust(ctx, chatID, userID, -math.Abs(amount), upstreamLogTypeManualSubtract)
}

func (s *upstreamBalanceService) adjust(ctx context.Context, chatID, userID int64, delta float64, entryType string) (*models.UpstreamBalance, bool, error) {
	if delta == 0 {
		balance, err := s.repo.GetBalance(ctx, chatID)
		return balance, s.shouldWarn(balance), err
	}
	updated, err := s.repo.AdjustBalance(ctx, chatID, userID, delta, entryType, "")
	if err != nil {
		return nil, false, err
	}
	return updated, s.shouldWarn(updated), nil
}

func (s *upstreamBalanceService) Get(ctx context.Context, chatID int64) (*models.UpstreamBalance, error) {
	return s.repo.GetBalance(ctx, chatID)
}

func (s *upstreamBalanceService) SetMinBalance(ctx context.Context, chatID int64, min float64) (*models.UpstreamBalance, error) {
	updated, err := s.repo.SetMinBalance(ctx, chatID, min)
	if err != nil {
		return nil, err
	}
	_, _ = s.repo.AdjustBalance(ctx, chatID, 0, 0, upstreamLogTypeThreshold, fmt.Sprintf("set_min_balance=%.2f", min))
	return updated, nil
}

func (s *upstreamBalanceService) SettleDaily(ctx context.Context, group *models.Group, targetDate time.Time) (*SettlementResult, error) {
	bindings := group.Settings.InterfaceBindings
	if len(bindings) == 0 {
		return nil, fmt.Errorf("当前群未绑定任何接口")
	}

	start := time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), 0, 0, 0, 0, targetDate.Location())
	end := start.Add(24*time.Hour - time.Second)

	items := make([]models.UpstreamSettlementItem, 0, len(bindings))
	totalDeduction := 0.0

	for _, binding := range bindings {
		item, err := s.settleBinding(ctx, binding, start, end, targetDate)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
		totalDeduction += item.Deduction
	}

	balance, err := s.repo.AdjustBalance(ctx, group.TelegramID, 0, -totalDeduction, upstreamLogTypeDaily, targetDate.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}

	return &SettlementResult{
		Balance:   balance,
		Items:     items,
		Deduction: totalDeduction,
		Target:    targetDate,
	}, nil
}

func (s *upstreamBalanceService) settleBinding(ctx context.Context, binding models.InterfaceBinding, start, end, targetDate time.Time) (*models.UpstreamSettlementItem, error) {
	summary, err := s.paymentService.GetSummaryByDayByPZID(ctx, binding.ID, start, end)
	if err != nil {
		return nil, fmt.Errorf("查询接口 %s 账单失败: %w", binding.ID, err)
	}

	grossAmount := parseAmountFromSummary(summary, targetDate)
	rate := parseRatePercent(binding.Rate)
	deduction := grossAmount * rate / 100

	return &models.UpstreamSettlementItem{
		InterfaceID:   binding.ID,
		InterfaceName: binding.Name,
		RatePercent:   rate,
		GrossAmount:   grossAmount,
		Deduction:     deduction,
	}, nil
}

func (s *upstreamBalanceService) shouldWarn(balance *models.UpstreamBalance) bool {
	if balance == nil {
		return false
	}
	if balance.MinBalance <= 0 {
		return false
	}
	return balance.Balance < balance.MinBalance
}

func parseRatePercent(rate string) float64 {
	clean := strings.TrimSpace(rate)
	clean = strings.TrimSuffix(clean, "%")
	if clean == "" {
		return 0
	}
	value, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		logger.L().Warnf("Failed to parse upstream rate %q: %v", rate, err)
		return 0
	}
	return value
}

func parseAmountFromSummary(summary *paymentservice.SummaryByPZID, targetDate time.Time) float64 {
	if summary == nil {
		return 0
	}

	dateStr := targetDate.Format("2006-01-02")
	for _, item := range summary.Items {
		if item == nil {
			continue
		}
		if normalizeSummaryDate(item.Date) != dateStr {
			continue
		}
		amount, err := strconv.ParseFloat(strings.TrimSpace(item.GrossAmount), 64)
		if err != nil {
			logger.L().Warnf("Failed to parse gross amount %q for date %s: %v", item.GrossAmount, dateStr, err)
			return 0
		}
		return amount
	}
	return 0
}

func mustLoadChinaLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

func normalizeSummaryDate(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	layouts := []string{
		"2006-01-02",
		"2006-01-02 15:04:05",
		"2006/01/02",
		"2006/01/02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.Format("2006-01-02")
		}
	}

	if len(trimmed) >= 10 {
		candidate := trimmed[:10]
		if t, err := time.Parse("2006-01-02", candidate); err == nil {
			return t.Format("2006-01-02")
		}
		if t, err := time.Parse("2006/01/02", candidate); err == nil {
			return t.Format("2006-01-02")
		}
	}

	return ""
}
