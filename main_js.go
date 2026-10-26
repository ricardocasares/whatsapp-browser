//go:build js && wasm

// Browser-only WhatsApp client: whatsmeow compiled to WebAssembly.
// The Go side talks to JavaScript through two globals:
//
//	waCommand(jsonString)  JS -> Go  (defined here)
//	waEmit(jsonString)     Go -> JS  (defined by the worker)
//	waPersist(jsonString)  Go -> JS  (defined by the worker, stores state)
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"syscall/js"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"browser/memstore"
)

type command struct {
	Type   string   `json:"type"`
	ReqID  string   `json:"reqId"`
	State  string   `json:"state"`
	JID    string   `json:"jid"`
	Text   string   `json:"text"`
	IDs    []string `json:"ids"`
	Sender string   `json:"sender"`
	Debug  bool     `json:"debug"`
}

type uiMessage struct {
	ID         string `json:"id"`
	Chat       string `json:"chat"`
	ChatName   string `json:"chatName"`
	IsGroup    bool   `json:"isGroup"`
	Sender     string `json:"sender"`
	SenderName string `json:"senderName"`
	FromMe     bool   `json:"fromMe"`
	Text       string `json:"text"`
	Kind       string `json:"kind"`
	TS         int64  `json:"ts"`
	Unread     uint32 `json:"unread,omitempty"`
}

type app struct {
	mu           sync.Mutex
	store        *memstore.Store
	client       *whatsmeow.Client
	log          waLog.Logger
	groups       map[string]string
	persistTimer *time.Timer
	qrRunning    bool
}

var a *app

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Println("emit marshal error:", err)
		return
	}
	js.Global().Call("waEmit", string(b))
}

func emitStatus(status string, extra map[string]any) {
	m := map[string]any{"type": "status", "status": status}
	for k, v := range extra {
		m[k] = v
	}
	emit(m)
}

func (a *app) schedulePersist() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.persistTimer != nil {
		a.persistTimer.Stop()
	}
	a.persistTimer = time.AfterFunc(400*time.Millisecond, a.persistNow)
}

func (a *app) persistNow() {
	data, err := a.store.Export()
	if err != nil {
		fmt.Println("persist export error:", err)
		return
	}
	js.Global().Call("waPersist", string(data))
}

// ---- JID helpers ----

func (a *app) toPN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server == types.HiddenUserServer {
		if pn, _ := a.store.GetPNForLID(ctx, jid); !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid.ToNonAD()
}

func (a *app) chatKey(ctx context.Context, src types.MessageSource) types.JID {
	chat := src.Chat
	if chat.Server == types.HiddenUserServer {
		if src.IsFromMe && src.RecipientAlt.Server == types.DefaultUserServer {
			return src.RecipientAlt.ToNonAD()
		}
		if !src.IsFromMe && src.SenderAlt.Server == types.DefaultUserServer {
			return src.SenderAlt.ToNonAD()
		}
	}
	return a.toPN(ctx, chat)
}

func (a *app) userName(ctx context.Context, jid types.JID) string {
	pn := a.toPN(ctx, jid)
	if c, _ := a.store.GetContact(ctx, pn); c.Found {
		if c.FullName != "" {
			return c.FullName
		}
		if c.PushName != "" {
			return c.PushName
		}
		if c.BusinessName != "" {
			return c.BusinessName
		}
	}
	if pn != jid.ToNonAD() {
		if c, _ := a.store.GetContact(ctx, jid.ToNonAD()); c.Found {
			if c.FullName != "" {
				return c.FullName
			}
			if c.PushName != "" {
				return c.PushName
			}
		}
	}
	if pn.Server == types.DefaultUserServer {
		return "+" + pn.User
	}
	return jid.User
}

func (a *app) chatName(ctx context.Context, chat types.JID) string {
	if chat.Server == types.GroupServer {
		a.mu.Lock()
		n, ok := a.groups[chat.String()]
		a.mu.Unlock()
		if ok && n != "" {
			return n
		}
		return "Group " + chat.User
	}
	if chat.Server == types.BroadcastServer {
		if chat.User == "status" {
			return "Status updates"
		}
		return "Broadcast"
	}
	if chat.Server == types.NewsletterServer {
		return "Channel " + chat.User
	}
	return a.userName(ctx, chat)
}

