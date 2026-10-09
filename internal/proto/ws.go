package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

// The agent's WebSocket (GET WSPath, itself signed like any request; agents 1.0 and later): the same
// signed requests and sealed answers as over HTTP - the state's long poll, reports, the country list -
// several at a time on one lasting connection. Each message is one request or one answer: its head
// (WSRequest or WSAnswer, JSON) after its length (4 bytes, big-endian), then the body exactly as it
// would travel over HTTP. Agents use it unless Settings say HTTP (AgentSettings.Transport), and go
// back to HTTP by themselves while it cannot be opened.

// WSPath is where agents open their WebSocket.
const WSPath = "/agent/v1/ws"

// WSRequest is a request's head: what goes into the HTTP request line and its signed headers.
type WSRequest struct {
	ID     uint32 `json:"id"`     // the answer carries it back
	Method string `json:"method"` // GET or POST
	Path   string `json:"path"`   // path and query, as signed
	Time   int64  `json:"time"`
	Nonce  string `json:"nonce"`
	Sign   string `json:"sign"`
	Type   string `json:"type,omitempty"` // the body's content type
}

// WSAnswer is an answer's head.
type WSAnswer struct {
	ID     uint32 `json:"id"`
	Status int    `json:"status"`
	Type   string `json:"type,omitempty"`
}

// WSPack makes one message of a head and a body.
func WSPack(head any, body []byte) ([]byte, error) {
	h, err := json.Marshal(head)
	if err != nil {
		return nil, err
	}
	msg := make([]byte, 4, 4+len(h)+len(body))
	binary.BigEndian.PutUint32(msg, uint32(len(h)))
	return append(append(msg, h...), body...), nil
}

// WSUnpack reads a message's head into head and returns its body.
func WSUnpack(msg []byte, head any) ([]byte, error) {
	if len(msg) < 4 {
		return nil, errors.New("message too short")
	}
	n := binary.BigEndian.Uint32(msg)
	if n > 64<<10 || uint64(n) > uint64(len(msg)-4) {
		return nil, errors.New("bad message head")
	}
	if err := json.Unmarshal(msg[4:4+n], head); err != nil {
		return nil, err
	}
	return msg[4+n:], nil
}
