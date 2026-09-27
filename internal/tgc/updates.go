package tgc

import (
	"context"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/aurora/aurora/internal/proto"
)

// handleUpdates is the single entry point for every MTProto update.
//
// The rule that shapes this file: the update path must never block. No RPC
// calls, no locks held across I/O, no unbounded work. Peers are resolved from
// the cache populated by the same batch, and anything unknown is simply
// rendered as a numeric ID.
func (r *Runtime) handleUpdates(ctx context.Context, u tg.UpdatesClass) error {
	switch v := u.(type) {
	case *tg.Updates:
		r.learn(v.Users, v.Chats)
		r.dispatch(ctx, v.Updates)
	case *tg.UpdatesCombined:
		r.learn(v.Users, v.Chats)
		r.dispatch(ctx, v.Updates)
	case *tg.UpdateShort:
		r.dispatch(ctx, []tg.UpdateClass{v.Update})
	case *tg.UpdateShortMessage:
		r.dispatch(ctx, []tg.UpdateClass{r.shortMessage(v)})
	case *tg.UpdateShortChatMessage:
		r.dispatch(ctx, []tg.UpdateClass{r.shortChatMessage(v)})
	case *tg.UpdateShortSentMessage:
		r.dispatch(ctx, []tg.UpdateClass{r.shortSentMessage(v)})
	case *tg.UpdatesTooLong:
		// The server dropped our update state; the next getDifference fixes it.
		r.log.Warn("updates dropped by server, requesting difference")
	}
	return nil
}

// shortMessage rebuilds a full update from an UpdateShortMessage, which is
// what Telegram sends for most private chats.
func (r *Runtime) shortMessage(v *tg.UpdateShortMessage) tg.UpdateClass {
	m := &tg.Message{
		ID:      v.ID,
		PeerID:  &tg.PeerUser{UserID: v.UserID},
		Message: v.Message,
		Date:    v.Date,
		Out:     v.Out,
	}
	if !v.Out {
		m.FromID = &tg.PeerUser{UserID: v.UserID}
	}
	m.SetMentioned(v.Mentioned)
	m.SetMediaUnread(v.MediaUnread)
	m.SetSilent(v.Silent)
	return &tg.UpdateNewMessage{Message: m, Pts: v.Pts, PtsCount: v.PtsCount}
}

func (r *Runtime) shortChatMessage(v *tg.UpdateShortChatMessage) tg.UpdateClass {
	m := &tg.Message{
		ID:      v.ID,
		PeerID:  &tg.PeerChat{ChatID: v.ChatID},
		FromID:  &tg.PeerUser{UserID: v.FromID},
		Message: v.Message,
		Date:    v.Date,
		Out:     v.Out,
	}
	m.SetMentioned(v.Mentioned)
	m.SetMediaUnread(v.MediaUnread)
	m.SetSilent(v.Silent)
	if v.ReplyTo != nil {
		m.SetReplyTo(v.ReplyTo)
	}
	m.SetEntities(v.Entities)
	return &tg.UpdateNewMessage{Message: m, Pts: v.Pts, PtsCount: v.PtsCount}
}

func (r *Runtime) shortSentMessage(v *tg.UpdateShortSentMessage) tg.UpdateClass {
	m := &tg.Message{
		ID:     v.ID,
		Out:    true,
		Date:   v.Date,
		PeerID: &tg.PeerUser{},
	}
	return &tg.UpdateNewMessage{Message: m, Pts: v.Pts, PtsCount: v.PtsCount}
}

// learn refreshes the peer name cache from an update batch.
func (r *Runtime) learn(users []tg.UserClass, chats []tg.ChatClass) {
	for _, u := range users {
		if user, ok := u.AsNotEmpty(); ok {
			name := strings.TrimSpace(user.FirstName + " " + user.LastName)
			r.remember(proto.PeerInfo{
				ID: user.ID, Type: "user", Title: name, Username: user.Username,
			})
		}
	}
	for _, c := range chats {
		ch, ok := c.(*tg.Channel)
		if !ok {
			continue
		}
		title := ch.Title
		if ch.Username != "" {
			title += " (@" + ch.Username + ")"
		}
		r.remember(proto.PeerInfo{ID: ch.ID, Type: "channel", Title: title, Username: ch.Username})
	}
}

func (r *Runtime) emit(name string, data any) {
	if r.opts.OnEvent != nil {
		r.opts.OnEvent(name, data)
	}
}