// ---- message conversion ----

func messageText(m *waE2E.Message) (text, kind string, ok bool) {
	if m == nil {
		return "", "", false
	}
	switch {
	case m.GetConversation() != "":
		return m.GetConversation(), "text", true
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText(), "text", true
	case m.GetImageMessage() != nil:
		return strings.TrimSpace("📷 Photo " + m.GetImageMessage().GetCaption()), "image", true
	case m.GetVideoMessage() != nil:
		return strings.TrimSpace("🎥 Video " + m.GetVideoMessage().GetCaption()), "video", true
	case m.GetPtvMessage() != nil:
		return "🎥 Video message", "video", true
	case m.GetAudioMessage() != nil:
		if m.GetAudioMessage().GetPTT() {
			return "🎤 Voice message", "audio", true
		}
		return "🎵 Audio", "audio", true
	case m.GetDocumentMessage() != nil:
		return "📄 " + m.GetDocumentMessage().GetFileName(), "document", true
	case m.GetStickerMessage() != nil:
		return "🩷 Sticker", "sticker", true
	case m.GetContactMessage() != nil:
		return "👤 Contact: " + m.GetContactMessage().GetDisplayName(), "contact", true
	case m.GetContactsArrayMessage() != nil:
		return "👤 Contacts", "contact", true
	case m.GetLocationMessage() != nil:
		return "📍 Location", "location", true
	case m.GetLiveLocationMessage() != nil:
		return "📍 Live location", "location", true
	case m.GetPollCreationMessage() != nil:
		return "📊 Poll: " + m.GetPollCreationMessage().GetName(), "poll", true
	case m.GetPollCreationMessageV2() != nil:
		return "📊 Poll: " + m.GetPollCreationMessageV2().GetName(), "poll", true
	case m.GetPollCreationMessageV3() != nil:
		return "📊 Poll: " + m.GetPollCreationMessageV3().GetName(), "poll", true
	case m.GetEventMessage() != nil:
		return "📅 Event: " + m.GetEventMessage().GetName(), "event", true
	case m.GetGroupInviteMessage() != nil:
		return "👥 Group invite: " + m.GetGroupInviteMessage().GetGroupName(), "invite", true
	case m.GetReactionMessage() != nil:
		return "", "reaction", false
	case m.GetProtocolMessage() != nil:
		pm := m.GetProtocolMessage()
		if pm.GetType() == waE2E.ProtocolMessage_REVOKE {
			return "🚫 This message was deleted", "revoke", true
		}
		return "", "protocol", false
	case m.GetSenderKeyDistributionMessage() != nil:
		return "", "skdm", false
	}
	return "", "unsupported", false
}

func (a *app) convert(ctx context.Context, evt *events.Message) (*uiMessage, bool) {
	text, kind, ok := messageText(evt.Message)
	if !ok {
		return nil, false
	}
	chat := a.chatKey(ctx, evt.Info.MessageSource)
	msg := &uiMessage{
		ID:       evt.Info.ID,
		Chat:     chat.String(),
		ChatName: a.chatName(ctx, chat),
		IsGroup:  evt.Info.IsGroup,
		Sender:   a.toPN(ctx, evt.Info.Sender).String(),
		FromMe:   evt.Info.IsFromMe,
		Text:     text,
		Kind:     kind,
		TS:       evt.Info.Timestamp.Unix(),
	}
	if kind == "revoke" {
		if key := evt.Message.GetProtocolMessage().GetKey(); key != nil {
			msg.ID = key.GetID()
		}
	}
	if evt.Info.IsFromMe {
		msg.SenderName = "You"
	} else {
		msg.SenderName = a.userName(ctx, evt.Info.Sender)
		if msg.SenderName == "+"+a.toPN(ctx, evt.Info.Sender).User && evt.Info.PushName != "" {
			msg.SenderName = evt.Info.PushName
		}
	}
	return msg, true
}

// ---- event handler ----

