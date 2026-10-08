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
)

// TestNotifySettings: secrets are checked, stored on their own and only ever shown masked; read-only
// tokens see the masked view and cannot change it.
func TestNotifySettings(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	token := "123456789:AAEabcdefghijklmnopqrstuvwxyz0123456"
	for body, want := range map[string]string{
		`{"telegram_token":"nope","telegram_chat":"42"}`:           "does not look like a bot token",
		`{"telegram_token":"` + token + `"}`:                       "both the bot token and the chat",
		`{"telegram_token":"` + token + `","telegram_chat":"x y"}`: "numeric id",
		`{"webhook_url":"http://hooks.example.com/x"}`:             "https://",
		`{"webhook_url":"https://127.0.0.1/x"}`:                    "this machine",
		`{"webhook_url":"https://169.254.169.254/latest"}`:         "link-local",
		`{"groups":["servers","everything"]}`:                      "groups are",
	} {
		var in map[string]any
		_ = json.Unmarshal([]byte(body), &in)
		if code, m, _ := b.do("PUT", "/api/settings/notify", in); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), want) {
			t.Errorf("%s: %d %v", body, code, m)
		}
	}
	v := b.must("PUT", "/api/settings/notify", map[string]any{"telegram_token": token, "telegram_chat": "-1001234567890",
		"webhook_url": "https://hooks.example.com/services/T000/B000/SECRET", "groups": []string{"servers", "users"}}, 200)
	if v["telegram_token"] != "123456789:…3456" || v["webhook_url"] != "https://hooks.example.com/…" || v["active"] != true ||
		fmt.Sprint(v["groups"]) != "[servers users]" {
		t.Errorf("view: %v", v)
	}
	_, _, raw := b.do("GET", "/api/settings/notify", nil)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "AAEabcdef") {
		t.Errorf("the view shows a secret: %s", raw)
	}
	// the panel settings never carry them either
	if _, _, raw := b.do("GET", "/api/settings", nil); strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "AAEabcdef") {
		t.Errorf("panel settings show a secret: %s", raw)
	}
	// omitted fields keep their value; an empty one removes it
	v = b.must("PUT", "/api/settings/notify", map[string]any{"webhook_url": ""}, 200)
	if v["webhook_url"] != "" || v["telegram_token"] != "123456789:…3456" {
		t.Errorf("after removing the webhook: %v", v)
	}
	ro := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, raw := h.bearer(ro).do("GET", "/api/settings/notify", nil); code != 200 || strings.Contains(string(raw), "AAEabcdef") {
		t.Errorf("read token view: %d %s", code, raw)
	}
	if code, _, _ := h.bearer(ro).do("PUT", "/api/settings/notify", map[string]any{"telegram_chat": "1"}); code != 403 {
		t.Errorf("read token change: %d", code)
	}
}

// TestNotifyJob: new events of the chosen groups go out once, in order, through Telegram and the
// webhook; other events do not; nothing from before notifications were on is sent; a failing channel
// keeps the events for later.
func TestNotifyJob(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var got []string
	fail := false
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			form, _ := url.ParseQuery(string(body))
			got = append(got, "telegram:"+form.Get("chat_id")+":"+form.Get("text"))
			fmt.Fprint(w, `{"ok":true}`)
		case r.URL.Path == "/hook":
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			got = append(got, "webhook:"+fmt.Sprint(m["text"]))
		}
	}))
	defer ts.Close()
	old := telegramAPI
	telegramAPI = ts.URL
	defer func() { telegramAPI = old }()
	h.p.notify.client = ts.Client()
	ctx := context.Background()

	// before notifications are on: these never go out
	h.p.event(0, "crit", "server_offline", 0, 0, 0, "Old stopped reporting", nil)
	h.p.notifyTick(ctx)
	if err := h.p.saveNotifyConfig(notifyConfig{TelegramToken: "123456789:AAEabcdefghijklmnopqrstuvwxyz0123456", TelegramChat: "42",
		WebhookURL: ts.URL + "/hook", Groups: []string{"servers"}}); err != nil {
		t.Fatal(err)
	}
	h.p.notifyTick(ctx)
	if len(got) != 0 {
		t.Fatalf("the past was sent: %v", got)
	}

	h.p.event(0, "crit", "server_offline", 0, 0, 0, "Tokyo stopped reporting", nil)
	h.p.event(0, "info", "protocol_added", 0, 0, 0, "REALITY added to Tokyo", nil) // not a chosen group
	h.p.event(0, "info", "server_online", 0, 0, 0, "Tokyo is back online after 2 min", nil)
	h.p.notifyTick(ctx)
	mu.Lock()
	if len(got) != 2 || !strings.HasPrefix(got[0], "telegram:42:") || !strings.Contains(got[0], "Tokyo stopped reporting") ||
		!strings.Contains(got[0], "back online") || strings.Contains(got[0], "REALITY added") || strings.Contains(got[0], "Old stopped") ||
		!strings.HasPrefix(got[1], "webhook:") {
		t.Errorf("sent: %q", got)
	}
	got = nil
	mu.Unlock()
	h.p.notifyTick(ctx) // nothing new
	if len(got) != 0 {
		t.Errorf("sent twice: %v", got)
	}

	// every channel failing: the event waits and goes out once they work again
	mu.Lock()
	fail = true
	mu.Unlock()
	h.p.event(0, "warn", "apply_failed", 0, 0, 0, "Tokyo: xray: refused", nil)
	h.p.notifyTick(ctx)
	if v := h.p.viewOfNotify(); !strings.Contains(v.LastError, "503") {
		t.Errorf("last error: %q", v.LastError)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	h.p.notify.mu.Lock()
	h.p.notify.nextTry = h.p.notify.nextTry.AddDate(0, 0, -1) // the backoff has passed
	h.p.notify.mu.Unlock()
	h.p.notifyTick(ctx)
	if len(got) != 2 || !strings.Contains(got[0], "xray: refused") {
		t.Errorf("after recovery: %v", got)
	}
}

// TestLimitEvents: a used-up quota, an ended access and an expiring shared certificate are recorded
// once each - and nobody is paused.
func TestLimitEvents(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.must("POST", "/api/users", map[string]any{"name": "eve"}, 201)
	if _, err := h.p.db.Exec1(`UPDATE subs SET quota = 1000, cycle_up = 600, cycle_down = 600, expires_at = ?`, now()-60); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h.p.limitEvents(ctx)
	h.p.limitEvents(ctx)
	count := func(kind string) int {
		var n int
		_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&n)
		return n
	}
	if count("quota_reached") != 1 || count("user_expired") != 1 {
		t.Errorf("quota_reached %d, user_expired %d", count("quota_reached"), count("user_expired"))
	}
	var paused int
	_ = h.p.db.QueryRow(`SELECT paused FROM subs`).Scan(&paused)
	if paused != 0 {
		t.Error("a limit paused someone")
	}
}
