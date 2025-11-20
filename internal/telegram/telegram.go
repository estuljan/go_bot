package telegram

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go_bot/internal/config"
	"go_bot/internal/logger"
	paymentservice "go_bot/internal/payment/service"
	"go_bot/internal/telegram/features"
	"go_bot/internal/telegram/features/calculator"
	"go_bot/internal/telegram/features/crypto"
	"go_bot/internal/telegram/features/merchant"
	sifangfeature "go_bot/internal/telegram/features/sifang"
	"go_bot/internal/telegram/features/upstream"
	"go_bot/internal/telegram/forward"
	"go_bot/internal/telegram/models"
	"go_bot/internal/telegram/repository"
	"go_bot/internal/telegram/service"

	"github.com/go-telegram/bot"
	botModels "github.com/go-telegram/bot/models"
	"go.mongodb.org/mongo-driver/mongo"
)

// Config Telegram Bot 配置
type Config struct {
	Token                string  // Bot Token
	OwnerIDs             []int64 // Owner 用户 IDs
	Debug                bool    // 是否开启调试模式
	MessageRetentionDays int     // 消息保留天数（用于 TTL 索引）
	ChannelID            int64   // 源频道 ID（用于转发功能）
	DailyBillPushEnabled bool    // 是否启用每日账单自动推送
}

// Bot Telegram Bot 服务
type Bot struct {
	bot                  *bot.Bot
	db                   *mongo.Database
	ownerIDs             []int64
	messageRetentionDays int // 消息保留天数
	workerPool           *WorkerPool
	startTime            time.Time
	tempMessageCtx       context.Context
	tempMessageCancel    context.CancelFunc

	// Service 层（业务逻辑）
	userService            service.UserService
	groupService           service.GroupService
	messageService         service.MessageService
	configMenuService      *service.ConfigMenuService
	forwardService         service.ForwardService    // 转发服务
	accountingService      service.AccountingService // 收支记账服务
	upstreamBalanceService service.UpstreamBalanceService
	paymentService         paymentservice.Service

	// 功能管理器
	featureManager *features.Manager
	sifangFeature  *sifangfeature.Feature

	dailySummaryScheduler  *dailySummaryScheduler
	upstreamDailyScheduler *upstreamDailyScheduler

	// Repository 层（仅用于初始化）
	userRepo            repository.UserRepository
	groupRepo           repository.GroupRepository
	messageRepo         repository.MessageRepository
	forwardRecordRepo   repository.ForwardRecordRepository
	accountingRepo      repository.AccountingRepository
	upstreamBalanceRepo repository.UpstreamBalanceRepository

	orderCascadeStates map[string]*orderCascadeState
	orderCascadeMu     sync.RWMutex
}