func (r *Runtime) dispatch(ctx context.Context, ups []tg.UpdateClass) {
	for _, u := range ups {
		switch v := u.(type) {
		case *tg.UpdateNewMessage:
			if m, ok := v.Message.(*tg.Message); ok {
				msg := r.toMessage(ctx, m)
				r.emit(proto.EventMessageNew, msg)
			}
		case *tg.UpdateNewChannelMessage:
			if m, ok := v.Message.(*tg.Message); ok {
				msg := r.toMessage(ctx, m)
				r.emit(proto.EventMessageNew, msg)
			}
		case *tg.UpdateEditMessage:
			if m, ok := v.Message.(*tg.Message); ok {
				msg := r.toMessage(ctx, m)
				r.emit(proto.EventMessageEdit, msg)
			}
		case *tg.UpdateEditChannelMessage:
			if m, ok := v.Message.(*tg.Message); ok {
				msg := r.toMessage(ctx, m)
				r.emit(proto.EventMessageEdit, msg)
			}
		case *tg.UpdateDeleteMessages:
			r.emit(proto.EventMessageDelete, map[string]any{
				"peer_id": r.selfID(), "message_ids": v.Messages, "channel": false,
			})
		case *tg.UpdateDeleteChannelMessages:
			r.emit(proto.EventMessageDelete, map[string]any{
				"peer_id": v.ChannelID, "message_ids": v.Messages, "channel": true,
			})
		case *tg.UpdateUserTyping:
			r.emit(proto.EventUserTyping, map[string]any{
				"user_id": v.UserID, "peer_id": v.PeerID, "action": "typing",
			})
		case *tg.UpdateChatUserTyping:
			r.emit(proto.EventUserTyping, map[string]any{
				"user_id": v.FromID, "peer_id": v.ChatID, "action": "typing",
			})
		case *tg.UpdateChannelParticipant:
			r.emit(proto.EventChatAction, map[string]any{
				"channel_id": v.ChannelID, "action": "participant",
			})
		case *tg.UpdateChatTitle:
			r.remember(proto.PeerInfo{ID: v.ChatID, Type: "chat", Title: v.Title})
			r.emit(proto.EventChatAction, map[string]any{
				"peer_id": v.ChatID, "action": "title", "title": v.Title,
			})
		case *tg.UpdateChannel:
			r.remember(proto.PeerInfo{ID: v.ChannelID, Type: "channel", Title: v.Title, Username: v.Username})
		case *tg.UpdateUserName:
			r.remember(proto.PeerInfo{ID: v.UserID, Type: "user", Title: v.FirstName, Username: v.Username})
		case *tg.UpdateUserStatus:
			r.emit(proto.EventChatAction, map[string]any{
				"peer_id": v.UserID, "action": "status", "status": v.Status.String(),
			})
		case *tg.UpdateReadHistoryInbox:
			// Cheap signal that a human is active; plugins like presence
			// trackers live on this.
			r.emit(proto.EventChatAction, map[string]any{
				"peer_id": v.PeerID, "action": "read",
			})
		}
	}
}

func (r *Runtime) selfID() int64 {
	if me := r.Me(); me != nil {
		return me.ID
	}
	return 0
}

// toMessage converts a Telegram message into the canonical wire shape.
//
// Everything here is synchronous and allocation-light: this runs for every
// incoming message on the hot path.
func (r *Runtime) toMessage(_ context.Context, m *tg.Message) proto.Message {
	peerID, peerType := peerOf(m.PeerID)
	fromID, _ := peerOf(m.FromID)

	info, known := r.lookup(peerID)
	if !known {
		info = proto.PeerInfo{ID: peerID, Type: peerType, Title: "id:" + itoa(peerID)}
	}
	fromName := ""
	if fromID != 0 {
		if fi, ok := r.lookup(fromID); ok {
			fromName = fi.Title
		} else {
			fromName = "id:" + itoa(fromID)
		}
	} else {
		fromName = info.Title
	}

	out := proto.Message{
		ID:        m.ID,
		PeerID:    peerID,
		PeerType:  peerType,
		PeerTitle: info.Title,
		FromID:    fromID,
		FromName:  fromName,
		Text:      m.Message,
		Date:      int64(m.Date),
		Out:       m.Out,
		IsPrivate: peerType == "user",
		MentionsMe: r.mentionsMe(m),
		Media:     mediaName(m),
	}
	if out.FromID == 0 {
		out.FromID = peerID
		out.FromName = info.Title
	}
	if rt, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
		out.ReplyTo = rt.ReplyToMsgID
	}
	return out
}

// mentionsMe is a cheap heuristic: Telegram already tells us via the
// Mentioned flag, so no entity parsing is needed.
func (r *Runtime) mentionsMe(m *tg.Message) bool {
	mentioned, ok := m.GetMentioned()
	return ok && mentioned
}

func peerOf(p tg.PeerClass) (int64, string) {
	switch v := p.(type) {
	case *tg.PeerUser:
		return v.UserID, "user"
	case *tg.PeerChat:
		return v.ChatID, "chat"
	case *tg.PeerChannel:
		return v.ChannelID, "channel"
	default:
		return 0, "user"
	}
}

func mediaName(m *tg.Message) string {
	switch v := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		switch doc := v.Document.(type) {
		case *tg.Document:
			for _, attr := range doc.Attributes {
				switch attr.(type) {
				case *tg.DocumentAttributeVideo:
					return "video"
				case *tg.DocumentAttributeAudio:
					return "audio"
				case *tg.DocumentAttributeAnimated:
					return "gif"
				case *tg.DocumentAttributeSticker:
					return "sticker"
				}
			}
		}
		return "file"
	case *tg.MessageMediaGeo:
		return "geo"
	case *tg.MessageMediaContact:
		return "contact"
	case *tg.MessageMediaPoll:
		return "poll"
	case *tg.MessageMediaWebPage:
		return "webpage"
	case *tg.MessageMediaDice:
		return "dice"
	default:
		return ""
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// isFatalRPC reports whether an error should tear the connection down.
func isFatalRPC(err error) bool {
	if err == nil {
		return false
	}
	if tgerr.Is(err, "SESSION_REVOKED", "AUTH_KEY_UNREGISTERED", "USER_DEACTIVATED") {
		return true
	}
	return false
}
