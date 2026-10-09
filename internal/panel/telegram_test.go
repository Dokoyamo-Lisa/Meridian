package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"meridian/internal/proto"
)

const (
	botToken   = "123456789:AAEabcdefghijklmnopqrstuvwxyz0123456"
	badToken   = "987654321:AAErefusedrefusedrefusedrefusedrefus"
	botChat    = "-1001234567890"
	botChatID  = int64(-1001234567890)
	operatorID = int64(42)
)

// fakeTelegram stands in for api.telegram.org.
type fakeTelegram struct {
	mu       sync.Mutex
	srv      *httptest.Server
	updates  [][]tgUpdate
	sent     []url.Values
	edits    []url.Values
	answers  []url.Values
	commands []string
	menus    []string
	deleted  int
	polls    int
	offsets  []string
	nextID   int64
}

func newFakeTelegram(t *testing.T, h *harness) *fakeTelegram {
	f := &fakeTelegram{nextID: 1000}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		parts := strings.Split(r.URL.Path, "/")
		method := parts[len(parts)-1]
		if len(parts) < 3 || parts[1] != "bot"+botToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		ok := func(result string) { fmt.Fprintf(w, `{"ok":true,"result":%s}`, result) }
		switch method {
		case "getMe":
			ok(`{"id":1,"is_bot":true,"first_name":"Meridian","username":"meridian_bot"}`)
		case "setMyCommands":
			f.commands = append(f.commands, form.Get("commands"))
			ok("true")
		case "deleteMyCommands":
			f.deleted++
			ok("true")
		case "setChatMenuButton":
			f.menus = append(f.menus, form.Get("menu_button"))
			ok("true")
		case "getUpdates":
			f.polls++
			f.offsets = append(f.offsets, form.Get("offset"))
			if len(f.updates) == 0 {
				f.mu.Unlock()
				time.Sleep(5 * time.Millisecond) // a long poll with nothing new
				f.mu.Lock()
				ok("[]")
				return
			}
			b, _ := json.Marshal(f.updates[0])
			f.updates = f.updates[1:]
			ok(string(b))
		case "sendMessage":
			f.nextID++
			f.sent = append(f.sent, form)
			ok(fmt.Sprintf(`{"message_id":%d,"date":%d,"chat":{"id":%s}}`, f.nextID, now(), form.Get("chat_id")))
		case "editMessageText":
			f.edits = append(f.edits, form)
			ok("true")
		case "answerCallbackQuery":
			f.answers = append(f.answers, form)
			ok("true")
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"ok":false,"description":"Not Found"}`)
		}
	}))
	old := telegramAPI
	telegramAPI = f.srv.URL
	t.Cleanup(func() {
		telegramAPI = old
		f.srv.Close()
	})
	h.p.notify.client = f.srv.Client()
	return f
}

func (f *fakeTelegram) queue(ups ...tgUpdate) {
	f.mu.Lock()
	f.updates = append(f.updates, ups)
	f.mu.Unlock()
}

// take returns what was sent since the last call.
func (f *fakeTelegram) take() (sent, edits, answers []url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sent, edits, answers = f.sent, f.edits, f.answers
	f.sent, f.edits, f.answers = nil, nil, nil
	return
}

var updateSeq int64 = 100

func tgMsg(chat, from int64, text string) tgUpdate {
	updateSeq++
	return tgUpdate{UpdateID: updateSeq, Message: &tgMessage{MessageID: updateSeq, Date: now(), Chat: tgChat{ID: chat, Type: "supergroup"},
		From: &tgUser{ID: from, FirstName: "Ops", Username: "ops"}, Text: text}}
}

func tgPress(from int64, msgID int64, data string) tgUpdate {
	updateSeq++
	return tgUpdate{UpdateID: updateSeq, Callback: &tgCallback{ID: fmt.Sprint(updateSeq), From: tgUser{ID: from, FirstName: "Ops", Username: "ops"},
		Message: &tgMessage{MessageID: msgID, Chat: tgChat{ID: botChatID}}, Data: data}}
}

// poll lets the bot read what was queued, once.
func poll(t *testing.T, h *harness) {
	t.Helper()
	if err := h.p.pollOnce(context.Background(), h.p.notifyConfig()); err != nil {
		t.Fatal(err)
	}
}

// buttons reads the inline keyboard of a sent message: text -> callback data.
func buttons(t *testing.T, m url.Values) map[string]string {
	t.Helper()
	out := map[string]string{}
	if m.Get("reply_markup") == "" {
		return out
	}
	var kb struct {
		Rows [][]tgButton `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(m.Get("reply_markup")), &kb); err != nil {
		t.Fatal(err)
	}
	for _, row := range kb.Rows {
		for _, b := range row {
			out[b.Text] = b.Data
		}
	}
	return out
}

