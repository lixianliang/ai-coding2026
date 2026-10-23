package game

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	helloTimeout       = 10 * time.Second
	writeTimeout       = 10 * time.Second
	websocketReadLimit = 1 << 20
	outboundQueueSize  = 256
	commandQueueSize   = 256
	externalTimeout    = 8 * time.Second
)

var exitPhrases = []string{"退出游戏", "退出房间", "不玩了"}

type Deps struct {
	Store        *Store
	ASR          ASR
	TTS          TTS
	Judge        Judge
	Codec        AudioCodec
	Logger       *slog.Logger
	TurnDuration time.Duration
	GameDuration time.Duration
}

type Manager struct {
	rooms map[int]*room
}

func NewManager(deps Deps) (*Manager, error) {
	if deps.Store == nil || deps.ASR == nil || deps.TTS == nil || deps.Judge == nil || deps.Codec == nil {
		return nil, errors.New("game dependencies are required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.TurnDuration <= 0 {
		deps.TurnDuration = 60 * time.Second
	}
	if deps.GameDuration <= 0 {
		deps.GameDuration = 10 * time.Minute
	}
	manager := &Manager{rooms: make(map[int]*room, RoomMax-RoomMin+1)}
	for roomID := RoomMin; roomID <= RoomMax; roomID++ {
		manager.rooms[roomID] = newRoom(roomID, deps)
	}
	return manager, nil
}

func (m *Manager) Close() {
	for _, room := range m.rooms {
		room.Close()
	}
}

func (m *Manager) Serve(ctx context.Context, conn *websocket.Conn, roomID int, deviceID string, version ProtocolVersion) error {
	room, ok := m.rooms[roomID]
	if !ok {
		return fmt.Errorf("invalid room %d", roomID)
	}
	conn.SetReadLimit(websocketReadLimit)
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return fmt.Errorf("set hello deadline: %w", err)
	}
	var hello Message
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("read device hello: %w", err)
	}
	if err := ValidateHello(hello); err != nil {
		return err
	}
	response := Message{
		Type:      messageTypeHello,
		Transport: websocketTransport,
		SessionID: newSessionID(),
		AudioParams: &AudioParams{
			Format:        audioFormatOpus,
			SampleRate:    AudioSampleRate,
			Channels:      AudioChannels,
			FrameDuration: AudioFrameDuration,
		},
	}
	if err := conn.WriteJSON(response); err != nil {
		return fmt.Errorf("write server hello: %w", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear hello deadline: %w", err)
	}

	asrSession, err := room.deps.ASR.Open(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("open ASR session: %w", err)
	}
	client := &client{
		deviceID: deviceID,
		version:  version,
		conn:     conn,
		out:      make(chan outbound, outboundQueueSize),
		done:     make(chan struct{}),
		asr:      asrSession,
		logger:   room.deps.Logger.With("room_id", roomID, "device_id", deviceID),
	}
	go client.writeLoop()
	defer client.close()
	defer func() {
		if err := asrSession.Close(); err != nil {
			client.logger.Warn("close ASR session", "error", err)
		}
	}()

	ack := make(chan error, 1)
	if !room.submit(roomCommand{kind: commandJoin, client: client, ack: ack}) {
		return errors.New("room is closed")
	}
	if err := <-ack; err != nil {
		client.sendMessage(Message{Type: messageTypeError, Message: err.Error()})
		return err
	}
	defer room.submit(roomCommand{kind: commandLeave, client: client})

	go func() {
		for event := range asrSession.Events() {
			if !room.submit(roomCommand{kind: commandASR, client: client, asrEvent: event}) {
				return
			}
		}
	}()

	return client.readLoop(room)
}

type outbound struct {
	messageType int
	data        []byte
}

type client struct {
	deviceID  string
	version   ProtocolVersion
	conn      *websocket.Conn
	out       chan outbound
	done      chan struct{}
	closeOnce sync.Once
	asr       ASRSession
	logger    *slog.Logger
	eligible  bool
	muted     bool
}

func (c *client) readLoop(room *room) error {
	for {
		messageType, data, err := c.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read websocket: %w", err)
		}
		switch messageType {
		case websocket.TextMessage:
			var message Message
			if err := json.Unmarshal(data, &message); err != nil {
				c.sendMessage(Message{Type: messageTypeError, Message: "invalid JSON message"})
				continue
			}
			// listen start/stop only describes device microphone state. Qwen VAD
			// drives room turns, so no server-side state change is needed.
		case websocket.BinaryMessage:
			payload, err := ParseAudioPacket(c.version, data)
			if err != nil {
				c.sendMessage(Message{Type: messageTypeError, Message: "invalid audio packet"})
				continue
			}
			if err := c.asr.Push(context.Background(), payload); err != nil {
				c.logger.Warn("push audio to ASR", "error", err)
				continue
			}
			if !room.submit(roomCommand{kind: commandAudio, client: c, audio: payload}) {
				return errors.New("room is closed")
			}
		}
	}
}

