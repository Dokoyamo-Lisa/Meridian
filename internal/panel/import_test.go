package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"meridian/internal/agent/scan"
)

func TestImportKeepsCredentials(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Old box", "address": "203.0.113.20"}, 201)
	sid := id(srv["server"].(map[string]any)["id"])
	b.must("POST", "/api/users", map[string]any{"name": "alice", "sign_in": false}, 201) // matched by name

	res := scan.Result{Found: []scan.Found{{Software: "xray", Config: "/usr/local/etc/xray/config.json", Unit: "xray.service", Running: true,
		Inbounds: []scan.Inbound{
			{Tag: "reality", Protocol: "vless", Port: 443, Transport: "raw", Security: "reality", SNI: "www.microsoft.com",
				Target: "www.microsoft.com:443", PrivateKey: "aGVsbG8taGVsbG8taGVsbG8taGVsbG8taGVsbG8tMTI", ShortIDs: []string{"6ba85179e30d4fc2", ""},
				Users: []scan.User{{Name: "alice", ID: "11111111-1111-4111-8111-111111111111", Flow: "xtls-rprx-vision"},
					{Name: "bob", ID: "22222222-2222-4222-8222-222222222222", Flow: "xtls-rprx-vision"}}},
			{Tag: "ss", Protocol: "shadowsocks", Port: 8388, Method: "2022-blake3-aes-128-gcm", ServerKey: "c2VydmVyLWtleS0xNmJ5dA==",
				Users: []scan.User{{Name: "bob", Password: "dXNlci1rZXktMTZieXRlcw=="}}},
			{Tag: "kcp", Protocol: "vless", Port: 9000, Transport: "kcp", Note: "Meridian does not support the kcp transport"},
		}}}}
	raw, _ := json.Marshal(res)
	if _, err := h.p.db.Exec1(`INSERT INTO server_scans (server_id, at, data) VALUES (?, ?, ?)`, sid, now(), string(raw)); err != nil {
		t.Fatal(err)
	}
	view := b.must("GET", "/api/servers/"+itoa(sid)+"/scan", nil, 200)
	text, _ := json.Marshal(view)
	if strings.Contains(string(text), "aGVsbG8t") || strings.Contains(string(text), "11111111-1111") || strings.Contains(string(text), "dXNlci1r") {
		t.Fatalf("the scan view shows secrets: %s", text)
	}
	ins := view["found"].([]any)[0].(map[string]any)["inbounds"].([]any)
	if ins[0].(map[string]any)["importable"] != true || ins[2].(map[string]any)["importable"] != false {
		t.Fatalf("importability: %v", ins)
	}
	// take over without confirm is refused
	items := []map[string]any{{"config": "/usr/local/etc/xray/config.json", "tag": "reality", "port": 443},
		{"config": "/usr/local/etc/xray/config.json", "tag": "ss", "port": 8388}}
	if code, _, _ := b.do("POST", "/api/servers/"+itoa(sid)+"/import", map[string]any{"items": items, "take_over": true}); code != 400 {
		t.Fatalf("take over without confirm: %d", code)
	}
	out := b.must("POST", "/api/servers/"+itoa(sid)+"/import", map[string]any{"items": items, "take_over": true, "confirm": true}, 201)
	if len(out["nodes"].([]any)) != 2 || out["matched"].(float64) != 1 || len(out["users"].([]any)) != 1 || len(out["stop_actions"].([]any)) != 1 {
		t.Fatalf("import: %v", out)
	}
	bob := out["users"].([]any)[0].(map[string]any)
	if bob["name"] != "bob" || bob["password"] == nil {
		t.Fatalf("bob: %v", bob)
	}
	// importing the same protocol twice is refused
	if code, _, _ := b.do("POST", "/api/servers/"+itoa(sid)+"/import", map[string]any{"items": items[:1]}); code != 409 {
		t.Fatalf("second import: %d", code)
	}
	// the nodes stay off until the old service has stopped; then bob's link carries his old credentials
	if _, err := h.p.db.Exec1(`UPDATE nodes SET enabled = 1 WHERE server_id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	bobID := itoa(id(bob["id"]))
	d := b.must("GET", "/api/users/"+bobID, nil, 200)
	link := d["user"].(map[string]any)["link"].(string)
	resp, err := http.Get(h.srv.URL + link[strings.Index(link, "/s/"):] + "?client=uri")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	links := string(body)
	if !strings.Contains(links, "vless://22222222-2222-4222-8222-222222222222@") || !strings.Contains(links, "sid=6ba85179e30d4fc2") ||
		!strings.Contains(links, ":443?") || !strings.Contains(links, "ss://") {
		t.Fatalf("bob's links do not carry his old credentials:\n%s", links)
	}
	// the REALITY public key is derived from the imported private key
	nodes := out["nodes"].([]any)
	if pk := nodes[0].(map[string]any)["settings"].(map[string]any)["public_key"]; pk == nil || pk == "" {
		t.Fatalf("no public key: %v", nodes[0])
	}
}
