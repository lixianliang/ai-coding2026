package game

import (
	"fmt"
	"slices"
)

// 积分规则：问出“是”得 1 分，“接近”得 2 分，猜中汤底得 5 分，其余不得分。
const (
	pointsYes     = 1
	pointsClose   = 2
	pointsCorrect = 5
)

type ScoreboardStatus string

const (
	ScoreboardIdle     ScoreboardStatus = "idle"
	ScoreboardPlaying  ScoreboardStatus = "playing"
	ScoreboardFinished ScoreboardStatus = "finished"
)

type PlayerScore struct {
	Name      string `json:"name"`
	Score     int    `json:"score"`
	Questions int    `json:"questions"`
	Yes       int    `json:"yes"`
	Close     int    `json:"close"`
	Correct   bool   `json:"correct"`
}

// Scoreboard 是结算页展示的一局快照；汤底只在本局结束后给出。
type Scoreboard struct {
	RoomID   int              `json:"room_id"`
	Status   ScoreboardStatus `json:"status"`
	Surface  string           `json:"surface"`
	Answer   string           `json:"answer"`
	Champion string           `json:"champion"`
	Players  []PlayerScore    `json:"players"`
}

// scoreSheet 按玩家首次发言顺序累计一局的成绩，未自报名字的发言不计入。
type scoreSheet struct {
	players []PlayerScore
}

func (s *scoreSheet) record(player string, verdict Verdict) {
	if player == "" {
		return
	}
	index := slices.IndexFunc(s.players, func(p PlayerScore) bool { return p.Name == player })
	if index < 0 {
		s.players = append(s.players, PlayerScore{Name: player})
		index = len(s.players) - 1
	}
	score := &s.players[index]
	score.Questions++
	switch verdict {
	case VerdictYes:
		score.Yes++
		score.Score += pointsYes
	case VerdictClose:
		score.Close++
		score.Score += pointsClose
	case VerdictCorrect:
		score.Correct = true
		score.Score += pointsCorrect
	}
}

// ranking 按积分降序，同分时猜中者优先，再按首次发言顺序。
func (s *scoreSheet) ranking() []PlayerScore {
	ranked := append([]PlayerScore{}, s.players...)
	slices.SortStableFunc(ranked, func(a, b PlayerScore) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		if a.Correct != b.Correct {
			if a.Correct {
				return -1
			}
			return 1
		}
		return 0
	})
	return ranked
}

// champion 返回第一名；没有人得分时不产生冠军。
func (s *scoreSheet) champion() (PlayerScore, bool) {
	ranked := s.ranking()
	if len(ranked) == 0 || ranked[0].Score == 0 {
		return PlayerScore{}, false
	}
	return ranked[0], true
}

func championSpeech(champion PlayerScore) string {
	return fmt.Sprintf("本局冠军是%s，得%d分。", champion.Name, champion.Score)
}
