package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"relayproxy/internal/messageutil"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

type PublicPushHandler struct {
	sessions *session.Manager
	db       *repository.DB
	mux      *http.ServeMux
}

func NewPublicPushHandler(sessions *session.Manager, db *repository.DB) *PublicPushHandler {
	handler := &PublicPushHandler{
		sessions: sessions,
		db:       db,
		mux:      http.NewServeMux(),
	}
	handler.mux.HandleFunc("GET /api/v1/push/{channelID}", handler.handleChannelPush)
	handler.mux.HandleFunc("POST /api/v1/push/{channelID}", handler.handleChannelPush)
	return handler
}

func (h *PublicPushHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, req)
}

func (h *PublicPushHandler) handleChannelPush(w http.ResponseWriter, req *http.Request) {
	channelID := strings.TrimSpace(req.PathValue("channelID"))
	if !validMessageChannelID(channelID) {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	channel, err := h.db.GetMessageChannel(channelID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load channel")
		return
	}
	if strings.TrimSpace(channel.IdentityID) == "" {
		writeError(w, http.StatusConflict, "channel identity is not configured")
		return
	}

	var body channelPushRequest
	if req.Method == http.MethodPost && req.ContentLength != 0 {
		if err := decodeJSON(w, req, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	query := req.URL.Query()
	if strings.TrimSpace(body.Title) == "" {
		body.Title = query.Get("title")
	}
	if strings.TrimSpace(body.Message) == "" {
		body.Message = query.Get("message")
	}
	if strings.TrimSpace(body.Content) == "" {
		body.Content = query.Get("content")
	}
	if strings.TrimSpace(body.Source) == "" {
		body.Source = query.Get("source")
	}

	content := strings.TrimSpace(body.Message)
	if content == "" {
		content = strings.TrimSpace(body.Content)
	}
	if content == "" || len(content) > 12000 {
		writeError(w, http.StatusBadRequest, "message must contain 1-12000 characters")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "RelayProxy 消息"
	}
	if len(title) > 200 {
		writeError(w, http.StatusBadRequest, "title is too long")
		return
	}
	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = channel.Name
	}
	if len(source) > 120 {
		writeError(w, http.StatusBadRequest, "source is too long")
		return
	}

	allDevices := channel.AllDevices
	deviceIDs := channel.DeviceIDs
	routeRuleName := ""
	matchedRoute, err := matchRouteRule(content, channel.RouteRules)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to evaluate channel routing rules")
		return
	}
	if matchedRoute != nil {
		allDevices = matchedRoute.AllDevices
		deviceIDs = matchedRoute.DeviceIDs
		routeRuleName = matchedRoute.Name
	}
	targets, err := h.db.ResolveMessageTargetsForIdentity(channel.IdentityID, allDevices, deviceIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve target devices")
		return
	}
	if len(targets) == 0 {
		if matchedRoute != nil {
			writeError(w, http.StatusConflict, "matched routing rule has no approved target devices")
		} else if len(channel.RouteRules) > 0 {
			writeError(w, http.StatusConflict, "message matched no routing rule and channel has no fallback target devices")
		} else {
			writeError(w, http.StatusConflict, "channel has no approved target devices")
		}
		return
	}

	classification := messageutil.MatchMessageRules(
		content, messageutilMessageRules(channel.MessageRules),
	)
	message := &repository.MessageRecord{
		IdentityID:       channel.IdentityID,
		ChannelID:        channel.ID,
		Title:            title,
		Content:          content,
		Source:           source,
		RouteRule:        routeRuleName,
		MessageType:      classification.Type,
		MessageRule:      classification.RuleName,
		VerificationCode: classification.VerificationCode,
		VerificationRule: func() string {
			if classification.Type == messageutil.MessageTypeVerification {
				return classification.RuleName
			}
			return ""
		}(),
		Popup:     classification.Popup,
		PopupType: classification.PopupType,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.db.CreateMessage(message, targets); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist message")
		return
	}

	h.deliverMessage(message)
	writeJSON(w, http.StatusOK, message)
}

func (h *PublicPushHandler) deliverMessage(message *repository.MessageRecord) {
	type deliveryResult struct {
		index       int
		status      string
		error       string
		deliveredAt *time.Time
	}
	results := make(chan deliveryResult, len(message.Deliveries))
	for i := range message.Deliveries {
		i := i
		go func() {
			delivery := message.Deliveries[i]
			sess, online := h.sessions.Get(delivery.DeviceID)
			if !online {
				results <- deliveryResult{index: i, status: "offline", error: "device is offline"}
				return
			}
			if err := h.pushMessage(sess, message); err != nil {
				results <- deliveryResult{index: i, status: "failed", error: err.Error()}
				return
			}
			now := time.Now().UTC()
			results <- deliveryResult{index: i, status: "delivered", deliveredAt: &now}
		}()
	}
	for range message.Deliveries {
		result := <-results
		delivery := &message.Deliveries[result.index]
		delivery.Status = result.status
		delivery.Error = result.error
		delivery.DeliveredAt = result.deliveredAt
		_ = h.db.UpdateMessageDelivery(message.ID, delivery.DeviceID, result.status, result.error, result.deliveredAt)
	}
}

func (h *PublicPushHandler) pushMessage(sess *session.DeviceSession, message *repository.MessageRecord) error {
	if sess == nil || sess.Tunnel == nil {
		return errors.New("device session is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	stream, err := sess.Tunnel.OpenStream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(6 * time.Second))
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic:     protocol.MagicHeader,
		Version:   protocol.CurrentVersion,
		Type:      protocol.FrameTypePushMessage,
		RequestID: message.ID,
	}); err != nil {
		return err
	}
	wire := protocol.PushMessage{
		ID:               message.ID,
		Title:            message.Title,
		Content:          message.Content,
		MessageType:      message.MessageType,
		MessageRule:      message.MessageRule,
		VerificationCode: message.VerificationCode,
		VerificationRule: message.VerificationRule,
		Popup:            message.Popup,
		PopupType:        message.PopupType,
		Source:           message.Source,
		CreatedAt:        message.CreatedAt.UnixMilli(),
	}
	if err := protocol.WriteJSON(stream, wire); err != nil {
		return err
	}
	var receipt protocol.PushMessageReceipt
	if err := protocol.ReadJSON(stream, &receipt); err != nil {
		return err
	}
	if receipt.MessageID != message.ID || !receipt.Received {
		if receipt.Error != "" {
			return errors.New(receipt.Error)
		}
		return errors.New("agent did not acknowledge message")
	}
	return nil
}
