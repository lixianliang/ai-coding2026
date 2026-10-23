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
	externalTimeout    = 20 * time.Second
)

var (
	exitPhrases     = []string{"退出游戏", "退出房间", "不玩了"}
	continuePhrases = []string{"游戏继续", "继续游戏", "再来一局", "下一局", "继续玩"}
)

const finishedPrompt = "大家可以讨论，想开新一局请说“游戏继续”。"

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
			if message.Type != messageTypeListen {
				continue
			}
			var kind commandKind
			switch message.State {
			case messageStateStart:
				kind = commandListenStart
			case messageStateStop:
				kind = commandListenStop
			default:
				continue
			}
			if !room.submit(roomCommand{kind: kind, client: c}) {
				return errors.New("room is closed")
			}
		case websocket.BinaryMessage:
			payload, err := ParseAudioPacket(c.version, data)
			if err != nil {
				c.sendMessage(Message{Type: messageTypeError, Message: "invalid audio packet"})
				continue
			}
			pcm, err := room.deps.Codec.DecodeOpus(payload)
			if err != nil {
				c.logger.Warn("decode device audio", "error", err)
				continue
			}
			if !room.submit(roomCommand{kind: commandAudio, client: c, audio: pcm}) {
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
			for {
				select {
				case frame := <-c.out:
					if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
						return
					}
					if err := c.conn.WriteMessage(frame.messageType, frame.data); err != nil {
						return
					}
				default:
					err := c.conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
					if err != nil {
						c.logger.Debug("write websocket close", "error", err)
					}
					return
				}
			}
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
	phaseListening
	phaseJudging
	phaseFinished
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
	commandListenStart
	commandListenStop
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

	client *client
	active *client

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
	discussion   bool
	capturing    bool
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
			if r.client != nil {
				r.client.close()
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
			case commandListenStart:
				r.handleListenStart(command.client)
			case commandListenStop:
				r.handleListenStop(command.client)
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
	if r.client != nil {
		command.ack <- errors.New("room is full")
		return
	}
	r.client = client
	if err := r.startGame(); err != nil {
		r.client = nil
		command.ack <- err
		return
	}
	command.ack <- nil
}

func (r *room) handleLeave(client *client) {
	if r.client != client {
		return
	}
	r.stopTimers()
	if r.current != nil {
		if err := r.deps.Store.EndGame(context.Background(), r.current.recordID, ""); err != nil {
			r.deps.Logger.Error("end abandoned game", "room_id", r.id, "error", err)
		}
	}
	r.client = nil
	r.active = nil
	r.current = nil
	r.discussion = false
	r.capturing = false
	r.phase = phaseIdle
}

func (r *room) handleListenStart(client *client) {
	if r.client != client || r.active != nil || (r.phase != phaseAwaitingQuestion && r.phase != phaseFinished) {
		return
	}
	r.discussion = r.phase == phaseFinished
	r.phase = phaseListening
	r.active = client
	r.capturing = true
	r.partial = ""
	r.turnSeq++
	turnSeq := r.turnSeq
	r.turnTimer = time.AfterFunc(r.deps.TurnDuration, func() {
		r.submit(roomCommand{kind: commandTurnTimeout, seq: turnSeq})
	})
}

func (r *room) handleListenStop(client *client) {
	if r.phase != phaseListening || r.active != client || !r.capturing {
		return
	}
	r.finishCapture(client)
}

func (r *room) finishCapture(client *client) {
	r.stopTurnTimer()
	r.capturing = false
	if err := client.asr.Finalize(context.Background()); err != nil {
		r.deps.Logger.Warn("finalize ASR audio", "room_id", r.id, "error", err)
		if r.timeExpired {
			r.speakTimeoutAnswer()
		} else {
			r.returnToIdle()
		}
	}
}

