package game

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type scoredTurn struct {
	player  string
	verdict Verdict
}

func TestScoreSheetRankingAndChampion(t *testing.T) {
	tests := []struct {
		name         string
		turns        []scoredTurn
		wantRanking  []string
		wantChampion string
	}{
		{
			name:  "no named player has no champion",
			turns: []scoredTurn{{"", VerdictCorrect}},
		},
		{
			name:        "zero score has no champion",
			turns:       []scoredTurn{{"小明", VerdictNo}, {"小红", VerdictIrrelevant}},
			wantRanking: []string{"小明", "小红"},
		},
		{
			name:         "higher score wins",
			turns:        []scoredTurn{{"小明", VerdictYes}, {"小红", VerdictClose}},
			wantRanking:  []string{"小红", "小明"},
			wantChampion: "小红",
		},
		{
			name: "tie prefers correct guess",
			turns: []scoredTurn{
				{"小明", VerdictClose}, {"小明", VerdictClose}, {"小明", VerdictYes},
				{"小红", VerdictCorrect},
			},
			wantRanking:  []string{"小红", "小明"},
			wantChampion: "小红",
		},
		{
			name:         "tie keeps first speaker order",
			turns:        []scoredTurn{{"小明", VerdictYes}, {"小红", VerdictYes}},
			wantRanking:  []string{"小明", "小红"},
			wantChampion: "小明",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sheet scoreSheet
			for _, turn := range test.turns {
				sheet.record(turn.player, turn.verdict)
			}
			names := []string{}
			for _, player := range sheet.ranking() {
				names = append(names, player.Name)
			}
			require.Equal(t, append([]string{}, test.wantRanking...), names)
			champion, ok := sheet.champion()
			require.Equal(t, test.wantChampion != "", ok)
			require.Equal(t, test.wantChampion, champion.Name)
		})
	}
}
