package game

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpeakerLeaveUnmutesEligiblePlayers(t *testing.T) {
	speaker := &client{deviceID: "speaker", out: make(chan outbound, 1)}
	listener := &client{deviceID: "listener", out: make(chan outbound, 1), eligible: true, muted: true}
	lateJoiner := &client{deviceID: "late", out: make(chan outbound, 1), muted: true}
	room := &room{
		players: map[string]*client{"speaker": speaker, "listener": listener, "late": lateJoiner},
		order:   []*client{speaker, listener, lateJoiner},
		active:  speaker,
		phase:   phaseSpeaking,
	}

	room.handleLeave(speaker)

	require.False(t, listener.muted)
	require.True(t, lateJoiner.muted)
	frame := <-listener.out
	var message Message
	require.NoError(t, json.Unmarshal(frame.data, &message))
	require.Equal(t, messageTypeTTS, message.Type)
	require.Equal(t, messageStateStop, message.State)
}

func TestDiscussionTurnTimeoutReleasesFloor(t *testing.T) {
	speaker := &client{deviceID: "speaker", out: make(chan outbound, 1), eligible: true}
	listener := &client{deviceID: "listener", out: make(chan outbound, 1), eligible: true, muted: true}
	lateJoiner := &client{deviceID: "late", out: make(chan outbound, 1), muted: true}
	room := &room{
		order:   []*client{speaker, listener, lateJoiner},
		active:  speaker,
		phase:   phaseFinished,
		turnSeq: 1,
	}

	room.handleTurnTimeout(1)

	require.Equal(t, phaseFinished, room.phase)
	require.Nil(t, room.active)
	require.False(t, listener.muted)
	require.True(t, lateJoiner.muted)
}