func (c *client) writeLoop() {
	defer func() {
		if err := c.conn.Close(); err != nil {
			c.logger.Debug("close websocket", "error", err)
		}
	}()
	for {
		select {
		case frame := <-c.out:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				c.logger.Warn("set websocket write deadline", "error", err)
				return
			}
			if err := c.conn.WriteMessage(frame.messageType, frame.data); err != nil {
				c.logger.Debug("write websocket", "error", err)
				return
			}
		case <-c.done:
			err := c.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			if err != nil {
				c.logger.Debug("write websocket close", "error", err)
			}
			return
		}
	}
}

func (c *client) send(messageType int, data []byte) bool {
	select {
	case c.out <- outbound{messageType: messageType, data: data}:
		return true
	case <-c.done:
		return false
	default:
		c.close()
		return false
	}
}

func (c *client) sendMessage(message Message) bool {
	data, err := json.Marshal(message)
	if err != nil {
		c.logger.Error("marshal websocket message", "error", err)
		return false
	}
	return c.send(websocket.TextMessage, data)
}

func (c *client) sendAudio(payload []byte) bool {
	packet, err := EncodeAudioPacket(c.version, uint32(time.Now().UnixMilli()), payload)
	if err != nil {
		c.logger.Error("encode websocket audio packet", "error", err)
		return false
	}
	return c.send(websocket.BinaryMessage, packet)
}

func (c *client) close() {
	c.closeOnce.Do(func() { close(c.done) })
}

func newSessionID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("read random session id: %v", err))
	}
	return hex.EncodeToString(value)
}

type roomPhase uint8

const (
	phaseIdle roomPhase = iota
	phaseHosting
	phaseAwaitingQuestion
	phaseSpeaking
	phaseJudging
)

type speechAction uint8

const (
	actionOpenQuestions speechAction = iota
	actionFinishGame
)

type commandKind uint8

const (
	commandJoin commandKind = iota
	commandLeave
	commandAudio
	commandASR
	commandSpeechDone
	commandJudgeDone
	commandTurnTimeout
	commandGameTimeout
)

type roomCommand struct {
	kind     commandKind
	client   *client
	audio    []byte
	asrEvent ASREvent
	seq      uint64
	text     string
	deviceID string
	verdict  Verdict
	err      error
	ack      chan error
}

type activeGame struct {
	recordID uint
	question Question
}

type room struct {
	id        int
	deps      Deps
	commands  chan roomCommand
	done      chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once

	players map[string]*client
	order   []*client
	speaker *client
	active  *client

	phase        roomPhase
	current      *activeGame
	lastQuestion uint
	gameSeq      uint64
	turnSeq      uint64
	speechSeq    uint64
	partial      string
	timeExpired  bool
	speechAction speechAction
	speechWinner string
	gameTimer    *time.Timer
	turnTimer    *time.Timer
}

func newRoom(id int, deps Deps) *room {
	r := &room{
		id:       id,
		deps:     deps,
		commands: make(chan roomCommand, commandQueueSize),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
		players:  make(map[string]*client, MaxPlayersPerRoom),
		order:    make([]*client, 0, MaxPlayersPerRoom),
	}
	go r.run()
	return r
}

func (r *room) submit(command roomCommand) bool {
	select {
	case r.commands <- command:
		return true
	case <-r.done:
		return false
	}
}

func (r *room) Close() {
	r.closeOnce.Do(func() { close(r.done) })
	<-r.stopped
}

func (r *room) run() {
	defer close(r.stopped)
	defer r.stopTimers()
	for {
		select {
		case <-r.done:
			for _, client := range r.order {
				client.close()
			}
			return
		case command := <-r.commands:
			switch command.kind {
			case commandJoin:
				r.handleJoin(command)
			case commandLeave:
				r.handleLeave(command.client)
			case commandAudio:
				r.handleAudio(command.client, command.audio)
			case commandASR:
				r.handleASREvent(command.client, command.asrEvent)
			case commandSpeechDone:
				r.handleSpeechDone(command.seq)
			case commandJudgeDone:
				r.handleJudgeDone(command)
			case commandTurnTimeout:
				r.handleTurnTimeout(command.seq)
			case commandGameTimeout:
				r.handleGameTimeout(command.seq)
			}
		}
	}
}

