package game

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReturnToIdleFromDiscussion(t *testing.T) {
	room := &room{phase: phaseListening, discussion: true, capturing: true, active: &client{}, current: &activeGame{}, partial: "partial"}

	room.returnToIdle()

	require.Equal(t, phaseFinished, room.phase)
	require.False(t, room.discussion)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
	require.Empty(t, room.partial)
}

func TestReturnToIdleDuringRound(t *testing.T) {
	room := &room{phase: phaseListening, capturing: true, active: &client{}, current: &activeGame{}}

	room.returnToIdle()

	require.Equal(t, phaseAwaitingQuestion, room.phase)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
}

func TestReturnToIdleFromLobby(t *testing.T) {
	room := &room{phase: phaseListening, capturing: true, active: &client{}}

	room.returnToIdle()

	require.Equal(t, phaseLobby, room.phase)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
}

func TestPhraseMatching(t *testing.T) {
	require.True(t, isExitPhrase("那我先退出游戏吧"))
	require.True(t, isContinuePhrase("我们游戏继续"))
	require.False(t, isContinuePhrase("这局还没讨论完"))
	require.True(t, isStartPhrase("开始游戏"))
	require.True(t, isStartPhrase("那就开始新游戏吧"))
	require.False(t, isStartPhrase("还没准备好"))
}
