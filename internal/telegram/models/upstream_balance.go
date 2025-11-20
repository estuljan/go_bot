package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// UpstreamBalance 表示上游群的余额与阈值配置
type UpstreamBalance struct {
	ID         primitive.ObjectID `bson:"_id,omitempty"`
	ChatID     int64              `bson:"chat_id"`
	Balance    float64            `bson:"balance"`
	MinBalance float64            `bson:"min_balance"`
	// AlertLimitPerHour 控制一小时内最多发送多少条低余额告警，0 使用默认值
	AlertLimitPerHour int       `bson:"alert_limit_per_hour,omitempty"`
	CreatedAt         time.Time `bson:"created_at"`
	UpdatedAt         time.Time `bson:"updated_at"`
}

// UpstreamBalanceLog 记录每一次余额变动
type UpstreamBalanceLog struct {
	ID           primitive.ObjectID `bson:"_id,omitempty"`
	ChatID       int64              `bson:"chat_id"`
	UserID       int64              `bson:"user_id"`
	Delta        float64            `bson:"delta"`
	BalanceAfter float64            `bson:"balance_after"`
	Type         string             `bson:"type"`
	Remark       string             `bson:"remark,omitempty"`
	OperationID  string             `bson:"operation_id,omitempty"`
	CreatedAt    time.Time          `bson:"created_at"`
}

// UpstreamSettlementItem 表示单个接口的日结结果
type UpstreamSettlementItem struct {
	InterfaceID   string
	InterfaceName string
	RatePercent   float64
	GrossAmount   float64
	Deduction     float64
}
