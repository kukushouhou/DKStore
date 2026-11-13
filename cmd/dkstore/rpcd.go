package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOpenWrtUCIConfig = "/etc/config/dkstore"
	defaultOpenWrtAppSocket = "/var/run/dkstore/app.sock"
)

// runRPCD 实现 OpenWrt rpcd 可执行插件协议（/usr/libexec/rpcd/luci.dkstore），零外部命令依赖。
func runRPCD(args []string) error {
	if len(args) == 0 {
		fmt.Println("{}")
		return nil
	}
	switch args[0] {
	case "list":
		fmt.Println(`{"status":{},"launch":{}}`)
		return nil
	case "call":
		// 消耗 rpcd 传入的 stdin JSON 参数
		_, _ = io.Copy(io.Discard, io.LimitReader(os.Stdin, 4096))
		method := ""
		if len(args) > 1 {
			method = args[1]
		}
		enabled, port, sockPath := readOpenWrtUCIConfig(defaultOpenWrtUCIConfig)
		hc := &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sockPath)
				},
			},
		}
		switch method {
		case "status":
			running := checkUnixHealth(hc)
			out, _ := json.Marshal(map[string]any{
				"running": running,
				"enabled": enabled,
				"port":    port,
			})
			fmt.Println(string(out))
			return nil
		case "launch":
			ticket, running := requestDeviceTicket(hc)
			if !running {
				running = checkUnixHealth(hc)
			}
			out, _ := json.Marshal(map[string]any{
				"running": running,
				"enabled": enabled,
				"port":    port,
				"ticket":  ticket,
			})
			fmt.Println(string(out))
			return nil
		default:
			fmt.Println(`{"error":"unknown_method"}`)
			return nil
		}
	default:
		fmt.Println("{}")
		return nil
	}
}

func checkUnixHealth(hc *http.Client) bool {
	resp, err := hc.Get("http://unix/api/v1/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func requestDeviceTicket(hc *http.Client) (string, bool) {
	resp, err := hc.Post("http://unix/api/v1/auth/device-ticket", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var res struct {
		Ticket string `json:"ticket"`
	}
	if json.NewDecoder(resp.Body).Decode(&res) != nil {
		return "", true
	}
	return res.Ticket, true
}

func readOpenWrtUCIConfig(path string) (enabled bool, port int, sockPath string) {
	enabled = true
	port = 8090
	sockPath = defaultOpenWrtAppSocket

	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "option ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		key := fields[1]
		val := strings.Trim(fields[2], `"'`)
		switch key {
		case "enabled":
			enabled = val != "0" && !strings.EqualFold(val, "false")
		case "port":
			if n, err := strconv.Atoi(val); err == nil && n > 0 && n <= 65535 {
				port = n
			}
		case "socket":
			if val != "" {
				sockPath = val
			}
		}
	}
	return
}