func botSetup(t *testing.T, extra map[string]any) (*harness, *client, *fakeTelegram) {
	h := newHarness(t)
	f := newFakeTelegram(t, h)
	b := h.browser()
	b.login("owner", "owner-password-1")
	body := map[string]any{"telegram_token": botToken, "telegram_chat": botChat, "telegram_commands": true}
	for k, v := range extra {
		body[k] = v
	}
	b.must("PUT", "/api/settings/notify", body, 200)
	return h, b, f
}

// TestTelegramSettings: the bot's settings are checked, kept with the notifications and shown.
func TestTelegramSettings(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	for body, want := range map[string]string{
		`{"telegram_commands":true}`: "bot token and chat first",
		`{"telegram_report":true}`:   "bot token and chat first",
		`{"telegram_token":"` + botToken + `","telegram_chat":"1","telegram_report_hour":24}`:  "0 to 23",
		`{"telegram_token":"` + botToken + `","telegram_chat":"1","telegram_users":[-5]}`:      "Telegram user ids",
		`{"telegram_token":"` + botToken + `","telegram_chat":"1","telegram_users":[1e17]}`:    "Telegram user ids",
		`{"telegram_token":"` + botToken + `","telegram_chat":"1","groups":["servers","fun"]}`: "health",
	} {
		var in map[string]any
		_ = json.Unmarshal([]byte(body), &in)
		if code, m, _ := b.do("PUT", "/api/settings/notify", in); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), want) {
			t.Errorf("%s: %d %v", body, code, m)
		}
	}
	v := b.must("PUT", "/api/settings/notify", map[string]any{"telegram_token": botToken, "telegram_chat": botChat, "telegram_commands": true,
		"telegram_report": true, "telegram_report_hour": 0, "telegram_users": []int64{42, 42, 7}}, 200)
	if v["telegram_commands"] != true || v["telegram_report"] != true || id(v["telegram_report_hour"]) != 0 ||
		fmt.Sprint(v["telegram_users"]) != "[42 7]" || v["telegram_changes"] != false || v["telegram_bot_status"] != "starting" {
		t.Errorf("view: %v", v)
	}
	// omitted fields keep their value
	v = b.must("PUT", "/api/settings/notify", map[string]any{"telegram_changes": true}, 200)
	if v["telegram_commands"] != true || v["telegram_changes"] != true || fmt.Sprint(v["telegram_users"]) != "[42 7]" {
		t.Errorf("after a partial change: %v", v)
	}
	ro := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, raw := h.bearer(ro).do("GET", "/api/settings/notify", nil); code != 200 || strings.Contains(string(raw), "AAEabcdef") {
		t.Errorf("read token view: %d %s", code, raw)
	}
	if code, _, _ := h.bearer(ro).do("POST", "/api/settings/notify/report", nil); code != 403 {
		t.Errorf("read token sent a report: %d", code)
	}
}

