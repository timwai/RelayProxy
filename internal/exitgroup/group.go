// Package exitgroup defines platform-independent, ordered Agent exit groups.
package exitgroup

import (
	"fmt"
	"slices"
	"strings"
)

const Prefix = "group:"

type Group struct {
	ID      string   `json:"id" yaml:"id"`
	Name    string   `json:"name" yaml:"name"`
	Members []string `json:"members" yaml:"members"`
}

func IsID(id string) bool { return strings.HasPrefix(id, Prefix) }

func Clone(groups []Group) []Group {
	groups = slices.Clone(groups)
	for i := range groups {
		groups[i].Members = slices.Clone(groups[i].Members)
	}
	return groups
}

func Validate(groups []Group, references ...string) error {
	if len(groups) > 128 {
		return fmt.Errorf("最多支持 128 个设备分组")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, g := range groups {
		if !IsID(g.ID) || len(g.ID) <= len(Prefix) || len(g.ID) > 128 || strings.ContainsAny(g.ID, " /\\\t\r\n\x00") {
			return fmt.Errorf("无效的设备分组 ID %q", g.ID)
		}
		name := strings.TrimSpace(g.Name)
		if name == "" || name != g.Name || len([]rune(name)) > 64 || strings.ContainsAny(name, "\r\n\x00") {
			return fmt.Errorf("设备分组名称不能为空，且最多 64 个字符")
		}
		if ids[g.ID] || names[strings.ToLower(name)] {
			return fmt.Errorf("设备分组 ID 或名称重复: %s", name)
		}
		ids[g.ID], names[strings.ToLower(name)] = true, true
		if len(g.Members) == 0 || len(g.Members) > 128 {
			return fmt.Errorf("分组 %q 必须有 1-128 个成员", name)
		}
		seen := map[string]bool{}
		for _, id := range g.Members {
			if id == "" || strings.TrimSpace(id) != id || len(id) > 128 || strings.ContainsAny(id, " /\\\t\r\n\x00") || IsID(id) || strings.HasPrefix(id, "local:") || seen[id] {
				return fmt.Errorf("分组 %q 的成员无效或重复: %q（仅支持单台 Agent 出口）", name, id)
			}
			seen[id] = true
		}
	}
	for _, id := range references {
		if IsID(id) && !ids[id] {
			return fmt.Errorf("出口引用了不存在的设备分组 %q，请先移除引用", id)
		}
	}
	return nil
}

// Candidates returns a new ordered list. Missing groups never select another
// group's members or fall back to a single-device default.
func Candidates(groups []Group, id string, usable func(string) bool) []string {
	for _, g := range groups {
		if g.ID == id {
			return slices.DeleteFunc(slices.Clone(g.Members), func(id string) bool { return usable != nil && !usable(id) })
		}
	}
	return nil
}
