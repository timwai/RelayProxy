//go:build darwin

package systemproxy

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type darwinBackend struct{}

func newBackend() backend { return darwinBackend{} }

func runRead(args ...string) (string, error) {
	out, err := exec.Command("/usr/sbin/networksetup", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("networksetup %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}
func quoteShell(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func runWrite(args ...string) error {
	out, err := exec.Command("/usr/sbin/networksetup", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	failure := strings.ToLower(string(out))
	if !strings.Contains(failure, "admin") && !strings.Contains(failure, "privilege") && !strings.Contains(failure, "permission") {
		return fmt.Errorf("networksetup %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
	}
	// Admin authorization is requested only for this fixed executable and escaped arguments.
	// A long-lived root broker is deliberately not installed without a signing identity.
	words := []string{quoteShell("/usr/sbin/networksetup")}
	for _, a := range args {
		words = append(words, quoteShell(a))
	}
	script := "do shell script " + strconv.Quote(strings.Join(words, " ")) + " with administrator privileges"
	result, scriptErr := exec.Command("/usr/bin/osascript", "-e", script).CombinedOutput()
	if scriptErr != nil {
		return fmt.Errorf("macOS authorization or networksetup failed: %s: %w", strings.TrimSpace(string(result)), scriptErr)
	}
	return nil
}
func (darwinBackend) CurrentService() (string, error) {
	out, err := exec.Command("/sbin/route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("find active default route: %s: %w", strings.TrimSpace(string(out)), err)
	}
	iface := ""
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "interface:" {
			iface = f[1]
			break
		}
	}
	if iface == "" {
		return "", fmt.Errorf("default route has no interface")
	}
	services, err := runRead("-listnetworkserviceorder")
	if err != nil {
		return "", err
	}
	pending := ""
	for _, raw := range strings.Split(services, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "(") && strings.Contains(line, ") ") && !strings.Contains(line, "Hardware Port:") {
			pending = strings.TrimSpace(line[strings.Index(line, ") ")+2:])
			if strings.HasPrefix(pending, "*") {
				pending = ""
			}
			continue
		}
		if pending != "" && strings.Contains(line, "Device: "+iface+")") {
			return pending, nil
		}
	}
	return "", fmt.Errorf("no enabled macOS network service mapped to %s", iface)
}

var statusLine = regexp.MustCompile(`(?m)^([^:]+):[ \t]*(.*)$`)

func (darwinBackend) Read(service, kind string) (ProxyState, error) {
	op, ok := map[string]string{"web": "-getwebproxy", "secureweb": "-getsecurewebproxy", "socks": "-getsocksfirewallproxy"}[kind]
	if !ok {
		return ProxyState{}, fmt.Errorf("unknown proxy kind %q", kind)
	}
	output, err := runRead(op, service)
	if err != nil {
		return ProxyState{}, err
	}
	s := ProxyState{}
	for _, v := range statusLine.FindAllStringSubmatch(output, -1) {
		name, value := strings.TrimSpace(v[1]), strings.TrimSpace(v[2])
		switch name {
		case "Enabled":
			s.Enabled = strings.EqualFold(value, "yes")
		case "Server":
			s.Host = value
		case "Port":
			s.Port, _ = strconv.Atoi(value)
		case "Authenticated Proxy Enabled":
			s.Authenticated = strings.EqualFold(value, "yes")
		}
	}
	return s, nil
}
func (darwinBackend) Validate(service string) error {
	// PAC and auto-discovery can override the intended HTTP proxy choice; never
	// silently disable somebody else's PAC settings.
	for _, op := range []string{"-getautoproxyurl", "-getproxyautodiscovery"} {
		output, err := runRead(op, service)
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToLower(output), "enabled: yes") || strings.Contains(strings.ToLower(output), "auto proxy discovery: on") {
			return fmt.Errorf("service %q has PAC/auto discovery enabled; turn it off manually before enabling RelayProxy", service)
		}
	}
	return nil
}
func (darwinBackend) Write(service, kind string, s ProxyState) error {
	pair, ok := map[string][2]string{
		"web":       {"-setwebproxy", "-setwebproxystate"},
		"secureweb": {"-setsecurewebproxy", "-setsecurewebproxystate"},
		"socks":     {"-setsocksfirewallproxy", "-setsocksfirewallproxystate"},
	}[kind]
	if !ok {
		return fmt.Errorf("unknown proxy kind %q", kind)
	}
	if s.Authenticated {
		return fmt.Errorf("restoring authenticated proxies is not supported")
	}
	if s.Host != "" && s.Port > 0 {
		if err := runWrite(pair[0], service, s.Host, strconv.Itoa(s.Port), "off"); err != nil {
			return err
		}
	}
	state := "off"
	if s.Enabled {
		state = "on"
	}
	if err := runWrite(pair[1], service, state); err != nil {
		return err
	}
	return nil
}

// Keep lint/import check local: bytes is deliberately not needed after applying
// the strict command argument escaping above.
var _ = bytes.MinRead
