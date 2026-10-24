package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/lixianliang/ai-coding2026/server/internal/game"
)

const (
	headerDeviceID        = "Device-Id"
	headerClientID        = "Client-Id"
	headerProtocolVersion = "Protocol-Version"
	headerAuthorization   = "Authorization"
	headerRequestID       = "X-Request-Id"
	bearerPrefix          = "Bearer "
	queryRoomID           = "room_id"
	routeHealth           = "/healthz"
	routeGame             = "/v1/game"
	routePlayground       = "/playground"
	routePlaygroundTurn   = "/v1/playground/turn"
	routeScoreboard       = "/scoreboard"
	routeRoomScoreboard   = "/v1/rooms/:room_id/scoreboard"
	paramRoomID           = "room_id"
	successCode           = 0
	badRequestCode        = 40000
	unauthorizedCode      = 40100
	serverErrorCode       = 50000
	playgroundAudioLimit  = 10 << 20
	playgroundTurnTimeout = 90 * time.Second
)

//go:embed playground.html
var playgroundHTML []byte

//go:embed scoreboard.html
var scoreboardHTML []byte

type response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	ReqID   string `json:"reqid"`
	Data    any    `json:"data"`
}

type Handler struct {
	manager  *game.Manager
	pipeline *game.Pipeline
	token    string
	logger   *slog.Logger
	upgrader websocket.Upgrader
}

func NewRouter(manager *game.Manager, pipeline *game.Pipeline, token string, logger *slog.Logger) *gin.Engine {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &Handler{
		manager:  manager,
		pipeline: pipeline,
		token:    token,
		logger:   logger,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
	router := gin.New()
	router.Use(gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logger.Error("panic in HTTP handler", "error", recovered)
		c.AbortWithStatusJSON(http.StatusOK, response{
			Code: serverErrorCode, Message: "internal server error", ReqID: requestID(c), Data: nil,
		})
	}))
	router.GET(routeHealth, handler.health)
	router.GET(routeGame, handler.gameWebSocket)
	router.GET(routePlayground, handler.playground)
	router.POST(routePlaygroundTurn, handler.playgroundTurn)
	router.GET(routeScoreboard, handler.scoreboardPage)
	router.GET(routeRoomScoreboard, handler.roomScoreboard)
	return router
}

func (h *Handler) health(c *gin.Context) {
	c.JSON(http.StatusOK, response{Code: successCode, Message: "ok", ReqID: requestID(c), Data: map[string]string{"status": "ok"}})
}

func (h *Handler) gameWebSocket(c *gin.Context) {
	if !h.authorized(c.GetHeader(headerAuthorization)) {
		c.JSON(http.StatusOK, response{Code: unauthorizedCode, Message: "unauthorized", ReqID: requestID(c), Data: nil})
		return
	}
	roomID, err := strconv.Atoi(c.Query(queryRoomID))
	if err != nil || roomID < game.RoomMin || roomID > game.RoomMax {
		h.badRequest(c, "room_id must be 1, 2, or 3")
		return
	}
	deviceID := strings.TrimSpace(c.GetHeader(headerDeviceID))
	if deviceID == "" || len(deviceID) > 128 {
		h.badRequest(c, "Device-Id header is required")
		return
	}
	if strings.TrimSpace(c.GetHeader(headerClientID)) == "" {
		h.badRequest(c, "Client-Id header is required")
		return
	}
	versionValue, err := strconv.Atoi(c.GetHeader(headerProtocolVersion))
	if err != nil {
		h.badRequest(c, "Protocol-Version header is required")
		return
	}
	version, err := game.ParseProtocolVersion(versionValue)
	if err != nil {
		h.badRequest(c, "unsupported Protocol-Version")
		return
	}
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("upgrade game websocket", "error", err, "device_id", deviceID)
		return
	}
	if err := h.manager.Serve(c.Request.Context(), conn, roomID, deviceID, version); err != nil {
		h.logger.Debug("game websocket ended", "error", err, "room_id", roomID, "device_id", deviceID)
	}
}

func (h *Handler) playground(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", playgroundHTML)
}

func (h *Handler) playgroundTurn(c *gin.Context) {
	if !h.authorized(c.GetHeader(headerAuthorization)) {
		c.JSON(http.StatusOK, response{Code: unauthorizedCode, Message: "unauthorized", ReqID: requestID(c), Data: nil})
		return
	}
	if h.pipeline == nil {
		c.JSON(http.StatusOK, response{Code: serverErrorCode, Message: "playground is not configured", ReqID: requestID(c), Data: nil})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, playgroundAudioLimit)
	pcm, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.badRequest(c, "PCM body is too large")
		return
	}
	if len(pcm) == 0 {
		h.badRequest(c, "PCM body is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), playgroundTurnTimeout)
	defer cancel()
	turn, err := h.pipeline.Turn(ctx, pcm, game.TurtleSoupDemo.Surface, game.TurtleSoupDemo.Answer)
	if err != nil {
		h.logger.Warn("playground turn failed", "error", err)
		c.JSON(http.StatusOK, response{Code: serverErrorCode, Message: "game turn failed", ReqID: requestID(c), Data: nil})
		return
	}
	c.JSON(http.StatusOK, response{
		Code: successCode, Message: "ok", ReqID: requestID(c),
		Data: map[string]any{
			"surface":          game.TurtleSoupDemo.Surface,
			"transcript":       turn.Transcript,
			"verdict":          turn.Verdict,
			"speech":           turn.Speech,
			"audio_pcm_base64": base64.StdEncoding.EncodeToString(turn.PCM),
			"sample_rate":      game.AudioSampleRate,
			"channels":         game.AudioChannels,
			"bits_per_sample":  16,
		},
	})
}

func (h *Handler) scoreboardPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", scoreboardHTML)
}

// roomScoreboard 供门店大屏只读展示，不要求设备 token。
func (h *Handler) roomScoreboard(c *gin.Context) {
	roomID, err := strconv.Atoi(c.Param(paramRoomID))
	if err != nil || roomID < game.RoomMin || roomID > game.RoomMax {
		h.badRequest(c, "room_id must be 1, 2, or 3")
		return
	}
	board, err := h.manager.Scoreboard(roomID)
	if err != nil {
		h.logger.Warn("read scoreboard", "room_id", roomID, "error", err)
		c.JSON(http.StatusOK, response{Code: serverErrorCode, Message: "scoreboard is unavailable", ReqID: requestID(c), Data: nil})
		return
	}
	c.JSON(http.StatusOK, response{Code: successCode, Message: "ok", ReqID: requestID(c), Data: board})
}

func (h *Handler) authorized(header string) bool {
	if h.token == "" {
		return true
	}
	token := strings.TrimPrefix(header, bearerPrefix)
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.token)) == 1
}

func (h *Handler) badRequest(c *gin.Context, message string) {
	c.JSON(http.StatusOK, response{Code: badRequestCode, Message: message, ReqID: requestID(c), Data: nil})
}

func requestID(c *gin.Context) string {
	if value := strings.TrimSpace(c.GetHeader(headerRequestID)); value != "" {
		return value
	}
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("read request id: %v", err))
	}
	return hex.EncodeToString(value)
}