// TestTelegramCommands: the bot answers the configured chat - and allowed users - only, in short
// HTML with every name escaped, and never sends a link or a secret.
func TestTelegramCommands(t *testing.T) {
	h, b, f := botSetup(t, map[string]any{"telegram_users": []int64{operatorID}})
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	var made []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "alice", "quota": 10 << 30})
	_ = json.Unmarshal(raw, &made)
	alice := id(made[0]["id"])
	link := made[0]["link"].(string)
	b.must("POST", "/api/users", map[string]any{"name": "<b>evil</b> & co"}, 201)
	if _, err := h.p.db.Exec1(`UPDATE subs SET cycle_down = ?, expires_at = ? WHERE id = ?`, 9<<30, now()+2*86400, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.db.Exec1(`UPDATE servers SET expires_on = ? WHERE id = ?`, time.Now().AddDate(0, 0, 3).Format("2006-01-02"), sid); err != nil {
		t.Fatal(err)
	}
	srv, _ := h.p.serverByID(context.Background(), sid)
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: "1.0.0", OS: "Debian", IPv4: "203.0.113.10"},
		Live:  &proto.Live{Sys: proto.Sys{CPU: 12, TXRate: 2_000_000, RXRate: 1_000_000, MemTotal: 100, MemUsed: 40}},
		Batch: &proto.Batch{Seq: 1, NIC: proto.NICDelta{RX: 3 << 30, TX: 4 << 30}}}); err != nil {
		t.Fatal(err)
	}
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Active: []string{"miner:xmrig"}, Findings: []proto.Finding{
		{Seq: 1, Key: "miner:xmrig", Kind: "miner", Severity: "critical", Title: "A crypto-miner is running: xmrig", At: now(), Lasting: true}}})

	ask := func(text string) string {
		t.Helper()
		h.p.bot.mu.Lock()
		h.p.bot.replies = nil // more commands than the rate limit allows in a minute
		h.p.bot.mu.Unlock()
		f.queue(tgMsg(botChatID, operatorID, text))
		poll(t, h)
		sent, _, _ := f.take()
		if len(sent) != 1 {
			t.Fatalf("%s: %d replies", text, len(sent))
		}
		m := sent[0]
		if m.Get("chat_id") != botChat || m.Get("parse_mode") != "HTML" {
			t.Errorf("%s: %v", text, m)
		}
		out := m.Get("text")
		for _, secret := range []string{link, "/s/", botToken, "203.0.113.10"} {
			if strings.Contains(out, secret) {
				t.Errorf("%s gave away %q: %s", text, secret, out)
			}
		}
		return out
	}
	for cmd, want := range map[string][]string{
		"/help":                {"/status", "/risks", "/report", "changes from Telegram are off"},
		"/start":               {"/servers"},
		"/status":              {"Servers: 1 of 1 online", "Users: 2", "Needs you", "crypto-miner"},
		"/servers":             {"🟢 Tokyo", "CPU 12%"},
		"/server tok":          {"<b>Tokyo</b>", "CPU 12%", "this month 7.0 GB", "Paid until", "1 open risk(s)"},
		"/server nowhere":      {"No server is called nowhere"},
		"/traffic":             {"Traffic today", "Tokyo - 7.0 GB"},
		"/traffic month":       {"Traffic this month", "Tokyo - 7.0 GB"},
		"/users":               {"alice - 9.0 GB of 10.0 GB", "&lt;b&gt;evil&lt;/b&gt; &amp; co"},
		"/user alice":          {"<b>alice</b> - active", "9.0 GB of 10.0 GB", "Access ends on"},
		"/user zed":            {"No user is called zed"},
		"/online":              {"Nobody is connected"},
		"/top":                 {"Most traffic today", "This month"},
		"/events":              {"Latest events", "crypto-miner"},
		"/risks":               {"Open health risks", "Tokyo", "xmrig", "Decide about them in the panel"},
		"/expiring":            {"Tokyo's paid period ends", "alice's access ends", "alice used 90%"},
		"/report":              {"daily report", "Traffic", "Health", "1 critical"},
		"/pause alice":         {"Changes from Telegram are off"},
		"/status@meridian_bot": {"Servers:"},
		"/nonsense":            {"/help lists"},
	} {
		got := ask(cmd)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %q in\n%s", cmd, w, got)
			}
		}
		if strings.Contains(got, "<b>evil</b>") {
			t.Errorf("%s: a name was not escaped", cmd)
		}
	}
	if strings.Contains(ask("/help"), "/pause") {
		t.Error("/help offers /pause while changes are off")
	}
	// everything else is ignored without a word
	stale := tgMsg(botChatID, operatorID, "/status")
	stale.Message.Date = now() - 3600
	fromBot := tgMsg(botChatID, operatorID, "/status")
	fromBot.Message.From.IsBot = true
	f.queue(tgMsg(-100999, operatorID, "/status"), tgMsg(botChatID, 7, "/status"), tgMsg(botChatID, operatorID, "hello"),
		tgMsg(botChatID, operatorID, "/status@other_bot"), stale, fromBot)
	poll(t, h)
	if sent, _, _ := f.take(); len(sent) != 0 {
		t.Errorf("answered someone it should not: %v", sent)
	}
	// the command list is registered once, without pause and resume
	poll(t, h)
	f.mu.Lock()
	cmds := f.commands
	offsets := f.offsets
	f.mu.Unlock()
	if len(cmds) != 1 || !strings.Contains(cmds[0], `"command":"risks"`) || strings.Contains(cmds[0], "pause") {
		t.Errorf("commands: %v", cmds)
	}
	// each poll goes on after the last update it read; where it got is kept for this bot
	if len(offsets) < 2 || offsets[0] != "" || offsets[len(offsets)-1] != fmt.Sprint(updateSeq+1) {
		t.Errorf("offsets: %v (last update %d)", offsets, updateSeq)
	}
	if v, _ := h.p.getSetting("telegram_offset"); v != fmt.Sprintf("123456789:%d", updateSeq+1) {
		t.Errorf("stored offset %s", v)
	}
	// /events and the rest never name the server's address
	if got := ask("/events"); !strings.Contains(got, "connected for the first time (Debian, …)") {
		t.Errorf("the address in /events: %s", got)
	}
}

