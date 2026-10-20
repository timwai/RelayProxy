package singleton

import (
	"encoding/json"
	"errors"
	"fmt"
)

const AgentInstanceName = "RelayProxyAgent_Instance"

type replacementRequest struct {
	Name      string `json:"name"`
	PID       uint32 `json:"pid"`
	Requester uint32 `json:"requester"`
	Created   uint64 `json:"created"`
}

// ReplacementHelperFlagName identifies the short-lived Windows UAC helper.
// It is handled before any Agent, listener, service or desktop startup.
func ReplacementHelperFlagName() string { return "_replace-agent" }

func parseReplacementRequest(raw string) (replacementRequest, error) {
	var request replacementRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return request, fmt.Errorf("无效的客户端替换请求: %w", err)
	}
	if request.Name != AgentInstanceName {
		return request, errors.New("客户端替换请求的单实例锁无效")
	}
	if request.Requester == 0 || request.Created == 0 {
		return request, errors.New("客户端替换请求缺少启动进程信息")
	}
	return request, nil
}

func (r replacementRequest) canReplace(pid uint32, created uint64) bool {
	return pid != 0 && pid != r.Requester && (r.PID == 0 || r.PID == pid) && created > 0 && created < r.Created
}