// New 创建 Telegram Bot 实例
func New(cfg Config, db *mongo.Database, paymentSvc paymentservice.Service) (*Bot, error) {
	// 验证配置
	if cfg.Token == "" {
		return nil, fmt.Errorf("telegram token cannot be empty")
	}

	// 创建 repositories
	userRepo := repository.NewMongoUserRepository(db)
	groupRepo := repository.NewMongoGroupRepository(db)
	messageRepo := repository.NewMongoMessageRepository(db)
	forwardRecordRepo := repository.NewForwardRecordRepository(db)
	accountingRepo := repository.NewMongoAccountingRepository(db)
	upstreamBalanceRepo := repository.NewMongoUpstreamBalanceRepository(db)

	// 创建 services
	userService := service.NewUserService(userRepo)
	groupService := service.NewGroupService(groupRepo)
	messageService := service.NewMessageService(messageRepo, groupRepo)
	configMenuService := service.NewConfigMenuService(groupService)
	accountingService := service.NewAccountingService(accountingRepo, groupRepo)
	upstreamBalanceService := service.NewUpstreamBalanceService(upstreamBalanceRepo, paymentSvc)

	// 创建转发服务（如果配置了频道 ID）
	var forwardService service.ForwardService
	if cfg.ChannelID != 0 {
		forwardService = forward.NewService(
			cfg.ChannelID,
			groupService,
			userService,
			forwardRecordRepo,
		)
		logger.L().Infof("Forward service initialized: channel_id=%d", cfg.ChannelID)
	} else {
		logger.L().Warn("Forward service not initialized: CHANNEL_ID not configured or is 0")
	}

	// 创建功能管理器
	featureManager := features.NewManager(groupService)

	// 创建 worker pool (10 workers, 100 queue size)
	workerPool := NewWorkerPool(10, 100)

	// 创建 bot 实例
	opts := []bot.Option{}
	if cfg.Debug {
		opts = append(opts, bot.WithDebug())
	}

	b, err := bot.New(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	telegramBot := &Bot{
		bot:                    b,
		db:                     db,
		ownerIDs:               cfg.OwnerIDs,
		messageRetentionDays:   cfg.MessageRetentionDays,
		workerPool:             workerPool,
		startTime:              time.Now(),
		userService:            userService,
		groupService:           groupService,
		messageService:         messageService,
		configMenuService:      configMenuService,
		forwardService:         forwardService,
		accountingService:      accountingService,
		upstreamBalanceService: upstreamBalanceService,
		paymentService:         paymentSvc,
		featureManager:         featureManager,
		userRepo:               userRepo,
		groupRepo:              groupRepo,
		messageRepo:            messageRepo,
		forwardRecordRepo:      forwardRecordRepo,
		accountingRepo:         accountingRepo,
		upstreamBalanceRepo:    upstreamBalanceRepo,
		orderCascadeStates:     make(map[string]*orderCascadeState),
	}

	tempCtx, tempCancel := context.WithCancel(context.Background())
	telegramBot.tempMessageCtx = tempCtx
	telegramBot.tempMessageCancel = tempCancel

	// 初始化 owners
	if err := telegramBot.initOwners(context.Background()); err != nil {
		logger.L().Warnf("Failed to initialize owners: %v", err)
	}

	// 注册功能插件
	telegramBot.registerFeatures()

	// 注册 handlers
	telegramBot.registerHandlers()

	// 初始化数据库索引
	if err := telegramBot.ensureIndexes(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ensure indexes: %w", err)
	}

	telegramBot.initDailySummaryScheduler(cfg.DailyBillPushEnabled)
	telegramBot.initUpstreamDailyScheduler()

	logger.L().Info("Telegram bot initialized successfully")
	return telegramBot, nil
}

// asyncHandler 异步 handler 包装器
// 将 handler 提交到 worker pool 异步执行
func (b *Bot) asyncHandler(handler bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, botInstance *bot.Bot, update *botModels.Update) {
		// 提交到 worker pool
		b.workerPool.Submit(HandlerTask{
			Ctx:         ctx,
			BotInstance: botInstance,
			Update:      update,
			Handler:     handler,
		})
	}
}

// InitFromConfig 从应用配置初始化 Telegram Bot
func InitFromConfig(cfg *config.Config, db *mongo.Database, paymentSvc paymentservice.Service) (*Bot, error) {
	telegramCfg := Config{
		Token:                cfg.TelegramToken,
		OwnerIDs:             cfg.BotOwnerIDs,
		Debug:                false, // 可根据需要从环境变量读取
		MessageRetentionDays: cfg.MessageRetentionDays,
		ChannelID:            cfg.ChannelID,
		DailyBillPushEnabled: cfg.DailyBillPushEnabled,
	}
	return New(telegramCfg, db, paymentSvc)
}

// Start 启动 Bot（阻塞式，应在 goroutine 中运行）
func (b *Bot) Start(ctx context.Context) error {
	logger.L().Info("Starting Telegram bot...")
	b.bot.Start(ctx)
	logger.L().Info("Telegram bot stopped")
	return nil
}

// Stop 停止 Bot
func (b *Bot) Stop(ctx context.Context) error {
	logger.L().Info("Stopping Telegram bot...")

	if b.tempMessageCancel != nil {
		b.tempMessageCancel()
		b.tempMessageCancel = nil
		b.tempMessageCtx = nil
	}

	// 关闭 worker pool
	if b.workerPool != nil {
		b.workerPool.Shutdown()
	}

	if b.dailySummaryScheduler != nil {
		b.dailySummaryScheduler.stop()
		b.dailySummaryScheduler = nil
	}

	if b.upstreamDailyScheduler != nil {
		b.upstreamDailyScheduler.stop()
		b.upstreamDailyScheduler = nil
	}

	// bot.Stop() 通过 context 取消实现
	return nil
}