func (a *app) handleEvent(rawEvt any) {
	ctx := context.Background()
	switch evt := rawEvt.(type) {
	case *events.PairSuccess:
		emitStatus("paired", map[string]any{"jid": evt.ID.String()})
		a.persistNow()
	case *events.Connected:
		a.onConnected(ctx)
	case *events.PushNameSetting:
		a.persistNow()
	case *events.OfflineSyncCompleted:
		emit(map[string]any{"type": "sync_complete"})
		go a.emitContacts(ctx)
	case *events.AppStateSyncComplete:
		go a.emitContacts(ctx)
	case *events.Disconnected:
		emitStatus("disconnected", nil)
	case *events.StreamReplaced:
		emitStatus("disconnected", map[string]any{"reason": "stream replaced (opened elsewhere?)"})
	case *events.ConnectFailure:
		emitStatus("disconnected", map[string]any{"reason": fmt.Sprintf("connect failure: %s %s", evt.Reason, evt.Message)})
	case *events.ClientOutdated:
		emitStatus("disconnected", map[string]any{"reason": "client outdated, update whatsmeow"})
	case *events.TemporaryBan:
		emitStatus("disconnected", map[string]any{"reason": "temporary ban: " + evt.String()})
	case *events.LoggedOut:
		emitStatus("logged_out", map[string]any{"reason": fmt.Sprintf("logged out (%s)", evt.Reason)})
		a.store.DeleteDevice(ctx, nil)
		a.persistNow()
		go a.restart(ctx)
	case *events.Message:
		if m, ok := a.convert(ctx, evt); ok {
			emit(map[string]any{"type": "message", "message": m})
		}
	case *events.Receipt:
		ids := make([]string, len(evt.MessageIDs))
		copy(ids, evt.MessageIDs)
		emit(map[string]any{"type": "receipt", "chat": a.chatKey(ctx, evt.MessageSource).String(), "ids": ids, "kind": string(evt.Type)})
	case *events.HistorySync:
		a.handleHistorySync(ctx, evt.Data)
	case *events.PushName:
		emit(map[string]any{"type": "chat", "jid": a.toPN(ctx, evt.JID).String(), "name": a.userName(ctx, evt.JID)})
	case *events.Contact:
		emit(map[string]any{"type": "chat", "jid": a.toPN(ctx, evt.JID).String(), "name": a.userName(ctx, evt.JID)})
	case *events.ChatPresence:
		emit(map[string]any{"type": "presence", "chat": a.chatKey(ctx, evt.MessageSource).String(), "state": string(evt.State)})
	}
}

func (a *app) onConnected(ctx context.Context) {
	dev := a.client.Store
	emitStatus("connected", map[string]any{"jid": dev.GetJID().String(), "pushName": dev.PushName})
	a.persistNow()
	go func() {
		groups, err := a.client.GetJoinedGroups(ctx)
		if err != nil {
			fmt.Println("GetJoinedGroups error:", err)
			return
		}
		list := make([]map[string]any, 0, len(groups))
		a.mu.Lock()
		for _, g := range groups {
			a.groups[g.JID.String()] = g.Name
			list = append(list, map[string]any{"jid": g.JID.String(), "name": g.Name, "isGroup": true})
		}
		a.mu.Unlock()
		emit(map[string]any{"type": "chats", "chats": list})
	}()
	go a.emitContacts(ctx)
}

func (a *app) emitContacts(ctx context.Context) {
	contacts, err := a.store.GetAllContacts(ctx)
	if err != nil {
		return
	}
	list := make([]map[string]any, 0, len(contacts))
	for jid, c := range contacts {
		if jid.Server != types.DefaultUserServer {
			continue
		}
		name := c.FullName
		if name == "" {
			name = c.PushName
		}
		if name == "" {
			name = c.BusinessName
		}
		if name == "" {
			continue
		}
		list = append(list, map[string]any{"jid": jid.String(), "name": name})
	}
	emit(map[string]any{"type": "contacts", "contacts": list})
}

