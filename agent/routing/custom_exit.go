package routing

import (
 "fmt"
 "net"
 "strconv"
 "strings"
)

// CustomExit describes an Agent-owned upstream proxy. IDs deliberately live in
// a separate namespace from Server-issued device IDs.
type CustomExit struct {
 ID string `yaml:"id" json:"id"`
 Name string `yaml:"name" json:"name"`
 Enabled bool `yaml:"enabled" json:"enabled"`
 Protocol string `yaml:"protocol" json:"protocol"`
 Address string `yaml:"address" json:"address"`
 Username string `yaml:"username,omitempty" json:"username,omitempty"`
 Password string `yaml:"password,omitempty" json:"-"`
 HasPassword bool `yaml:"-" json:"hasPassword,omitempty"`
}

func IsCustomExitID(id string) bool { return strings.HasPrefix(id, "local:") }

// ValidateCustomExits checks all entries, including disabled entries, before
// rules or shared upstreams can reference them.
func ValidateCustomExits(exits []CustomExit) error {
 seen := make(map[string]struct{}, len(exits))
 for i, item := range exits {
  if !IsCustomExitID(item.ID) || len(item.ID) <= len("local:") || len(item.ID) > 128 || strings.ContainsAny(item.ID, " \t\r\n/\\") {
   return fmt.Errorf("proxy.custom_exits[%d]: invalid local exit id", i)
  }
  if _, ok := seen[item.ID]; ok { return fmt.Errorf("proxy.custom_exits: duplicate id %q", item.ID) }
  seen[item.ID] = struct{}{}
  if strings.TrimSpace(item.Name) == "" || len(item.Name) > 128 {
   return fmt.Errorf("proxy.custom_exits[%d]: name must be 1-128 characters", i)
  }
  switch item.Protocol {
  case "socks5", "http", "https":
  default:
   return fmt.Errorf("proxy.custom_exits[%d]: unsupported protocol %q", i, item.Protocol)
  }
  host, port, err := net.SplitHostPort(item.Address)
  if err != nil || host == "" { return fmt.Errorf("proxy.custom_exits[%d]: address must be host:port", i) }
  p, err := strconv.Atoi(port)
  if err != nil || p < 1 || p > 65535 { return fmt.Errorf("proxy.custom_exits[%d]: invalid port", i) }
  if len(item.Username) > 255 || len(item.Password) > 255 {
   return fmt.Errorf("proxy.custom_exits[%d]: credentials exceed 255 bytes", i)
  }
 }
 return nil
}

func CloneCustomExits(items []CustomExit) []CustomExit {
 if items == nil { return nil }
 return append([]CustomExit{}, items...)
}

func FindCustomExit(items []CustomExit, id string) (CustomExit, bool) {
 for _, item := range items { if item.ID == id { return item, true } }
 return CustomExit{}, false
}

// ValidateCustomReferences prevents dangling references and disabling/removing
// an exit that is used by the default, a rule, or the shared exit upstream.
func ValidateCustomReferences(items []CustomExit, defaultID, sharedID string, rules []Rule) error {
 used := map[string]string{}
 if IsCustomExitID(defaultID) { used[defaultID] = "proxy.default_exit_id" }
 for i, rule := range rules {
  if rule.Action == ActionProxy && IsCustomExitID(rule.ExitID) {
   used[rule.ExitID] = fmt.Sprintf("routing.rules[%d]", i)
  }
 }
 if sharedID != "" {
  if !IsCustomExitID(sharedID) { return fmt.Errorf("exit.upstream_exit_id must reference a local custom exit") }
  used[sharedID] = "exit.upstream_exit_id"
 }
 for id, source := range used {
  item, ok := FindCustomExit(items, id)
  if !ok { return fmt.Errorf("%s references missing custom exit %q", source, id) }
  if !item.Enabled { return fmt.Errorf("%s references disabled custom exit %q", source, id) }
 }
 return nil
}