// initOwners 初始化 owner 角色
func (b *Bot) initOwners(ctx context.Context) error {
	for _, ownerID := range b.ownerIDs {
		user, err := b.userRepo.GetByTelegramID(ctx, ownerID)
		if err != nil {
			// 用户不存在，创建 owner 记录
			user = &models.User{
				TelegramID:   ownerID,
				Role:         models.RoleOwner,
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
				LastActiveAt: time.Now(),
			}
			if err := b.userRepo.CreateOrUpdate(ctx, user); err != nil {
				logger.L().Warnf("Failed to create owner %d: %v", ownerID, err)
				continue
			}
			logger.L().Infof("Initialized owner: %d", ownerID)
		} else if user.Role != models.RoleOwner {
			// 用户存在但角色不是 owner，更新为 owner
			user.Role = models.RoleOwner
			user.UpdatedAt = time.Now()
			if err := b.userRepo.CreateOrUpdate(ctx, user); err != nil {
				logger.L().Warnf("Failed to update owner role for %d: %v", ownerID, err)
				continue
			}
			logger.L().Infof("Updated user %d to owner", ownerID)
		}
	}
	return nil
}

// ensureIndexes 确保所有数据库索引存在
func (b *Bot) ensureIndexes(ctx context.Context) error {
	// 计算 TTL 秒数（天数 * 24小时 * 3600秒）
	ttlSeconds := int32(b.messageRetentionDays * 24 * 3600)

	if err := b.userRepo.EnsureIndexes(ctx, ttlSeconds); err != nil {
		return fmt.Errorf("failed to ensure user indexes: %w", err)
	}
	logger.L().Debug("User indexes ensured")

	if err := b.groupRepo.EnsureIndexes(ctx, ttlSeconds); err != nil {
		return fmt.Errorf("failed to ensure group indexes: %w", err)
	}
	logger.L().Debug("Group indexes ensured")

	if err := b.messageRepo.EnsureIndexes(ctx, ttlSeconds); err != nil {
		return fmt.Errorf("failed to ensure message indexes: %w", err)
	}
	logger.L().Infof("Message indexes ensured (TTL: %d days = %d seconds)", b.messageRetentionDays, ttlSeconds)

	// 确保转发记录索引（如果转发服务已启用）
	if b.forwardRecordRepo != nil {
		if err := b.forwardRecordRepo.EnsureIndexes(ctx); err != nil {
			return fmt.Errorf("failed to ensure forward_records indexes: %w", err)
		}
		logger.L().Info("Forward records indexes ensured (TTL: 48 hours)")
	}

	// 确保收支记账索引
	if err := b.accountingRepo.EnsureIndexes(ctx); err != nil {
		return fmt.Errorf("failed to ensure accounting indexes: %w", err)
	}
	logger.L().Debug("Accounting indexes ensured")

	if b.upstreamBalanceRepo != nil {
		if err := b.upstreamBalanceRepo.EnsureIndexes(ctx); err != nil {
			return fmt.Errorf("failed to ensure upstream balance indexes: %w", err)
		}
		logger.L().Debug("Upstream balance indexes ensured")
	}

	return nil
}

func (b *Bot) initDailySummaryScheduler(enabled bool) {
	if !enabled {
		logger.L().Info("Daily bill push disabled via config")
		return
	}

	if b.paymentService == nil {
		logger.L().Warn("Daily bill push not started: payment service is not configured")
		return
	}

	if b.sifangFeature == nil {
		logger.L().Warn("Daily bill push not started: sifang feature is unavailable")
		return
	}

	scheduler := newDailySummaryScheduler(b)
	b.dailySummaryScheduler = scheduler
	scheduler.start()
}

// registerFeatures 注册所有功能插件
func (b *Bot) registerFeatures() {
	// 注册计算器功能
	b.featureManager.Register(calculator.New())

	// 注册商户号绑定功能
	b.featureManager.Register(merchant.New(b.groupService, b.userService))

	// 注册接口绑定功能
	b.featureManager.Register(upstream.New(b.groupService, b.userService))
	b.featureManager.Register(upstream.NewSummaryFeature(b.paymentService))
	b.featureManager.Register(upstream.NewBalanceFeature(b.upstreamBalanceService, b.userService))

	// 注册四方支付功能
	b.sifangFeature = sifangfeature.New(b.paymentService, b.userService)
	b.featureManager.Register(b.sifangFeature)

	// 注册加密货币价格查询功能
	b.featureManager.Register(crypto.New())

	// 后续可添加更多功能:
	// b.featureManager.Register(aichat.New())
	// b.featureManager.Register(reminder.New())

	logger.L().Infof("Registered %d features: %v", len(b.featureManager.ListFeatures()), b.featureManager.ListFeatures())
}
