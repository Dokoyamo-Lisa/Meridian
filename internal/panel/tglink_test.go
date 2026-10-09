package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// initData makes a Mini App's launch data as Telegram signs it.
func initData(token string, u tgUser, at int64) string {
	ub, _ := json.Marshal(u)
	vals := url.Values{"auth_date": {strconv.FormatInt(at, 10)}, "query_id": {"AAHdF6IQAAAAAN0XohDhrOrc"}, "user": {string(ub)},
		"signature": {"sig-not-checked-here"}}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k + "=" + vals.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(b.String()))
	vals.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return vals.Encode()
}

func TestTgInitData(t *testing.T) {
	u := tgUser{ID: 501, FirstName: "Ann", Username: "ann"}
	good := initData(botToken, u, now())
	if got, err := tgInitUser(botToken, good, 3600); err != nil || got.ID != 501 || got.Username != "ann" {
		t.Fatalf("good data: %+v %v", got, err)
	}
	for name, raw := range map[string]string{
		"another bot's":   initData(badToken, u, now()),
		"an hour old":     initData(botToken, u, now()-3700),
		"from the future": initData(botToken, u, now()+3600),
		"changed":         strings.Replace(good, "501", "502", 1),
		"a field twice":   good + "&auth_date=1",
		"no hash":         "auth_date=1&user=%7B%22id%22%3A1%7D",
		"a bot":           initData(botToken, tgUser{ID: 9, IsBot: true}, now()),
		"nobody":          initData(botToken, tgUser{}, now()),
		"empty":           "",
	} {
		if _, err := tgInitUser(botToken, raw, 3600); err == nil {
			t.Errorf("%s data was accepted", name)
		}
	}
}

func tgPrivate(from int64, text string) tgUpdate {
	updateSeq++
	return tgUpdate{UpdateID: updateSeq, Message: &tgMessage{MessageID: updateSeq, Date: now(), Chat: tgChat{ID: from, Type: "private"},
		From: &tgUser{ID: from, FirstName: fmt.Sprint("TG", from)}, Text: text}}
}

func tgPrivatePress(from, msgID int64, data string) tgUpdate {
	updateSeq++
	return tgUpdate{UpdateID: updateSeq, Callback: &tgCallback{ID: fmt.Sprint(updateSeq), From: tgUser{ID: from, FirstName: fmt.Sprint("TG", from)},
		Message: &tgMessage{MessageID: msgID, Chat: tgChat{ID: from, Type: "private"}}, Data: data}}
}

// say sends a private message to the bot and returns its one reply.
func say(t *testing.T, h *harness, f *fakeTelegram, from int64, text string) url.Values {
	t.Helper()
	f.queue(tgPrivate(from, text))
	poll(t, h)
	sent, _, _ := f.take()
	if len(sent) != 1 {
		t.Fatalf("%q: %d replies", text, len(sent))
	}
	if sent[0].Get("chat_id") != strconv.FormatInt(from, 10) {
		t.Errorf("%q answered in chat %s", text, sent[0].Get("chat_id"))
	}
	return sent[0]
}

