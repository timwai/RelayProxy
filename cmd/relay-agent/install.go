package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"relayproxy/internal/config"
)

func runInstall(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("relay-agent install", flag.ContinueOnError)
	flags.SetOutput(output)
	configFile := flags.String("config", "", "指定配置文件")
	configDir := flags.String("config-dir", "", "指定配置目录（保存 relay-agent.yaml）")
	logFile := flags.String("log-file", "", "后台日志文件（默认 ~/.relayproxy/logs/relay-agent.log）")
	flags.Usage = func() {
		fmt.Fprintln(output, "用法: relay-agent install [--config 文件 | --config-dir 目录] [--log-file 文件]")
		fmt.Fprintln(output, "交互填写基础配置，保存后启动后台 Agent。")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("install 不接受额外的位置参数: %s", strings.Join(flags.Args(), " "))
	}
	var fileExplicit, dirExplicit bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config":
			fileExplicit = true
		case "config-dir":
			dirExplicit = true
		}
	})
	path, err := resolveConfigPath(*configFile, *configDir, fileExplicit, dirExplicit)
	if err != nil {
		return err
	}
	cfg, err := configureInstall(path, input, output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}
	fmt.Fprintf(output, "\n配置已保存: %s\n正在后台启动 Agent…\n", path)
	// Use the resolved absolute file path so the detached process cannot
	// rediscover a different default or re-enter this interactive command.
	if err := launchBackground([]string{"--background", "--config=" + path}, *logFile); err != nil {
		return fmt.Errorf("配置已保存，但后台启动失败: %w", err)
	}
	fmt.Fprintln(output, "Agent 已在后台启动。首次连接后，请在 Server 管理页面审批该设备。")
	return nil
}

// The wizard only changes memory. EOF, invalid existing settings, or an
// interrupted wizard must never leave a half-written configuration on disk.
func configureInstall(path string, input io.Reader, output io.Writer) (*config.AgentConfigFile, error) {
	cfg, err := config.LoadAgentConfig(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("读取已有配置失败: %w", err)
		}
		cfg = defaultConfigFile()
		if err := config.NormalizeAgentConfig(cfg); err != nil {
			return nil, err
		}
	} else {
		fmt.Fprintln(output, "检测到已有配置，直接回车可保留当前值；其他设置将保留。")
	}
	fmt.Fprintf(output, "RelayProxy Agent 安装向导\n配置文件: %s\n按提示输入，直接回车使用方括号内的默认值。\n\n", path)
	prompt := installPrompt{reader: bufio.NewReader(input), output: output}
	cfg.Server.Address, err = prompt.ask("1. Server 地址（域名或 IP，不含端口）", cfg.Server.Address, func(value string) error {
		if net.ParseIP(value) == nil && strings.ContainsAny(value, ":/\\ \t\r\n") {
			return errors.New("请输入域名或 IP，不要包含协议、端口或路径")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, item := range []struct {
		label string
		port  *int
	}{
		{"2. Server TCP 端口", &cfg.Server.TCPPort},
		{"3. Server QUIC 端口", &cfg.Server.QUICPort},
	} {
		value, err := prompt.ask(item.label, strconv.Itoa(*item.port), func(value string) error {
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return errors.New("端口必须是 1 到 65535 之间的整数")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		*item.port, _ = strconv.Atoi(value)
	}
	tlsDefault := "y"
	if !cfg.IsServerTLSEnabled() {
		tlsDefault = "n"
	}
	tls, err := prompt.ask("4. 启用 TLS（y/n，关闭后只使用 TCP）", tlsDefault, func(value string) error {
		if !strings.EqualFold(value, "y") && !strings.EqualFold(value, "n") {
			return errors.New("请输入 y 或 n")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	cfg.Server.TLSEnabled = config.BoolPtr(strings.EqualFold(tls, "y"))
	cfg.Device.IdentityID, err = prompt.ask("5. 身份 ID（Server 生成的 16 位字母数字组合）", cfg.Device.IdentityID, func(value string) error {
		cfg.Device.IdentityID = value
		return config.NormalizeAgentConfig(cfg)
	})
	if err != nil {
		return nil, err
	}
	name := cfg.Device.Name
	if name == "" {
		name, _ = os.Hostname()
		if strings.TrimSpace(name) == "" {
			name = "RelayProxy Agent"
		}
	}
	cfg.Device.Name, err = prompt.ask("6. 设备名称", name, nil)
	if err != nil {
		return nil, err
	}
	if err := config.NormalizeAgentConfig(cfg); err != nil {
		return nil, fmt.Errorf("基础配置无效: %w", err)
	}
	return cfg, nil
}

type installPrompt struct {
	reader *bufio.Reader
	output io.Writer
}

func (p installPrompt) ask(label, defaultValue string, validate func(string) error) (string, error) {
	for {
		fmt.Fprint(p.output, label)
		if defaultValue != "" {
			fmt.Fprintf(p.output, " [%s]", defaultValue)
		}
		fmt.Fprint(p.output, ": ")
		line, err := p.reader.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
			return "", fmt.Errorf("配置向导未完成，未保存配置: %w", err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = defaultValue
		}
		if value == "" {
			fmt.Fprintln(p.output, "此项不能为空，请重新输入。")
			continue
		}
		if validate != nil {
			if err := validate(value); err != nil {
				fmt.Fprintf(p.output, "输入无效: %v，请重新输入。\n", err)
				continue
			}
		}
		return value, nil
	}
}
