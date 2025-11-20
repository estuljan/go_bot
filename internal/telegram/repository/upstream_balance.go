package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go_bot/internal/logger"
	"go_bot/internal/telegram/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
)

var errOperationAlreadyApplied = errors.New("operation already applied")

// MongoUpstreamBalanceRepository 上游群余额 Mongo 实现
type MongoUpstreamBalanceRepository struct {
	db              *mongo.Database
	balanceColl     *mongo.Collection
	balanceLogsColl *mongo.Collection
}

// NewMongoUpstreamBalanceRepository 创建仓储实现
func NewMongoUpstreamBalanceRepository(db *mongo.Database) UpstreamBalanceRepository {
	return &MongoUpstreamBalanceRepository{
		db:              db,
		balanceColl:     db.Collection("upstream_balances"),
		balanceLogsColl: db.Collection("upstream_balance_logs"),
	}
}

// GetBalance 获取群余额
func (r *MongoUpstreamBalanceRepository) GetBalance(ctx context.Context, chatID int64) (*models.UpstreamBalance, error) {
	var balance models.UpstreamBalance
	err := r.balanceColl.FindOne(ctx, bson.M{"chat_id": chatID}).Decode(&balance)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query upstream balance: %w", err)
	}
	return &balance, nil
}

// AdjustBalance 调整余额并记录日志（事务内）
func (r *MongoUpstreamBalanceRepository) AdjustBalance(ctx context.Context, chatID, userID int64, delta float64, entryType, remark, operationID string) (*models.UpstreamBalance, error) {
	session, err := r.db.Client().StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var updated *models.UpstreamBalance
	txnOpts := options.Transaction().SetReadConcern(readconcern.Snapshot())
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		now := time.Now()

		if operationID != "" {
			var existingLog models.UpstreamBalanceLog
			err := r.balanceLogsColl.FindOne(sc, bson.M{"chat_id": chatID, "operation_id": operationID}).Decode(&existingLog)
			if err == nil {
				var balance models.UpstreamBalance
				if err := r.balanceColl.FindOne(sc, bson.M{"chat_id": chatID}).Decode(&balance); err != nil {
					if errors.Is(err, mongo.ErrNoDocuments) {
						return nil, fmt.Errorf("balance not found for existing operation: %w", err)
					}
					return nil, fmt.Errorf("failed to load balance for existing operation: %w", err)
				}
				updated = &balance
				return nil, errOperationAlreadyApplied
			}
			if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
				return nil, fmt.Errorf("failed to check existing balance log: %w", err)
			}
		}

		update := bson.M{
			"$inc": bson.M{"balance": delta},
			"$set": bson.M{"updated_at": now},
			"$setOnInsert": bson.M{
				"chat_id":              chatID,
				"min_balance":          0.0,
				"alert_limit_per_hour": 0,
				"created_at":           now,
			},
		}

		opts := options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After)

		var balance models.UpstreamBalance
		if err := r.balanceColl.FindOneAndUpdate(sc, bson.M{"chat_id": chatID}, update, opts).Decode(&balance); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return nil, fmt.Errorf("failed to upsert balance: %w", err)
			}
			return nil, fmt.Errorf("failed to update balance: %w", err)
		}

		logEntry := &models.UpstreamBalanceLog{
			ChatID:       chatID,
			UserID:       userID,
			Delta:        delta,
			BalanceAfter: balance.Balance,
			Type:         entryType,
			Remark:       remark,
			OperationID:  operationID,
			CreatedAt:    now,
		}

		if _, err := r.balanceLogsColl.InsertOne(sc, logEntry); err != nil {
			return nil, fmt.Errorf("failed to insert balance log: %w", err)
		}

		updated = &balance
		return nil, nil
	}, txnOpts)

	if err != nil {
		if errors.Is(err, errOperationAlreadyApplied) {
			return updated, nil
		}
		return nil, err
	}
	return updated, nil
}

// SetMinBalance 更新最低余额阈值
func (r *MongoUpstreamBalanceRepository) SetMinBalance(ctx context.Context, chatID int64, min float64) (*models.UpstreamBalance, error) {
	now := time.Now()
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	update := bson.M{
		"$set": bson.M{
			"min_balance": min,
			"updated_at":  now,
		},
		"$setOnInsert": bson.M{
			"chat_id":              chatID,
			"balance":              0.0,
			"alert_limit_per_hour": 0,
			"created_at":           now,
		},
	}

	var balance models.UpstreamBalance
	if err := r.balanceColl.FindOneAndUpdate(ctx, bson.M{"chat_id": chatID}, update, opts).Decode(&balance); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("failed to upsert min balance: %w", err)
		}
		return nil, fmt.Errorf("failed to update min balance: %w", err)
	}
	return &balance, nil
}

// SetAlertLimit 更新余额告警限频
func (r *MongoUpstreamBalanceRepository) SetAlertLimit(ctx context.Context, chatID int64, limit int) (*models.UpstreamBalance, error) {
	now := time.Now()
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	update := bson.M{
		"$set": bson.M{
			"alert_limit_per_hour": limit,
			"updated_at":           now,
		},
		"$setOnInsert": bson.M{
			"chat_id":     chatID,
			"balance":     0.0,
			"min_balance": 0.0,
			"created_at":  now,
		},
	}

	var balance models.UpstreamBalance
	if err := r.balanceColl.FindOneAndUpdate(ctx, bson.M{"chat_id": chatID}, update, opts).Decode(&balance); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("failed to upsert alert limit: %w", err)
		}
		return nil, fmt.Errorf("failed to update alert limit: %w", err)
	}
	return &balance, nil
}

// ListBalances 返回所有余额配置
func (r *MongoUpstreamBalanceRepository) ListBalances(ctx context.Context) ([]*models.UpstreamBalance, error) {
	cursor, err := r.balanceColl.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to list balances: %w", err)
	}
	defer cursor.Close(ctx)

	var balances []*models.UpstreamBalance
	if err := cursor.All(ctx, &balances); err != nil {
		return nil, fmt.Errorf("failed to decode balances: %w", err)
	}
	return balances, nil
}

// EnsureIndexes 创建索引
func (r *MongoUpstreamBalanceRepository) EnsureIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "chat_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "updated_at", Value: -1}},
		},
	}

	if _, err := r.balanceColl.Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("failed to create upstream balance indexes: %w", err)
	}

	logIndexes := []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "chat_id", Value: 1}, {Key: "created_at", Value: -1}},
		},
		{
			Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "operation_id", Value: 1}},
			Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{"operation_id": bson.M{"$gt": ""}}),
		},
	}

	if _, err := r.balanceLogsColl.Indexes().CreateMany(ctx, logIndexes); err != nil {
		return fmt.Errorf("failed to create upstream balance log indexes: %w", err)
	}

	logger.L().Info("Upstream balance indexes ensured")
	return nil
}