func (r *room) handleJoin(command roomCommand) {
	client := command.client
	if _, exists := r.players[client.deviceID]; exists {
		command.ack <- errors.New("device already joined")
		return
	}
	if len(r.players) >= MaxPlayersPerRoom {
		command.ack <- errors.New("room is full")
		return
	}
	r.players[client.deviceID] = client
	r.order = append(r.order, client)
	if r.speaker == nil {
		r.speaker = client
	}
	if r.current == nil {
		client.eligible = true
		if err := r.startGame(); err != nil {
			r.removeClient(client)
			command.ack <- err
			return
		}
	} else {
		client.eligible = false
		r.mute(client)
	}
	command.ack <- nil
}

func (r *room) handleLeave(client *client) {
	if _, exists := r.players[client.deviceID]; !exists {
		return
	}
	r.removeClient(client)
	if len(r.order) == 0 {
		r.stopTimers()
		if r.current != nil {
			if err := r.deps.Store.EndGame(context.Background(), r.current.recordID, ""); err != nil {
				r.deps.Logger.Error("end abandoned game", "room_id", r.id, "error", err)
			}
		}
		r.current = nil
		r.active = nil
		r.speaker = nil
		r.phase = phaseIdle
		return
	}
	if r.active == client {
		r.active = nil
		r.stopTurnTimer()
		if r.phase == phaseSpeaking {
			r.phase = phaseAwaitingQuestion
			r.unmuteEligible()
		}
	}
}

func (r *room) removeClient(client *client) {
	delete(r.players, client.deviceID)
	filtered := r.order[:0]
	for _, item := range r.order {
		if item != client {
			filtered = append(filtered, item)
		}
	}
	r.order = filtered
	if r.speaker == client {
		r.speaker = nil
		if len(r.order) > 0 {
			r.speaker = r.order[0]
		}
	}
}

func (r *room) handleAudio(client *client, payload []byte) {
	if r.phase != phaseSpeaking || r.active != client || r.speaker == nil || r.speaker == client {
		return
	}
	r.speaker.sendAudio(payload)
}

func (r *room) handleASREvent(client *client, event ASREvent) {
	switch event.Kind {
	case ASREventStarted:
		if r.phase != phaseAwaitingQuestion || !client.eligible || r.active != nil || r.timeExpired {
			return
		}
		r.phase = phaseSpeaking
		r.active = client
		r.partial = ""
		r.turnSeq++
		turnSeq := r.turnSeq
		for _, other := range r.order {
			if other != client && other.eligible {
				r.mute(other)
			}
		}
		r.turnTimer = time.AfterFunc(r.deps.TurnDuration, func() {
			r.submit(roomCommand{kind: commandTurnTimeout, seq: turnSeq})
		})
	case ASREventPartial:
		if r.phase == phaseSpeaking && r.active == client {
			r.partial = event.Text
		}
	case ASREventFinal:
		if r.phase != phaseSpeaking || r.active != client {
			return
		}
		if isExitPhrase(event.Text) {
			client.close()
			r.handleLeave(client)
			return
		}
		r.finishTurn(client, event.Text)
	}
}

func (r *room) finishTurn(client *client, text string) {
	if strings.TrimSpace(text) == "" {
		text = r.partial
	}
	r.stopTurnTimer()
	r.active = nil
	r.phase = phaseJudging
	gameSeq := r.gameSeq
	question := r.current.question
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
		defer cancel()
		verdict, err := r.deps.Judge.Judge(ctx, question.Surface, question.Answer, text)
		r.submit(roomCommand{
			kind:     commandJudgeDone,
			seq:      gameSeq,
			text:     text,
			deviceID: client.deviceID,
			verdict:  verdict,
			err:      err,
		})
	}()
}

func (r *room) handleJudgeDone(command roomCommand) {
	if command.seq != r.gameSeq || r.phase != phaseJudging || r.current == nil {
		return
	}
	if command.err != nil {
		r.deps.Logger.Warn("judge failed", "room_id", r.id, "error", command.err)
		if r.timeExpired {
			r.speakTimeoutAnswer()
			return
		}
		r.beginSpeech(VerdictSpeech(VerdictIrrelevant), actionOpenQuestions, "")
		return
	}
	if r.timeExpired {
		r.speakTimeoutAnswer()
		return
	}
	if command.verdict == VerdictCorrect {
		r.beginSpeech("猜对了。汤底是："+r.current.question.Answer, actionFinishGame, command.deviceID)
		return
	}
	r.beginSpeech(VerdictSpeech(command.verdict), actionOpenQuestions, "")
}