// TestTelegramUserLinks: users link up to two Telegram accounts with codes from their page, check
// their usage in a private chat, and unlink one a month; strangers get only how to link theirs.
func TestTelegramUserLinks(t *testing.T) {
	h, b, f := botSetup(t, map[string]any{"telegram_user_link": true})
	ctx := context.Background()
	for _, u := range []string{"alice", "bobby"} {
		b.must("POST", "/api/users", map[string]any{"name": u, "username": u, "password": u + "-password-1", "quota": 10 << 30}, 201)
	}
	h.p.db.Exec1(`UPDATE subs SET cycle_down = ? WHERE login = 'alice'`, 3<<30)
	alice, bobby := h.browser(), h.browser()
	alice.login("alice", "alice-password-1")
	bobby.login("bobby", "bobby-password-1")

	// a stranger: how to link
	if m := say(t, h, f, 501, "/usage"); !strings.Contains(m.Get("text"), "/link CODE") {
		t.Errorf("a stranger's answer: %v", m)
	}
	// a code links; the bot then answers about the user's own data only
	code := alice.must("POST", "/api/portal/telegram/code", nil, 200)["code"].(string)
	if m := say(t, h, f, 501, "/start "+code); !strings.Contains(m.Get("text"), "Linked") {
		t.Fatalf("linking: %v", m)
	}
	if m := say(t, h, f, 501, "/start "+code); strings.Contains(m.Get("text"), "Linked!") {
		t.Error("a code worked twice")
	}
	u := say(t, h, f, 501, "/usage").Get("text")
	if !strings.Contains(u, "alice") || !strings.Contains(u, "3.0 GB of 10.0 GB") || !strings.Contains(u, "7.0 GB left") || strings.Contains(u, "bobby") {
		t.Errorf("/usage: %s", u)
	}
	if m := say(t, h, f, 501, "/status"); strings.Contains(m.Get("text"), "Servers:") {
		t.Errorf("a user got the operator's /status: %v", m)
	}
	// wrong codes: five an hour
	for i := 0; i < 5; i++ {
		say(t, h, f, 502, "/link WRONGCODE1")
	}
	if m := say(t, h, f, 502, "/link "+alice.must("POST", "/api/portal/telegram/code", nil, 200)["code"].(string)); !strings.Contains(m.Get("text"), "Too many wrong codes") {
		t.Errorf("a sixth try: %v", m)
	}
	// two at most; a Telegram account links to one account only
	say(t, h, f, 503, "/link "+alice.must("POST", "/api/portal/telegram/code", nil, 200)["code"].(string))
	if code, _, raw := alice.do("POST", "/api/portal/telegram/code", nil); code != 409 || !strings.Contains(string(raw), "unlink one first") {
		t.Errorf("a third code: %d %s", code, raw)
	}
	if m := say(t, h, f, 501, "/link "+bobby.must("POST", "/api/portal/telegram/code", nil, 200)["code"].(string)); !strings.Contains(m.Get("text"), "linked to another account") {
		t.Errorf("one Telegram account for two people: %v", m)
	}
	v := alice.must("GET", "/api/portal/telegram", nil, 200)
	if links, _ := v["links"].([]any); len(links) != 2 || v["unlink_at"] != float64(0) {
		t.Fatalf("alice's links: %v", v)
	}

	// unlinking: confirmed, then not again for a month
	m := say(t, h, f, 501, "/unlink")
	yes := buttons(t, m)["Yes, unlink"]
	f.queue(tgPrivatePress(501, 1, yes))
	poll(t, h)
	if _, edits, _ := f.take(); len(edits) != 1 || !strings.Contains(edits[0].Get("text"), "unlinked") {
		t.Fatalf("unlink: %v", edits)
	}
	if l := h.p.tgLinkOf(ctx, 501); l != nil {
		t.Fatal("still linked")
	}
	if m := say(t, h, f, 503, "/unlink"); !strings.Contains(m.Get("text"), "once a month") {
		t.Errorf("a second unlink: %v", m)
	}
	l := h.p.tgLinkOf(ctx, 503)
	if code, _, raw := alice.do("DELETE", fmt.Sprintf("/api/portal/telegram/%d", l.ID), nil); code != 409 || !strings.Contains(string(raw), "once a month") {
		t.Errorf("a second unlink from the page: %d %s", code, raw)
	}
	// the supervisor may always
	list := b.must("GET", "/api/telegram/links", nil, 200)["links"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["user"] != "alice" {
		t.Fatalf("the supervisor's list: %v", list)
	}
	b.must("DELETE", fmt.Sprintf("/api/telegram/links/%d", l.ID), nil, 200)
	if h.p.tgLinkOf(ctx, 503) != nil {
		t.Error("the supervisor's unlink did not unlink")
	}
	if code, _, _ := alice.do("GET", "/api/telegram/links", nil); code != 401 {
		t.Errorf("a user listed every link: %d", code)
	}

	// linking off: private chats get no answer at all (the bot answers its chat only)
	say(t, h, f, 504, "/link "+bobby.must("POST", "/api/portal/telegram/code", nil, 200)["code"].(string))
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_user_link": false}, 200)
	f.queue(tgPrivate(504, "/usage"))
	poll(t, h)
	if sent, _, _ := f.take(); len(sent) != 0 {
		t.Errorf("a linked user while linking is off: %v", sent)
	}
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_commands": false}, 200)
	f.queue(tgPrivate(505, "/usage"))
	if c := h.p.notifyConfig(); c.listening() {
		t.Fatal("the bot listens with everything off")
	}
}

// TestTelegramMiniApp: the Mini App signs a linked account in, links an unlinked one with its
// username and password (guarded), and a session made there cannot change passwords or tokens.
func TestTelegramMiniApp(t *testing.T) {
	h, b, _ := botSetup(t, map[string]any{"telegram_user_link": true})
	set := b.must("GET", "/api/settings", nil, 200)
	set["public_url"] = "https://panel.example.com"
	b.must("PUT", "/api/settings", set, 200)
	b.must("POST", "/api/users", map[string]any{"name": "carol", "username": "carol", "password": "carol-password-1"}, 201)

	carol := tgUser{ID: 601, FirstName: "Carol"}
	app := h.browser()
	v := app.must("POST", "/api/tg/session", map[string]any{"init_data": initData(botToken, carol, now())}, 200)
	if v["linked"] != false || v["kind"] != nil {
		t.Fatalf("an unlinked account: %v", v)
	}
	if code, _, _ := app.do("POST", "/api/tg/session", map[string]any{"init_data": initData(badToken, carol, now())}); code != 401 {
		t.Errorf("forged launch data: %d", code)
	}
	// wrong passwords: five an hour per Telegram account
	for i := 0; i < 5; i++ {
		if code, _, _ := app.do("POST", "/api/tg/link", map[string]any{"init_data": initData(botToken, carol, now()), "username": "carol",
			"password": fmt.Sprint("wrong-password-", i)}); code != 401 {
			t.Fatalf("a wrong password: %d", code)
		}
	}
	if code, _, _ := app.do("POST", "/api/tg/link", map[string]any{"init_data": initData(botToken, carol, now()), "username": "carol",
		"password": "carol-password-1"}); code != 429 {
		t.Errorf("a sixth try: %d", code)
	}
	h.p.limiter.reset("tglink:601")
	h.p.limiter.reset("ip:127.0.0.1")
	h.p.signin = signinGuard{}
	v = app.must("POST", "/api/tg/link", map[string]any{"init_data": initData(botToken, carol, now()), "username": "carol", "password": "carol-password-1"}, 200)
	if v["kind"] != "user" || v["linked"] != true {
		t.Fatalf("linked: %v", v)
	}
	if me := app.must("GET", "/api/portal/me", nil, 200); me["username"] != "carol" {
		t.Errorf("the session: %v", me)
	}
	if code, _, raw := app.do("POST", "/api/portal/password", map[string]any{"current": "carol-password-1", "password": "carol-password-2"}); code != 403 ||
		!strings.Contains(string(raw), "browser") {
		t.Errorf("a password change from Telegram: %d %s", code, raw)
	}
	// next time it opens directly
	again := h.browser()
	if v := again.must("POST", "/api/tg/session", map[string]any{"init_data": initData(botToken, carol, now())}, 200); v["kind"] != "user" {
		t.Errorf("a linked account: %v", v)
	}

	// the supervisor: only while allowed, and only from a browser
	sup := tgUser{ID: 602, FirstName: "Boss"}
	if code, _, raw := app.do("POST", "/api/tg/link", map[string]any{"init_data": initData(botToken, sup, now()), "username": "owner",
		"password": "owner-password-1"}); code != 403 || !strings.Contains(string(raw), "off") {
		t.Errorf("the supervisor while off: %d %s", code, raw)
	}
	full := b.must("POST", "/api/tokens", map[string]any{"name": "full", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(full).do("PUT", "/api/settings/notify", map[string]any{"telegram_panel": true}); code != 403 {
		t.Errorf("an API token allowed the panel from Telegram: %d", code)
	}
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_panel": true}, 200)
	boss := h.browser()
	v = boss.must("POST", "/api/tg/link", map[string]any{"init_data": initData(botToken, sup, now()), "username": "owner", "password": "owner-password-1"}, 200)
	if v["kind"] != "admin" {
		t.Fatalf("the supervisor linked: %v", v)
	}
	boss.must("GET", "/api/servers", nil, 200)
	for _, req := range [][3]string{{"POST", "/api/tokens", `{"name":"x","scope":"full"}`}, {"POST", "/api/me/password", `{}`}, {"GET", "/api/me/sessions", ""}} {
		var body any
		if req[2] != "" {
			_ = json.Unmarshal([]byte(req[2]), &body)
		}
		if code, _, raw := boss.do(req[0], req[1], body); code != 403 || !strings.Contains(string(raw), "Telegram") {
			t.Errorf("%s %s from a Telegram sign-in: %d %s", req[0], req[1], code, raw)
		}
	}
	var n int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'telegram_panel_linked'`).Scan(&n)
	if n != 1 {
		t.Errorf("the supervisor's link was not recorded: %d", n)
	}
	// turned off: the supervisor's link opens nothing
	b.must("PUT", "/api/settings/notify", map[string]any{"telegram_panel": false}, 200)
	if code, _, _ := h.browser().do("POST", "/api/tg/session", map[string]any{"init_data": initData(botToken, sup, now())}); code != 403 {
		t.Errorf("the supervisor's link while off: %d", code)
	}
}

// TestTelegramSupervisorChat: the supervisor's linked account uses the operator's commands in a
// private chat and may make changes there (with changes allowed) without being listed.
func TestTelegramSupervisorChat(t *testing.T) {
	h, b, f := botSetup(t, map[string]any{"telegram_panel": true, "telegram_changes": true})
	b.must("POST", "/api/users", map[string]any{"name": "dave"}, 201)
	code := b.must("POST", "/api/telegram/code", nil, 200)["code"].(string)
	if m := say(t, h, f, 701, "/link "+code); !strings.Contains(m.Get("text"), "opens the panel") {
		t.Fatalf("the supervisor's link: %v", m)
	}
	if m := say(t, h, f, 701, "/status"); !strings.Contains(m.Get("text"), "Servers:") {
		t.Errorf("/status in private: %v", m)
	}
	m := say(t, h, f, 701, "/pause dave")
	yes := buttons(t, m)["Yes, pause dave"]
	if yes == "" {
		t.Fatalf("/pause in private: %v", m)
	}
	f.queue(tgPrivatePress(701, 1, yes))
	poll(t, h)
	var paused bool
	h.p.db.QueryRow(`SELECT paused FROM subs WHERE name = 'dave'`).Scan(&paused)
	if !paused {
		t.Error("the supervisor's linked account could not pause")
	}
	// someone else in private: nothing of the operator's
	if m := say(t, h, f, 702, "/status"); strings.Contains(m.Get("text"), "Servers:") {
		t.Errorf("a stranger got /status: %v", m)
	}
	// the menus: the supervisor's chat has the operator's commands
	f.mu.Lock()
	cmds := strings.Join(f.commands, "\n")
	f.mu.Unlock()
	if !strings.Contains(cmds, `"command":"unlink"`) {
		t.Errorf("the supervisor's menu: %s", cmds)
	}
}
