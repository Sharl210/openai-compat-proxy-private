package upstream

import "encoding/json"

type Event struct {
	Event string
	Data  map[string]any
	Raw   json.RawMessage
}