func (r *room) returnToIdle() {
	r.stopTurnTimer()
	r.capturing = false
	r.active = nil
	r.partial = ""
	if r.discussion {
		r.discussion = false
		r.phase = phaseFinished
	} else {
		r.phase = phaseAwaitingQuestion
	}
}

func (r *room) handleAudio(client *client, pcm []byte) {
	if r.phase != phaseListening || r.active != client || !r.capturing {
		return
	}
	if err := client.asr.Push(context.Background(), pcm); err != nil {
		r.deps.Logger.Warn("push audio to ASR", "room_id", r.id, "error", err)
	}
}

func (r *room) handleASREvent(client *client, event ASREvent) {
	switch event.Kind {
	case ASREventPartial:
		if r.phase == phaseListening && r.active == client {
			r.partial = event.Text
		}
	case ASREventFinal:
		if r.phase != phaseListening || r.active != client || r.capturing {
			return
		}
		r.stopTurnTimer()
		r.active = nil
		if isExitPhrase(event.Text) {
			client.close()
			r.handleLeave(client)
			return
		}
		if r.discussion {
			r.discussion = false
			r.phase = phaseFinished
			if isContinuePhrase(event.Text) {
				r.partial = ""
				if err := r.startGame(); err != nil {
					r.deps.Logger.Error("start next game", "room_id", r.id, "error", err)
				}
				return
			}
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
		r.beginSpeech("猜对了。汤底是："+formatSpokenAnswer(r.current.question.Answer)+finishedPrompt, actionFinishGame, command.deviceID)
		return
	}
	r.beginSpeech(VerdictSpeech(command.verdict), actionOpenQuestions, "")
}

func (r *room) handleTurnTimeout(seq uint64) {
	if seq != r.turnSeq || r.phase != phaseListening || r.active == nil || !r.capturing {
		return
	}
	r.finishCapture(r.active)
}

func (r *room) handleGameTimeout(seq uint64) {
	if seq != r.gameSeq || r.current == nil || r.timeExpired {
		return
	}
	r.timeExpired = true
	switch r.phase {
	case phaseAwaitingQuestion:
		r.speakTimeoutAnswer()
	case phaseListening:
		if r.capturing {
			r.finishCapture(r.active)
		}
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
	r.discussion = false
	r.capturing = false
	gameSeq := r.gameSeq
	r.gameTimer = time.AfterFunc(r.deps.GameDuration, func() {
		r.submit(roomCommand{kind: commandGameTimeout, seq: gameSeq})
	})
	r.beginSpeech(question.Surface, actionOpenQuestions, "")
	return nil
}

func (r *room) beginSpeech(text string, action speechAction, winner string) {
	if r.current == nil {
		return
	}
	client := r.client
	r.phase = phaseHosting
	r.active = nil
	r.capturing = false
	r.stopTurnTimer()
	r.speechSeq++
	r.speechAction = action
	r.speechWinner = winner
	if client != nil {
		client.sendMessage(Message{Type: messageTypeTTS, State: messageStateStart})
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
			} else if client != nil {
				for _, packet := range packets {
					client.sendAudio(packet)
				}
			}
		}
		if client != nil {
			client.sendMessage(Message{Type: messageTypeTTS, State: messageStateStop})
		}
		r.submit(roomCommand{kind: commandSpeechDone, seq: seq})
	}()
}

func (r *room) handleSpeechDone(seq uint64) {
	if seq != r.speechSeq || r.phase != phaseHosting {
		return
	}
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
	r.beginSpeech("时间到。本局汤底是："+formatSpokenAnswer(r.current.question.Answer)+finishedPrompt, actionFinishGame, "")
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
	r.discussion = false
	r.timeExpired = false
	r.phase = phaseFinished
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

func formatSpokenAnswer(answer string) string {
	return strings.TrimSuffix(answer, "。") + "。"
}

func isExitPhrase(text string) bool {
	for _, phrase := range exitPhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func isContinuePhrase(text string) bool {
	for _, phrase := range continuePhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
