package game

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReturnToIdleFromDiscussion(t *testing.T) {
	room := &room{phase: phaseListening, discussion: true, capturing: true, active: &client{}, partial: "partial"}

	room.returnToIdle()

	require.Equal(t, phaseFinished, room.phase)
	require.False(t, room.discussion)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
	require.Empty(t, room.partial)
}

func TestReturnToIdleDuringRound(t *testing.T) {
	room := &room{phase: phaseListening, capturing: true, active: &client{}}

	room.returnToIdle()

	require.Equal(t, phaseAwaitingQuestion, room.phase)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
}

func TestPhraseMatching(t *testing.T) {
	require.True(t, isExitPhrase("那我先退出游戏吧"))
	require.True(t, isContinuePhrase("我们游戏继续"))
	require.False(t, isContinuePhrase("这局还没讨论完"))
}