func (r *room) handleTurnTimeout(seq uint64) {
	if seq != r.turnSeq || r.phase != phaseSpeaking || r.active == nil {
		return
	}
	client := r.active
	r.finishTurn(client, r.partial)
}

func (r *room) handleGameTimeout(seq uint64) {
	if seq != r.gameSeq || r.current == nil || r.timeExpired {
		return
	}
	r.timeExpired = true
	switch r.phase {
	case phaseAwaitingQuestion, phaseSpeaking:
		r.speakTimeoutAnswer()
	}
}

func (r *room) startGame() error {
	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()
	question, err := r.deps.Store.NextQuestion(ctx, r.lastQuestion)
	if err != nil {
		return fmt.Errorf("load next question: %w", err)
	}
	recordID, err := r.deps.Store.StartGame(ctx, r.id, question.ID)
	if err != nil {
		return fmt.Errorf("record game start: %w", err)
	}
	r.current = &activeGame{recordID: recordID, question: question}
	r.lastQuestion = question.ID
	r.gameSeq++
	r.phase = phaseHosting
	r.timeExpired = false
	r.partial = ""
	r.active = nil
	for _, client := range r.order {
		client.eligible = true
	}
	gameSeq := r.gameSeq
	r.gameTimer = time.AfterFunc(r.deps.GameDuration, func() {
		r.submit(roomCommand{kind: commandGameTimeout, seq: gameSeq})
	})
	r.beginSpeech(question.Surface, actionOpenQuestions, "")
	return nil
}

func (r *room) beginSpeech(text string, action speechAction, winner string) {
	if r.current == nil || r.speaker == nil {
		return
	}
	r.phase = phaseHosting
	r.speechSeq++
	r.speechAction = action
	r.speechWinner = winner
	speaker := r.speaker
	for _, client := range r.order {
		r.mute(client)
	}
	for _, client := range r.order {
		client.sendMessage(Message{Type: messageTypeTTS, State: messageStateSentenceStart, Text: text})
	}
	seq := r.speechSeq
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
		defer cancel()
		pcm, err := r.deps.TTS.Synthesize(ctx, text)
		if err != nil {
			r.deps.Logger.Warn("synthesize host speech", "room_id", r.id, "error", err)
		} else {
			packets, err := r.deps.Codec.EncodePCM(pcm)
			if err != nil {
				r.deps.Logger.Warn("encode host speech", "room_id", r.id, "error", err)
			} else {
				for _, packet := range packets {
					speaker.sendAudio(packet)
				}
			}
		}
		r.submit(roomCommand{kind: commandSpeechDone, seq: seq})
	}()
}

func (r *room) handleSpeechDone(seq uint64) {
	if seq != r.speechSeq || r.phase != phaseHosting {
		return
	}
	r.unmuteEligible()
	if r.speechAction == actionFinishGame {
		r.finishGame()
		return
	}
	if r.timeExpired {
		r.speakTimeoutAnswer()
		return
	}
	r.phase = phaseAwaitingQuestion
}

func (r *room) speakTimeoutAnswer() {
	if r.current == nil {
		return
	}
	r.beginSpeech("时间到。本局汤底是："+r.current.question.Answer, actionFinishGame, "")
}

func (r *room) finishGame() {
	r.stopTimers()
	winner := r.speechWinner
	if r.current != nil {
		if err := r.deps.Store.EndGame(context.Background(), r.current.recordID, winner); err != nil {
			r.deps.Logger.Error("end game record", "room_id", r.id, "error", err)
		}
	}
	r.current = nil
	r.active = nil
	r.partial = ""
	r.timeExpired = false
	r.phase = phaseIdle
	if len(r.order) == 0 {
		return
	}
	if err := r.startGame(); err != nil {
		r.deps.Logger.Error("start next game", "room_id", r.id, "error", err)
	}
}

func (r *room) mute(client *client) {
	if client.muted {
		return
	}
	client.muted = true
	client.sendMessage(Message{Type: messageTypeTTS, State: messageStateStart})
}

func (r *room) unmuteEligible() {
	for _, client := range r.order {
		if !client.eligible || !client.muted {
			continue
		}
		client.muted = false
		client.sendMessage(Message{Type: messageTypeTTS, State: messageStateStop})
	}
}

func (r *room) stopTurnTimer() {
	if r.turnTimer != nil {
		r.turnTimer.Stop()
		r.turnTimer = nil
	}
}

func (r *room) stopTimers() {
	if r.gameTimer != nil {
		r.gameTimer.Stop()
		r.gameTimer = nil
	}
	r.stopTurnTimer()
}

func isExitPhrase(text string) bool {
	for _, phrase := range exitPhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
