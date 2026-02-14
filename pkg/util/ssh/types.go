package ssh

const (
	wsMsgCmd       = "cmd"
	wsMsgResize    = "resize"
	wsMsgHeartbeat = "heartbeat"
)

type wsMsg struct {
	Type string `json:"type"`
	Cmd  string `json:"cmd"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Data string `json:"data"` // for heartbeat
}