func (a *app) handleHistorySync(ctx context.Context, data *waHistorySync.HistorySync) {
	if data == nil {
		return
	}
	for _, pn := range data.GetPushnames() {
		if jid, err := types.ParseJID(pn.GetID()); err == nil && pn.GetPushname() != "" {
			a.store.PutPushName(ctx, jid, pn.GetPushname())
		}
	}
	var msgs []*uiMessage
	var chats []map[string]any
	for _, conv := range data.GetConversations() {
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		key := a.toPN(ctx, chatJID)
		name := conv.GetName()
		if name == "" {
			name = conv.GetDisplayName()
		}
		if name != "" && chatJID.Server == types.GroupServer {
			a.mu.Lock()
			a.groups[chatJID.String()] = name
			a.mu.Unlock()
		}
		if name == "" {
			name = a.chatName(ctx, key)
		}
		chats = append(chats, map[string]any{
			"jid": key.String(), "name": name, "isGroup": chatJID.Server == types.GroupServer,
			"unread": conv.GetUnreadCount(), "archived": conv.GetArchived(), "lastTs": conv.GetLastMsgTimestamp(),
		})
		for _, hm := range conv.GetMessages() {
			evt, err := a.client.ParseWebMessage(chatJID, hm.GetMessage())
			if err != nil {
				continue
			}
			if m, ok := a.convert(ctx, evt); ok {
				m.Chat = key.String()
				m.ChatName = name
				msgs = append(msgs, m)
			}
		}
	}
	emit(map[string]any{"type": "history", "syncType": data.GetSyncType().String(), "chats": chats, "messages": msgs})
	a.schedulePersist()
}

// ---- lifecycle ----

func (a *app) newClient() {
	dev := a.store.Device()
	a.client = whatsmeow.NewClient(dev, a.log)
	a.client.AddEventHandler(a.handleEvent)
}

func (a *app) start(ctx context.Context) {
	a.newClient()
	if a.client.Store.ID == nil {
		a.runQR(ctx)
		return
	}
	emitStatus("connecting", map[string]any{"jid": a.client.Store.ID.String(), "pushName": a.client.Store.PushName})
	if err := a.client.Connect(); err != nil {
		emitStatus("disconnected", map[string]any{"reason": "connect: " + err.Error()})
	}
}

func (a *app) runQR(ctx context.Context) {
	a.mu.Lock()
	if a.qrRunning {
		a.mu.Unlock()
		return
	}
	a.qrRunning = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.qrRunning = false
		a.mu.Unlock()
	}()

	qrChan, err := a.client.GetQRChannel(ctx)
	if err != nil {
		emitStatus("disconnected", map[string]any{"reason": "qr channel: " + err.Error()})
		return
	}
	emitStatus("qr", nil)
	if err := a.client.Connect(); err != nil {
		emitStatus("disconnected", map[string]any{"reason": "connect: " + err.Error()})
		return
	}
	for item := range qrChan {
		switch item.Event {
		case "code":
			png, err := qrcode.Encode(item.Code, qrcode.Medium, 320)
			if err != nil {
				fmt.Println("qr encode error:", err)
				continue
			}
			emit(map[string]any{
				"type":    "qr",
				"code":    item.Code,
				"png":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
				"timeout": item.Timeout.Milliseconds(),
			})
		case "success":
			emitStatus("paired", nil)
			return
		case "timeout":
			emit(map[string]any{"type": "qr_timeout"})
			// Client is disconnected now; start a fresh QR session.
			go func() {
				time.Sleep(500 * time.Millisecond)
				a.runQR(ctx)
			}()
			return
		default:
			emitStatus("disconnected", map[string]any{"reason": "qr: " + item.Event + " " + fmt.Sprint(item.Error)})
			return
		}
	}
}

func (a *app) restart(ctx context.Context) {
	if a.client != nil {
		a.client.Disconnect()
	}
	a.mu.Lock()
	a.groups = map[string]string{}
	a.mu.Unlock()
	a.start(ctx)
}

