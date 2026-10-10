package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// api calls the panel's API with an API token.
type api struct {
	base, token string
	hc          *http.Client
}

func newAPI(base, token string) *api {
	return &api{base: strings.TrimRight(base, "/"), token: token, hc: &http.Client{Timeout: 2 * time.Minute}}
}

func (a *api) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, e.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type apiServer struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CycleStart int64  `json:"cycle_start"`
	Nodes      []struct {
		ID       int64  `json:"id"`
		Kind     string `json:"kind"`
		Port     int    `json:"port"`
		Enabled  bool   `json:"enabled"`
		PassOnly bool   `json:"pass_only"`
	} `json:"nodes"`
}

type apiUser struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	Paused     bool   `json:"paused"`
	CycleStart int64  `json:"cycle_start"`
	ResetDay   int    `json:"reset_day"`
	ResetEvery int    `json:"reset_every"`
	Scope      struct {
		Servers   []int64 `json:"servers"`
		Protocols []int64 `json:"protocols"`
	} `json:"scope"`
}

// loadWorld reads the demo's servers and users from the panel; tokens are the agents' (server id ->
// token), from the state file.
func loadWorld(a *api, tokens map[int64]string) (*world, error) {
	var ss []apiServer
	if err := a.call("GET", "/api/servers", nil, &ss); err != nil {
		return nil, err
	}
	var us []apiUser
	if err := a.call("GET", "/api/users", nil, &us); err != nil {
		return nil, err
	}
	w := &world{}
	for i, m := range fleet {
		for _, s := range ss {
			if s.Name != m.Name {
				continue
			}
			srv := &server{m: m, idx: i, id: s.ID, token: tokens[s.ID], cycle: s.CycleStart}
			for _, n := range s.Nodes {
				if n.Enabled && !n.PassOnly {
					srv.nodes = append(srv.nodes, node{ID: n.ID, Kind: n.Kind, Port: n.Port})
				}
			}
			w.servers = append(w.servers, srv)
		}
	}
	if len(w.servers) == 0 {
		return nil, errors.New("the panel has none of the demo's servers - run setup first")
	}
	for i, p := range people {
		for _, au := range us {
			if au.Username != p.Username {
				continue
			}
			u := &user{p: p, idx: i, id: au.ID, cycle: au.CycleStart, paused: au.Paused, devices: devicesFor(i, p.Devices),
				nodes: map[int64][]int64{}}
			if au.ResetDay == 0 && au.ResetEvery == 0 { // never resets: everything since they started counts
				u.cycle = u.started()
			}
			everything := len(au.Scope.Servers) == 0 && len(au.Scope.Protocols) == 0
			for _, s := range w.servers {
				whole := everything
				for _, id := range au.Scope.Servers {
					whole = whole || id == s.id
				}
				for _, n := range s.nodes {
					ok := whole
					for _, id := range au.Scope.Protocols {
						ok = ok || id == n.ID
					}
					if ok {
						u.nodes[s.id] = append(u.nodes[s.id], n.ID)
					}
				}
			}
			w.users = append(w.users, u)
		}
	}
	w.affinities()
	return w, nil
}

func (w *world) kinds() map[int64]string {
	out := map[int64]string{}
	for _, s := range w.servers {
		for _, n := range s.nodes {
			out[n.ID] = n.Kind
		}
	}
	return out
}