// TestTelegramButtons: deciding about risks and pausing users from Telegram works only while changes
// are allowed, only after a confirmation button, and only for who asked.
func TestTelegramButtons(t *testing.T) {
	h, b, f := botSetup(t, nil)
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	b.must("POST", "/api/users", map[string]any{"name": "alice"}, 201)
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Findings: []proto.Finding{
		{Seq: 1, Key: "port:tcp:8080", Kind: "port", Severity: "warning", Title: "A new port is open: 8080/tcp", At: now(), Lasting: true},
		{Seq: 2, Key: "account:bob", Kind: "account", Severity: "high", Title: "A new account was added: bob", At: now()}}})
	risks := byKey(listRisks(t, b, ""))
	port, acct := risks["port:tcp:8080"].ID, risks["account:bob"].ID

	// changes off: no buttons, and a press only says so
	f.queue(tgMsg(botChatID, operatorID, "/risks"))
	poll(t, h)
	sent, _, _ := f.take()
	if len(sent) != 1 || sent[0].Get("reply_markup") != "" {
		t.Fatalf("/risks with changes off: %v", sent)
	}
	f.queue(tgPress(operatorID, 1, fmt.Sprintf("e:%d", port)))
	poll(t, h)
	sent, _, answers := f.take()
	if len(sent) != 0 || len(answers) != 1 || !strings.Contains(answers[0].Get("text"), "Changes from Telegram are off") || answers[0].Get("show_alert") != "true" {
		t.Fatalf("a press with changes off: %v %v", sent, answers)
	}

	// changes on, but nobody listed: "everyone in the chat" may read, never change
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_changes": true}, 200)
	f.queue(tgPress(operatorID, 1, fmt.Sprintf("e:%d", port)))
	poll(t, h)
	sent, _, answers = f.take()
	if len(sent) != 0 || len(answers) != 1 || !strings.Contains(answers[0].Get("text"), "Only the Telegram accounts allowed") {
		t.Fatalf("a change by an unlisted account: %v %v", sent, answers)
	}
	f.queue(tgMsg(botChatID, operatorID, "/pause alice"))
	poll(t, h)
	if sent, _, _ = f.take(); len(sent) != 1 || !strings.Contains(sent[0].Get("text"), "Only the Telegram accounts allowed") {
		t.Fatalf("/pause by an unlisted account: %v", sent)
	}
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_users": []int64{operatorID, 7}}, 200)
	f.queue(tgMsg(botChatID, operatorID, "/risks"))
	poll(t, h)
	sent, _, _ = f.take()
	kb := buttons(t, sent[0])
	if kb["1: Acknowledge"] != fmt.Sprintf("a:%d", acct) || kb["2: Expected…"] != fmt.Sprintf("e:%d", port) {
		t.Fatalf("/risks buttons: %v", kb)
	}
	f.mu.Lock()
	if len(f.commands) != 2 || !strings.Contains(f.commands[1], `"command":"pause"`) {
		t.Errorf("the command list did not follow: %v", f.commands)
	}
	f.mu.Unlock()

	// expected: a question first; "every server" asks once more; only who asked may answer
	f.queue(tgPress(operatorID, 1, fmt.Sprintf("e:%d", port)))
	poll(t, h)
	sent, _, _ = f.take()
	if len(sent) != 1 || !strings.Contains(sent[0].Get("text"), "A new port is open: 8080/tcp") {
		t.Fatalf("the question: %v", sent)
	}
	q := buttons(t, sent[0])
	if q["Yes, on Tokyo"] == "" || q["On every server…"] == "" || q["Cancel"] == "" {
		t.Fatalf("question buttons: %v", q)
	}
	f.queue(tgPress(7, 2, q["On every server…"]))
	poll(t, h)
	_, _, answers = f.take()
	if len(answers) != 1 || !strings.Contains(answers[0].Get("text"), "Only who asked") {
		t.Errorf("someone else answered: %v", answers)
	}
	f.queue(tgPress(operatorID, 2, q["On every server…"]))
	poll(t, h)
	_, edits, _ := f.take()
	if len(edits) != 1 || !strings.Contains(edits[0].Get("text"), "every server") {
		t.Fatalf("the second question: %v", edits)
	}
	if v, _ := h.p.riskByID(context.Background(), port); v.Status != "open" {
		t.Fatal("decided before the confirmation")
	}
	sure := buttons(t, edits[0])["Yes, on every server"]
	f.queue(tgPress(operatorID, 2, sure))
	poll(t, h)
	_, edits, _ = f.take()
	v, _ := h.p.riskByID(context.Background(), port)
	if len(edits) != 1 || !strings.Contains(edits[0].Get("text"), "expected on every server") || v.Status != "expected" || !v.Everywhere ||
		!strings.HasPrefix(v.DecidedBy, "Telegram: Ops (@ops)") {
		t.Fatalf("expected everywhere: %v %+v", edits, v)
	}
	// the same answer twice: it was used up
	f.queue(tgPress(operatorID, 2, sure))
	poll(t, h)
	if _, edits, _ = f.take(); len(edits) != 1 || !strings.Contains(edits[0].Get("text"), "expired") {
		t.Errorf("a used confirmation: %v", edits)
	}

	// acknowledge, after a cancel
	ackAsk := func() string {
		f.queue(tgPress(operatorID, 1, fmt.Sprintf("a:%d", acct)))
		poll(t, h)
		sent, _, _ := f.take()
		return buttons(t, sent[0])["Yes, acknowledge"]
	}
	tok := ackAsk()
	f.queue(tgPress(operatorID, 3, strings.Replace(tok, "c:", "x:", 1)))
	poll(t, h)
	if _, edits, _ = f.take(); len(edits) != 1 || !strings.Contains(edits[0].Get("text"), "Cancelled") {
		t.Errorf("cancel: %v", edits)
	}
	if v, _ := h.p.riskByID(context.Background(), acct); v.Status != "open" {
		t.Error("cancelled, yet changed")
	}
	f.queue(tgPress(operatorID, 4, ackAsk()))
	poll(t, h)
	if v, _ := h.p.riskByID(context.Background(), acct); v.Status != "acknowledged" {
		t.Errorf("acknowledged: %+v", v)
	}
	// an old question expires
	tok = ackAsk()
	h.p.bot.mu.Lock()
	for _, x := range h.p.bot.pending {
		x.expires = time.Now().Add(-time.Minute)
	}
	h.p.bot.mu.Unlock()
	f.queue(tgPress(operatorID, 5, tok))
	poll(t, h)
	if _, edits, _ = f.take(); len(edits) != 1 || !strings.Contains(edits[0].Get("text"), "expired") {
		t.Errorf("an expired question: %v", edits)
	}

	// pause and resume a user, each confirmed
	f.queue(tgMsg(botChatID, operatorID, "/pause ali"))
	poll(t, h)
	sent, _, _ = f.take()
	yes := buttons(t, sent[0])["Yes, pause alice"]
	if yes == "" || !strings.Contains(sent[0].Get("text"), "disconnected") {
		t.Fatalf("pause question: %v", sent)
	}
	var paused bool
	_ = h.p.db.QueryRow(`SELECT paused FROM subs WHERE name = 'alice'`).Scan(&paused)
	if paused {
		t.Fatal("paused before the confirmation")
	}
	f.queue(tgPress(operatorID, 6, yes))
	poll(t, h)
	_, edits, _ = f.take()
	_ = h.p.db.QueryRow(`SELECT paused FROM subs WHERE name = 'alice'`).Scan(&paused)
	if !paused || len(edits) != 1 || !strings.Contains(edits[0].Get("text"), "is paused") {
		t.Fatalf("paused: %v %v", paused, edits)
	}
	var msg string
	_ = h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'user_pause'`).Scan(&msg)
	if msg != "Telegram: Ops (@ops) paused alice" {
		t.Errorf("event: %q", msg)
	}
	f.queue(tgMsg(botChatID, operatorID, "/resume alice"))
	poll(t, h)
	sent, _, _ = f.take()
	f.queue(tgPress(operatorID, 7, buttons(t, sent[0])["Yes, resume alice"]))
	poll(t, h)
	_ = h.p.db.QueryRow(`SELECT paused FROM subs WHERE name = 'alice'`).Scan(&paused)
	if paused {
		t.Error("not resumed")
	}
}

// TestTelegramRiskNotifications: with changes allowed, a high risk arrives as a message of its own
// with its buttons; without, in the usual plain message.
func TestTelegramRiskNotifications(t *testing.T) {
	h, b, f := botSetup(t, map[string]any{"telegram_changes": true, "groups": []string{"servers", "health"}})
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	ctx := context.Background()
	h.p.notifyTick(ctx) // starts from now
	h.p.event(0, "crit", "server_offline", sid, 0, 0, "Osaka stopped reporting", nil)
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Findings: []proto.Finding{
		{Seq: 1, Key: "account:bob", Kind: "account", Severity: "high", Title: "A new account was added: bob", Detail: "uid 1001 <script>", At: now()},
		{Seq: 2, Key: "port:tcp:8080", Kind: "port", Severity: "warning", Title: "A new port is open: 8080/tcp", At: now(), Lasting: true}}})
	h.p.notifyTick(ctx)
	sent, _, _ := f.take()
	if len(sent) != 2 {
		t.Fatalf("sent %d: %v", len(sent), sent)
	}
	if sent[0].Get("parse_mode") != "" || !strings.Contains(sent[0].Get("text"), "Osaka stopped reporting") || strings.Contains(sent[0].Get("text"), "bob") {
		t.Errorf("the other events: %v", sent[0])
	}
	acct := byKey(listRisks(t, b, ""))["account:bob"].ID
	kb := buttons(t, sent[1])
	if kb["Acknowledge"] != fmt.Sprintf("a:%d", acct) || kb["Expected…"] != fmt.Sprintf("e:%d", acct) ||
		!strings.Contains(sent[1].Get("text"), "Tokyo: A new account was added: bob") || !strings.Contains(sent[1].Get("text"), "&lt;script&gt;") ||
		strings.Contains(sent[1].Get("text"), "8080") {
		t.Errorf("the risk: %v", sent[1])
	}
	// changes off: one plain message
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_changes": false}, 200)
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Findings: []proto.Finding{
		{Seq: 3, Key: "miner:xmrig", Kind: "miner", Severity: "critical", Title: "A crypto-miner is running: xmrig", At: now(), Lasting: true}}})
	h.p.notifyTick(ctx)
	sent, _, _ = f.take()
	if len(sent) != 1 || sent[0].Get("reply_markup") != "" || !strings.Contains(sent[0].Get("text"), "xmrig") {
		t.Errorf("with changes off: %v", sent)
	}
}

// TestTelegramRateLimit: at most 20 replies a minute; the first one over says so once.
func TestTelegramRateLimit(t *testing.T) {
	h, _, f := botSetup(t, nil)
	var ups []tgUpdate
	for i := 0; i < 25; i++ {
		ups = append(ups, tgMsg(botChatID, operatorID, "/help"))
	}
	f.queue(ups...)
	poll(t, h)
	sent, _, _ := f.take()
	if len(sent) != 21 || !strings.Contains(sent[20].Get("text"), "Too many commands") {
		t.Fatalf("sent %d", len(sent))
	}
}

// TestTelegramDailyReport: the report goes out once a day from its hour; turned on after the hour, it
// starts the next day; it can be sent now from the panel.
func TestTelegramDailyReport(t *testing.T) {
	h, b, f := botSetup(t, nil)
	b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)
	ctx := context.Background()
	c := h.p.notifyConfig()
	c.Report, c.ReportHour = true, new(int) // midnight: due at once
	if err := h.p.saveNotifyConfig(c); err != nil {
		t.Fatal(err)
	}
	if err := h.p.reportTick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.p.reportTick(ctx); err != nil {
		t.Fatal(err)
	}
	sent, _, _ := f.take()
	if len(sent) != 1 || !strings.Contains(sent[0].Get("text"), "daily report") || !strings.Contains(sent[0].Get("text"), "no open risks") {
		t.Fatalf("report: %v", sent)
	}
	if v := b.must("GET", "/api/settings/notify", nil, 200); id(v["telegram_report_sent"]) == 0 {
		t.Errorf("the view does not say when: %v", v)
	}
	// turned on again after its hour: tomorrow
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_report": false}, 200)
	h.p.setSetting("telegram_report_day", "")
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_report": true, "telegram_report_hour": 0}, 200)
	if err := h.p.reportTick(ctx); err != nil {
		t.Fatal(err)
	}
	if sent, _, _ := f.take(); len(sent) != 0 {
		t.Errorf("a report right after turning it on: %v", sent)
	}
	if r := b.must("POST", "/api/settings/notify/report", nil, 200); r["telegram"] != "ok" {
		t.Errorf("send now: %v", r)
	}
	if sent, _, _ := f.take(); len(sent) != 1 {
		t.Errorf("send now sent %d", len(sent))
	}
}

// TestTelegramPoller: the poller answers, and stops as soon as the token is removed.
func TestTelegramPoller(t *testing.T) {
	h, b, f := botSetup(t, nil)
	f.queue(tgMsg(botChatID, operatorID, "/help"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.p.telegramBot(ctx)
		close(done)
	}()
	wait := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 400 && !ok(); i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if !ok() {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	wait("a reply", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) == 1 })
	wait("listening", func() bool {
		return b.must("GET", "/api/settings/notify", nil, 200)["telegram_bot_status"] == "listening"
	})
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_token": "", "telegram_chat": ""}, 200)
	wait("off", func() bool { return b.must("GET", "/api/settings/notify", nil, 200)["telegram_bot_status"] == "off" })
	f.mu.Lock()
	polls := f.polls
	f.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	if f.polls != polls {
		t.Errorf("still polling: %d -> %d", polls, f.polls)
	}
	f.mu.Unlock()
	cancel()
	<-done

	// a token Telegram refuses: the reason, in plain words
	h.p.bot = botState{}
	c := h.p.notifyConfig()
	c.TelegramToken, c.TelegramChat, c.Commands = badToken, botChat, true
	if err := h.p.pollOnce(context.Background(), c); err == nil || !strings.Contains(err.Error(), "refused by Telegram") || strings.Contains(err.Error(), badToken) {
		t.Errorf("refused token: %v", err)
	}
}

// TestTelegramChangesNeedTheBrowser: API tokens may turn changes from Telegram off, never on, and
// never change who may use the bot; another chat or bot does not inherit the right to change things;
// removing Telegram stops the bot.
func TestTelegramChangesNeedTheBrowser(t *testing.T) {
	h, b, _ := botSetup(t, map[string]any{"telegram_report": true})
	full := b.must("POST", "/api/tokens", map[string]any{"name": "assistant", "scope": "full"}, 201)["token"].(string)
	api := h.bearer(full)
	for _, body := range []map[string]any{{"telegram_changes": true}, {"telegram_users": []int64{7}}} {
		if code, m, _ := api.do("PUT", "/api/settings/notify", body); code != 403 || !strings.Contains(fmt.Sprint(m["error"]), "signed in with your password") {
			t.Errorf("a token set %v: %d %v", body, code, m)
		}
	}
	// the browser allows it; the token may take it back
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_changes": true, "telegram_users": []int64{operatorID}}, 200)
	if v := api.must("PUT", "/api/settings/notify", map[string]any{"telegram_changes": false, "telegram_users": []int64{operatorID}}, 200); v["telegram_changes"] != false {
		t.Errorf("turned off by a token: %v", v)
	}
	// another chat: changes go off until they are allowed again by hand
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_changes": true}, 200)
	if v := api.must("PUT", "/api/settings/notify", map[string]any{"telegram_chat": "-1009999999999"}, 200); v["telegram_changes"] != false || v["telegram_commands"] != true {
		t.Errorf("a new chat kept changes on: %v", v)
	}
	if v := b.must("PUT", "/api/settings/notify", map[string]any{"telegram_chat": botChat, "telegram_changes": true}, 200); v["telegram_changes"] != true {
		t.Errorf("a new chat with changes allowed in the browser: %v", v)
	}
	// Telegram removed: the bot stops for good
	v := b.must("PUT", "/api/settings/notify", map[string]any{"telegram_token": "", "telegram_chat": ""}, 200)
	if v["telegram_commands"] != false || v["telegram_report"] != false || v["telegram_changes"] != false || v["telegram_bot_status"] != "off" {
		t.Errorf("after removing Telegram: %v", v)
	}
}

// TestTelegramChatsWhileListening: "Find chats" asks the running bot instead of reading its updates
// (which would cut the bot's own reading off); a new bot does not start from another bot's place.
func TestTelegramChatsWhileListening(t *testing.T) {
	h, b, f := botSetup(t, nil)
	m := tgMsg(-100777, 9, "hello")
	m.Message.Chat.Title = "Ops <team>"
	f.queue(m, tgMsg(botChatID, operatorID, "/help"))
	poll(t, h)
	f.mu.Lock()
	polls := f.polls
	f.mu.Unlock()
	_, _, raw := b.do("POST", "/api/settings/notify/telegram-chats", map[string]any{})
	var chats []telegramChat
	_ = json.Unmarshal(raw, &chats)
	if len(chats) != 2 || chats[0].ID != botChat || chats[1].ID != "-100777" || chats[1].Title != "Ops <team>" {
		t.Errorf("chats: %s", raw)
	}
	f.mu.Lock()
	if f.polls != polls {
		t.Error("Find chats read the bot's updates while the bot runs")
	}
	f.mu.Unlock()
	// the offset belongs to the bot: another bot starts from its own beginning
	h.p.setSetting("telegram_offset", "555:900")
	h.p.bot = botState{}
	f.queue(tgMsg(botChatID, operatorID, "/help"))
	poll(t, h)
	f.mu.Lock()
	last := f.offsets[len(f.offsets)-1]
	f.mu.Unlock()
	if last != "" {
		t.Errorf("another bot's offset was used: %q", last)
	}
}

func TestHideAddrs(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)
	b.must("POST", "/api/servers", map[string]any{"name": "Osaka", "address": "Osaka.Example.com"}, 201)
	if _, err := h.p.db.Exec1(`UPDATE servers SET ipv6 = '2001:db8::10' WHERE name = 'Tokyo'`); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"Tokyo connected (Debian, 203.0.113.10)":         "Tokyo connected (Debian, …)",
		"listening on 203.0.113.10:443.":                 "listening on …:443.",
		"on [2001:db8::10]:8443 and 2001:DB8::10":        "on […]:8443 and …",
		"osaka.example.com is up":                        "… is up",
		"root signed in from 203.0.113.5, 1203.0.113.10": "root signed in from 203.0.113.5, 1203.0.113.10",
		"a pool at 203.0.113.100:3333":                   "a pool at 203.0.113.100:3333",
	} {
		if got := h.p.hideAddrs(context.Background(), in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