func (a *app) logout(ctx context.Context) {
	if a.client != nil && a.client.IsLoggedIn() {
		if err := a.client.Logout(ctx); err != nil {
			fmt.Println("logout error:", err)
			a.client.Disconnect()
			a.store.DeleteDevice(ctx, nil)
		}
	} else {
		if a.client != nil {
			a.client.Disconnect()
		}
		a.store.DeleteDevice(ctx, nil)
	}
	a.persistNow()
	emitStatus("logged_out", nil)
	a.restart(ctx)
}

func (a *app) send(ctx context.Context, cmd command) {
	resp := map[string]any{"type": "sent", "reqId": cmd.ReqID}
	jid, err := types.ParseJID(cmd.JID)
	if err != nil {
		resp["error"] = "invalid JID: " + err.Error()
		emit(resp)
		return
	}
	if a.client == nil || !a.client.IsLoggedIn() {
		resp["error"] = "not logged in"
		emit(resp)
		return
	}
	msg := &waE2E.Message{Conversation: proto.String(cmd.Text)}
	r, err := a.client.SendMessage(ctx, jid, msg)
	if err != nil {
		resp["error"] = err.Error()
		emit(resp)
		return
	}
	resp["ok"] = true
	resp["id"] = r.ID
	resp["ts"] = r.Timestamp.Unix()
	chat := a.toPN(ctx, jid)
	resp["message"] = uiMessage{
		ID: r.ID, Chat: chat.String(), ChatName: a.chatName(ctx, chat), IsGroup: jid.Server == types.GroupServer,
		Sender: a.client.Store.GetJID().ToNonAD().String(), SenderName: "You", FromMe: true,
		Text: cmd.Text, Kind: "text", TS: r.Timestamp.Unix(),
	}
	emit(resp)
	a.schedulePersist()
}

func (a *app) markRead(ctx context.Context, cmd command) {
	if a.client == nil || !a.client.IsLoggedIn() || len(cmd.IDs) == 0 {
		return
	}
	jid, err := types.ParseJID(cmd.JID)
	if err != nil {
		return
	}
	sender := jid
	if cmd.Sender != "" {
		if sj, err := types.ParseJID(cmd.Sender); err == nil {
			sender = sj
		}
	}
	if err := a.client.MarkRead(ctx, cmd.IDs, time.Now(), jid, sender); err != nil {
		fmt.Println("mark read error:", err)
	}
}

func handleCommand(raw string) {
	var cmd command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		emit(map[string]any{"type": "error", "error": "bad command: " + err.Error()})
		return
	}
	ctx := context.Background()
	switch cmd.Type {
	case "start":
		level := "INFO"
		if cmd.Debug {
			level = "DEBUG"
		}
		a.log = waLog.Stdout("WA", level, false)
		if cmd.State != "" {
			if err := a.store.Import([]byte(cmd.State)); err != nil {
				emit(map[string]any{"type": "error", "error": "failed to load saved state, starting fresh: " + err.Error()})
			}
		}
		a.store.OnChange = a.schedulePersist
		a.start(ctx)
	case "send":
		a.send(ctx, cmd)
	case "logout":
		a.logout(ctx)
	case "reconnect":
		if a.client != nil && !a.client.IsConnected() {
			a.restart(ctx)
		}
	case "markRead":
		a.markRead(ctx, cmd)
	case "typing":
		if a.client != nil && a.client.IsLoggedIn() {
			if jid, err := types.ParseJID(cmd.JID); err == nil {
				state := types.ChatPresenceComposing
				if cmd.Text == "paused" {
					state = types.ChatPresencePaused
				}
				a.client.SendChatPresence(ctx, jid, state, types.ChatPresenceMediaText)
			}
		}
	default:
		emit(map[string]any{"type": "error", "error": "unknown command " + cmd.Type})
	}
}

func main() {
	store.DeviceProps.Os = proto.String("whatsmeow browser")
	a = &app{
		store:  memstore.New(waLog.Stdout("Store", "WARN", false)),
		groups: map[string]string{},
		log:    waLog.Stdout("WA", "INFO", false),
	}
	js.Global().Set("waCommand", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			return nil
		}
		raw := args[0].String()
		go handleCommand(raw)
		return nil
	}))
	emit(map[string]any{"type": "ready"})
	select {}
}
