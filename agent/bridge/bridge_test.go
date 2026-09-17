package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"relayproxy/agent/app"
	"relayproxy/agent/divert"
	"relayproxy/agent/routing"
	"relayproxy/internal/config"
)

func newTestBridge(t *testing.T) *UIBridge {
	t.Helper()
	cfg := &config.AgentConfigFile{}
	cfg.Server.Address = "relay.example.test"
	cfg.Mode = "CLIENT"
	cfg.Device.Name = "test-agent"
	cfg.Proxy.DefaultExitID = "old-exit"
	cfg.Routing = routing.Config{Mode: routing.ModeRule, DefaultAction: routing.ActionProxy, Rules: []routing.Rule{
		{Name: "existing", Enabled: true, Targets: []string{"example.test"}, Action: routing.ActionReject},
	}}
	cfg.Network.ExcludeProcesses = []string{"old.exe"}
	if err := config.NormalizeAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	agent, err := app.NewAgent(app.AgentConfig{
		ServerAddress: cfg.Server.Address, QUICPort: cfg.Server.QUICPort, TCPPort: cfg.Server.TCPPort,
		DeviceName: cfg.Device.Name,
		Mode:       cfg.Mode, TransportMode: cfg.Transport.Mode,
		SOCKS5Enabled: cfg.Proxy.SOCKS5.Enabled,
		SOCKS5Listen:  net.JoinHostPort(cfg.Proxy.SOCKS5.Listen, strconv.Itoa(cfg.Proxy.SOCKS5.Port)),
		HTTPEnabled:   cfg.Proxy.HTTP.Enabled,
		HTTPListen:    net.JoinHostPort(cfg.Proxy.HTTP.Listen, strconv.Itoa(cfg.Proxy.HTTP.Port)),
		DefaultExitID: cfg.Proxy.DefaultExitID, ExitEnabled: cfg.Exit.Enabled,
		AllowInternet: cfg.Exit.AllowInternet, AllowPrivateNet: cfg.Exit.AllowPrivateNetwork,
		AllowLoopback: cfg.Exit.AllowLoopback, AccessMode: cfg.Exit.Access.Mode,
		AccessDomains: cfg.Exit.Access.Domains, AccessCIDRs: cfg.Exit.Access.CIDRs,
		NetworkMode: cfg.Network.Mode, DivertConfig: cfg.DivertConfig(), Routing: cfg.Routing,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Never start the Agent: these tests exercise configuration and policy
	// publication without proxy listeners, relay connections or interception.
	t.Cleanup(func() { _ = agent.Close() })
	b := NewUIBridge(agent, path)
	// Configuration tests never change the developer's login entries/tasks.
	b.syncAutoStart = func(string, bool) (func() error, error) { return nil, nil }
	b.setAutoStart = func(string, bool, bool) error { return nil }
	return b
}

func readConfigBytes(t *testing.T, b *UIBridge) []byte {
	t.Helper()
	data, err := os.ReadFile(b.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStartupMigrationFailurePreventsConfigSave(t *testing.T) {
	b := newTestBridge(t)
	before := readConfigBytes(t, b)
	failure := errors.New("task registration unavailable")
	b.syncAutoStart = func(string, bool) (func() error, error) { return nil, failure }
	var in ConfigUpdate
	in.Network.Mode = ptr("")
	in.Proxy.DefaultExitID = ptr("new-exit")
	if _, err := b.SaveConfig(in); !errors.Is(err, failure) {
		t.Fatalf("startup migration error was lost: %v", err)
	}
	if !bytes.Equal(before, readConfigBytes(t, b)) || b.agent.Config().DefaultExitID != "old-exit" {
		t.Fatal("configuration changed after startup migration failed")
	}
}

func TestConfigWriteFailureRollsBackStartupMigration(t *testing.T) {
	b := newTestBridge(t)
	rolledBack := false
	b.syncAutoStart = func(string, bool) (func() error, error) {
		return func() error { rolledBack = true; return nil }, nil
	}
	failure := errors.New("configuration disk full")
	b.writeConfig = func(string, *config.AgentConfigFile) error { return failure }
	var in ConfigUpdate
	in.Network.Mode = ptr("")
	if _, err := b.SaveConfig(in); !errors.Is(err, failure) || !rolledBack {
		t.Fatalf("failed configuration save left startup migration applied: %v", err)
	}
}

func TestAutoStartUsesSavedTransparentModeBeforeRestart(t *testing.T) {
	b := newTestBridge(t)
	cfg, err := config.LoadAgentConfig(b.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Network.Mode = "divert"
	if err := config.SaveAgentConfig(b.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	called := false
	b.setAutoStart = func(path string, enabled, requireAdmin bool) error {
		called = true
		if path != b.configPath || !enabled || !requireAdmin {
			t.Fatal("autostart did not use the saved transparent-proxy mode")
		}
		return nil
	}
	if err := b.SetAutoStart(true); err != nil || !called || b.agent.Config().NetworkMode != "" {
		t.Fatalf("autostart requires an unnecessary agent restart: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func replacementRouting() *RoutingConfigUpdate {
	return &RoutingConfigUpdate{Mode: ptr("rule"), DefaultAction: ptr("REJECT"), Rules: []routing.Rule{
		{Name: "replacement", Enabled: true, Targets: []string{"new.example.test"}, Action: routing.ActionProxy},
	}}
}

func TestInvalidUpdatesPreserveDiskAndRuntime(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ConfigUpdate)
	}{
		{"inactive ACL", func(in *ConfigUpdate) { in.Exit.Access.CIDRs = ptr([]string{"not-a-network"}) }},
		{"routing", func(in *ConfigUpdate) {
			in.Routing = &RoutingConfigUpdate{Rules: []routing.Rule{{Enabled: true, Targets: []string{"bad-cidr/999"}, Action: routing.ActionDirect}}}
		}},
		{"disabled process rule", func(in *ConfigUpdate) {
			in.Routing = &RoutingConfigUpdate{Rules: []routing.Rule{{Enabled: false, Processes: []string{"browser.exe"}, Ports: []string{"70000"}, Action: routing.ActionProxy}}}
		}},
		{"theme", func(in *ConfigUpdate) { in.GUI.Theme = ptr("unknown-theme") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBridge(t)
			before, runtime := readConfigBytes(t, b), b.agent.Config()
			var in ConfigUpdate
			tc.edit(&in)
			if _, err := b.SaveConfig(in); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
			if !bytes.Equal(before, readConfigBytes(t, b)) || !reflect.DeepEqual(runtime, b.agent.Config()) {
				t.Fatal("failed validation changed saved or applied configuration")
			}
		})
	}
}

func TestWriteFailureDoesNotPublishPoliciesOrExit(t *testing.T) {
	b := newTestBridge(t)
	before, runtime := readConfigBytes(t, b), b.agent.Config()
	failure := errors.New("simulated full disk")
	b.writeConfig = func(string, *config.AgentConfigFile) error { return failure }
	in := ConfigUpdate{Routing: replacementRouting()}
	in.Proxy.DefaultExitID = ptr("new-exit")
	in.Network.ExcludeProcesses = ptr([]string{})
	if _, err := b.SaveConfig(in); !errors.Is(err, failure) {
		t.Fatalf("SaveConfig error = %v, want disk failure", err)
	}
	if !bytes.Equal(before, readConfigBytes(t, b)) || !reflect.DeepEqual(runtime, b.agent.Config()) {
		t.Fatal("write failure changed saved or applied configuration")
	}
}

func TestPublicSaveCannotBypassPersistence(t *testing.T) {
	b := newTestBridge(t)
	runtime := b.agent.Config()
	failure := errors.New("write rejected")
	b.writeConfig = func(string, *config.AgentConfigFile) error { return failure }
	var in ConfigUpdate
	if err := json.Unmarshal([]byte(`{"reload":true,"routing":{"mode":"direct","default_action":"DIRECT","rules":[]}}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveConfig(in); !errors.Is(err, failure) {
		t.Fatalf("public save bypassed writing: %v", err)
	}
	if !reflect.DeepEqual(runtime, b.agent.Config()) {
		t.Fatal("public reload flag applied an unsaved patch")
	}
}

func TestApplyFailureLeavesSavedPoliciesPending(t *testing.T) {
	b := newTestBridge(t)
	runtime := b.agent.Config()
	if err := b.agent.Close(); err != nil {
		t.Fatal(err)
	}
	in := ConfigUpdate{Routing: replacementRouting()}
	in.Proxy.DefaultExitID = ptr("new-exit")
	if _, err := b.SaveConfig(in); err == nil || !strings.Contains(err.Error(), "已保存在磁盘") {
		t.Fatalf("apply failure was not distinguished from persistence failure: %v", err)
	}
	state, err := b.GetConfigState()
	if err != nil || !state.ReloadPending || state.Config.Routing.Rules[0].Name != "replacement" || state.Config.Proxy.DefaultExitID != "new-exit" {
		t.Fatalf("saved policies were lost or shown as applied: %+v, error = %v", state, err)
	}
	if !reflect.DeepEqual(runtime, b.agent.Config()) {
		t.Fatal("closed agent published a policy or exit change")
	}
}

func TestRestartStateTracksAppliedSettingsAcrossSaves(t *testing.T) {
	b := newTestBridge(t)
	initial, err := b.GetConfigState()
	if err != nil || initial.RestartRequired || initial.ReloadPending {
		t.Fatalf("initial state = %+v, error = %v", initial, err)
	}
	in := ConfigUpdate{Routing: replacementRouting()}
	in.Server.Address = ptr("next.example.test")
	result, err := b.SaveConfig(in)
	if err != nil || !result.RestartRequired || result.ReloadPending {
		t.Fatalf("mixed save = %+v, error = %v", result, err)
	}
	if b.agent.Config().ServerAddress != "relay.example.test" || b.agent.Config().Routing.Rules[0].Name != "replacement" {
		t.Fatal("startup setting was applied or hot rule was left pending")
	}
	var theme ConfigUpdate
	theme.GUI.Theme = ptr("light")
	result, err = b.SaveConfig(theme)
	if err != nil || !result.RestartRequired {
		t.Fatalf("later save lost pending restart: %+v, error = %v", result, err)
	}
	state, err := b.GetConfigState()
	if err != nil || !state.RestartRequired || state.Config.Server.Address != "next.example.test" || state.Runtime.Server.Address != "relay.example.test" {
		t.Fatalf("desired/applied state = %+v, error = %v", state, err)
	}
	var revert ConfigUpdate
	revert.Server.Address = ptr("relay.example.test")
	result, err = b.SaveConfig(revert)
	if err != nil || result.RestartRequired {
		t.Fatalf("reverted startup setting remains pending: %+v, error = %v", result, err)
	}
}

func TestStaleRevisionRejectsExternalEdit(t *testing.T) {
	b := newTestBridge(t)
	state, err := b.GetConfigState()
	if err != nil {
		t.Fatal(err)
	}
	external := append(readConfigBytes(t, b), []byte("\n# external edit\n")...)
	if err := os.WriteFile(b.configPath, external, 0600); err != nil {
		t.Fatal(err)
	}
	in := ConfigUpdate{Revision: &state.Revision, Routing: replacementRouting()}
	if _, err := b.SaveConfig(in); err == nil {
		t.Fatal("stale update overwrote an external edit")
	}
	if !bytes.Equal(external, readConfigBytes(t, b)) || b.agent.Config().Routing.Rules[0].Name != "existing" {
		t.Fatal("conflict changed disk or runtime")
	}
}

func TestExplicitEmptyRulesRemainEmpty(t *testing.T) {
	b := newTestBridge(t)
	in := ConfigUpdate{Routing: &RoutingConfigUpdate{Mode: ptr("rule"), DefaultAction: ptr("DIRECT"), Rules: []routing.Rule{}}}
	in.Network.ExcludeProcesses = ptr([]string{})
	result, err := b.SaveConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	cfg, revision, err := config.LoadAgentConfigWithRevision(b.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.Rules == nil || len(cfg.Routing.Rules) != 0 || len(b.agent.Config().Routing.Rules) != 0 || cfg.Routing.DefaultAction != routing.ActionDirect {
		t.Fatal("empty routing list was replaced by defaults")
	}
	if cfg.Network.ExcludeProcesses == nil || len(cfg.Network.ExcludeProcesses) != 0 {
		t.Fatal("empty process rules or exclusions were replaced by defaults")
	}
	if result.Revision != revision || result.ReloadPending {
		t.Fatalf("save result does not match saved/applied state: %+v", result)
	}
}

func TestNativeUDPRequirementRoundTripsThroughSharedRules(t *testing.T) {
	b := newTestBridge(t)
	var in ConfigUpdate
	raw := `{"routing":{"mode":"rule","rules":[{"name":"native","enabled":true,"processes":["game.exe"],"ports":["53"],"protocols":["udp"],"action":"PROXY","datagram_required":true}]}}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	if !in.Routing.Rules[0].DatagramRequired {
		t.Fatal("bridge did not decode the GUI's datagram requirement")
	}
	for _, required := range []bool{true, false} {
		in.Routing.Rules[0].DatagramRequired = required
		result, err := b.SaveConfig(in)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadAgentConfig(b.configPath)
		if err != nil {
			t.Fatal(err)
		}
		active := b.agent.Config()
		if cfg.Routing.Rules[0].DatagramRequired != required || active.Routing.Rules[0].DatagramRequired != required || result.ReloadPending || active.Routing.Rules[0].Processes[0] != "game.exe" || active.Routing.Rules[0].Ports[0] != "53" {
			t.Fatalf("required=%v did not survive persistence and application", required)
		}
	}
}

func TestReloadAppliesSharedRulesAndExclusionsWithoutRewritingFile(t *testing.T) {
	b := newTestBridge(t)
	cfg, err := config.LoadAgentConfig(b.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Routing.Rules = replacementRouting().Rules
	cfg.Network.ExcludeProcesses = []string{"new.exe"}
	cfg.Proxy.DefaultExitID = "new-exit"
	cfg.Server.TCPPort++
	if err := config.SaveAgentConfig(b.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	external := append(readConfigBytes(t, b), []byte("\n# formatting owned by the editor\n")...)
	if err := os.WriteFile(b.configPath, external, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := b.GetConfigState()
	if err != nil || !state.ReloadPending || !state.RestartRequired {
		t.Fatalf("external edit state = %+v, error = %v", state, err)
	}
	b.writeConfig = func(string, *config.AgentConfigFile) error {
		t.Fatal("reload attempted to rewrite the file")
		return nil
	}
	result, err := b.ReloadConfig()
	if err != nil || result.ReloadPending || !result.RestartRequired || result.Revision != state.Revision {
		t.Fatalf("reload = %+v, error = %v", result, err)
	}
	active := b.agent.Config()
	if active.Routing.Rules[0].Name != "replacement" || active.DivertConfig.ExcludeProcesses[0] != "new.exe" || active.DefaultExitID != "new-exit" || active.TCPPort == cfg.Server.TCPPort {
		t.Fatal("reload did not apply exactly the hot settings")
	}
	if !bytes.Equal(external, readConfigBytes(t, b)) {
		t.Fatal("reload rewrote externally edited YAML")
	}
	state, err = b.GetConfigState()
	if err != nil || state.ReloadPending || !state.RestartRequired {
		t.Fatalf("state after reload = %+v, error = %v", state, err)
	}
}

func TestMalformedFileCannotBeSavedOrReloaded(t *testing.T) {
	b := newTestBridge(t)
	runtime := b.agent.Config()
	invalid := []byte("routing: [not-valid-yaml")
	if err := os.WriteFile(b.configPath, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GetConfigState(); err == nil {
		t.Fatal("malformed file was exposed as a valid config")
	}
	if _, err := b.SaveConfig(ConfigUpdate{}); err == nil {
		t.Fatal("save replaced a malformed file with defaults")
	}
	if _, err := b.ReloadConfig(); err == nil {
		t.Fatal("reload applied a malformed file")
	}
	if !bytes.Equal(invalid, readConfigBytes(t, b)) || !reflect.DeepEqual(runtime, b.agent.Config()) {
		t.Fatal("malformed config changed disk or runtime")
	}
}

func TestUnsupportedInterceptionRejectedBeforeSave(t *testing.T) {
	b := newTestBridge(t)
	cfg := b.agent.Config().DivertConfig
	cfg.Mode = "divert"
	if divert.Preflight(cfg) == nil {
		t.Skip("platform has complete interception capabilities")
	}
	before := readConfigBytes(t, b)
	var in ConfigUpdate
	in.Network.Mode = ptr("divert")
	if _, err := b.SaveConfig(in); err == nil {
		t.Fatal("unsupported interception was enabled")
	}
	if !bytes.Equal(before, readConfigBytes(t, b)) || b.agent.Config().NetworkMode != "" {
		t.Fatal("unsupported configuration was saved or applied")
	}
}

func TestConcurrentConfigUpdatesDoNotLoseUnrelatedFields(t *testing.T) {
	b := newTestBridge(t)
	var theme, endpoint ConfigUpdate
	theme.GUI.Theme = ptr("light")
	endpoint.Server.Address = ptr("new.example.test")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, in := range []ConfigUpdate{theme, endpoint} {
		wg.Add(1)
		go func(in ConfigUpdate) {
			defer wg.Done()
			<-start
			_, err := b.SaveConfig(in)
			errs <- err
		}(in)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.LoadAgentConfig(b.configPath)
	if err != nil || cfg.GUI.Theme != "light" || cfg.Server.Address != "new.example.test" {
		t.Fatalf("concurrent updates lost fields: config = %+v, error = %v", cfg, err)
	}
}
