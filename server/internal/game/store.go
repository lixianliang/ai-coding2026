package game

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const sqliteInMemory = ":memory:"

type Question struct {
	ID      uint   `gorm:"primaryKey"`
	Surface string `gorm:"not null"`
	Answer  string `gorm:"not null"`
}

type GameRecord struct {
	ID             uint      `gorm:"primaryKey"`
	RoomID         int       `gorm:"not null;index"`
	QuestionID     uint      `gorm:"not null;index"`
	StartedAt      time.Time `gorm:"not null"`
	EndedAt        *time.Time
	WinnerDeviceID string
}

type Store struct {
	db *gorm.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	if path == sqliteInMemory {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("get sqlite connection: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&Question{}, &GameRecord{}); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	store := &Store{db: db}
	if err := store.seed(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) seed(ctx context.Context) error {
	var count int64
	if err := s.db.WithContext(ctx).Model(&Question{}).Count(&count).Error; err != nil {
		return fmt.Errorf("count questions: %w", err)
	}
	if count > 0 {
		return nil
	}
	questions := []Question{
		{
			Surface: "一个人走进餐厅，点了一碗海龟汤，喝了一口就哭了。为什么？",
			Answer:  "他认出这是失踪多年的母亲做的味道，而母亲已经去世。",
		},
		{
			Surface: "男人每天深夜都乘电梯到十三楼，再走楼梯回家。为什么？",
			Answer:  "十三楼是他已故妻子的家，电梯无法直达，他只能用这种方式纪念她。",
		},
		{
			Surface: "女孩在生日当天收到一张空白贺卡，却立刻报了警。为什么？",
			Answer:  "寄卡人只会写盲文，这证明失踪的姐姐还活着并留下了求救信息。",
		},
	}
	if err := s.db.WithContext(ctx).Create(&questions).Error; err != nil {
		return fmt.Errorf("seed questions: %w", err)
	}
	return nil
}

func (s *Store) NextQuestion(ctx context.Context, excludeID uint) (Question, error) {
	var question Question
	query := s.db.WithContext(ctx).Order("RANDOM()")
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.First(&question).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Question{}, errors.New("question bank is empty")
		}
		return Question{}, fmt.Errorf("select question: %w", err)
	}
	return question, nil
}

func (s *Store) StartGame(ctx context.Context, roomID int, questionID uint) (uint, error) {
	record := GameRecord{
		RoomID:     roomID,
		QuestionID: questionID,
		StartedAt:  time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return 0, fmt.Errorf("create game record: %w", err)
	}
	return record.ID, nil
}

func (s *Store) EndGame(ctx context.Context, gameID uint, winnerDeviceID string) error {
	now := time.Now()
	updates := map[string]any{"ended_at": now, "winner_device_id": winnerDeviceID}
	if err := s.db.WithContext(ctx).Model(&GameRecord{}).Where("id = ?", gameID).Updates(updates).Error; err != nil {
		return fmt.Errorf("end game record %d: %w", gameID, err)
	}
	return nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("get sqlite connection: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}
	return nil
}
