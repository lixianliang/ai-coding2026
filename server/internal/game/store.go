package game

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const sqliteInMemory = ":memory:"

type Question struct {
	ID      uint   `gorm:"primaryKey"`
	Surface string `gorm:"not null"`
	Answer  string `gorm:"not null"`
	Enabled bool   `gorm:"not null;default:true;index"`
}

var defaultQuestions = []Question{
	{
		ID:      101,
		Surface: "一根点燃的火柴掉进一杯水里，却没有熄灭。为什么？",
		Answer:  "杯子里的水已经冻成了冰，火柴落在冰面上，没有碰到液态水。",
	},
	{
		ID:      102,
		Surface: "一位先生每天坐电梯到十楼，再走楼梯上十二楼；下雨天却直接坐到十二楼。为什么？",
		Answer:  "他个子不高，只能按到十楼按钮；下雨天带着伞，可以用伞尖按十二楼。",
	},
	{
		ID:      103,
		Surface: "小美每天给窗边的植物浇水，它却越来越蔫；移到浴室后偶尔浇水，反而长好了。为什么？",
		Answer:  "那是一盆喜阴喜湿的蕨类植物，窗边暴晒且浇水过多导致烂根，浴室的散射光和湿度更适合它。",
	},
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

func OpenStore(path string, questions []Question) (*Store, error) {
	if len(questions) == 0 {
		questions = defaultQuestions
	}
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
	if err := store.seed(context.Background(), questions); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) seed(ctx context.Context, questions []Question) error {
	ids := make([]uint, 0, len(questions))
	seen := make(map[uint]struct{}, len(questions))
	for index := range questions {
		question := &questions[index]
		if question.ID == 0 || question.Surface == "" || question.Answer == "" {
			return errors.New("question id, surface, and answer are required")
		}
		if _, exists := seen[question.ID]; exists {
			return fmt.Errorf("duplicate question id %d", question.ID)
		}
		seen[question.ID] = struct{}{}
		question.Enabled = true
		ids = append(ids, question.ID)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Question{}).Where("id NOT IN ?", ids).Update("enabled", false).Error; err != nil {
			return fmt.Errorf("disable removed questions: %w", err)
		}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&questions).Error; err != nil {
			return fmt.Errorf("upsert questions: %w", err)
		}
		return nil
	})
}

func (s *Store) NextQuestion(ctx context.Context, excludeID uint) (Question, error) {
	var question Question
	query := s.db.WithContext(ctx).Where("enabled = ?", true).Order("RANDOM()")
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.First(&question).Error; err == nil {
		return question, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Question{}, fmt.Errorf("select question: %w", err)
	}
	if excludeID == 0 {
		return Question{}, errors.New("question bank is empty")
	}
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Order("RANDOM()").First(&question).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Question{}, errors.New("question bank is empty")
		}
		return Question{}, fmt.Errorf("select fallback question: %w", err)
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
