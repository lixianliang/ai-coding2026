package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	successCode           = 0
	badRequestCode        = 40000
	unauthorizedCode      = 40100
	serverErrorCode       = 50000
)

type response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	ReqID   string `json:"reqid"`
	Data    any    `json:"data"`
}

type Handler struct {
	manager  *game.Manager
	token    string
	logger   *slog.Logger
	upgrader websocket.Upgrader
}

func NewRouter(manager *game.Manager, token string, logger *slog.Logger) *gin.Engine {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &Handler{
		manager: manager,
		token:   token,
		logger:  logger,
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
