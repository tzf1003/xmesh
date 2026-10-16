package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func Test_CLIUsageMentionsEverySubcommand(t *testing.T) {
	for _, word := range []string{"serve", "install", "pair", "status", "version", "--system", "--tunnel-listen", "--oauth-password-file", "--pat-file"} {
		if !strings.Contains(usage, word) {
			t.Errorf("usage 缺少 %q", word)
		}
	}
	if strings.Contains(usage, "status --config") {
		t.Error("status 没有 --config 参数")
	}
	if e := run(nil); e == nil || !strings.Contains(e.Error(), "用法: xmesh") {
		t.Fatalf("无参数应返回用法: %v", e)
	}
	if e := run([]string{"nope"}); e == nil || !strings.Contains(e.Error(), "未知子命令: nope") || !strings.Contains(e.Error(), "用法: xmesh") {
		t.Fatalf("未知子命令应附带用法: %v", e)
	}
}

func Test_CLIStatusFormatsNestedFields(t *testing.T) {
	var info map[string]any
	raw := `{"os":"linux","arch":"amd64","hostname":"dev-1","roles":["agent","gateway"],
		"network":{"mode":"tailnet","node_id":"dev-1","tags":["tag:device"]},
		"backends":[{"name":"ida","policy":"exclusive","state":"running","favorite":true}],
		"limits":{"exec_shared":4,"exec_shared_in_use":1,"exec_exclusive":1,"exec_exclusive_in_use":0,"pty_max_sessions":20,"pty_max_per_caller":8,"pty_in_use":2,"pty_active":1,"pty_closed":3,"pty_closed_retain_max":20},
		"restart_required":true,"warnings":["节点、网络或监听配置已变更，需要重启"]}`
	if e := json.Unmarshal([]byte(raw), &info); e != nil {
		t.Fatal(e)
	}
	got := formatStatus(info)
	for _, want := range []string{
		"节点: dev-1\n", "平台: linux/amd64\n", "角色: agent、gateway\n",
		"网络身份: 模式 tailnet，节点 dev-1，标签 tag:device\n",
		"  ida（exclusive，running，常用）\n",
		"exec 共享槽 1/4，独占槽 0/1", "终端 2/20（active 1，closed 3/20），单个调用方上限 8",
		"需要重启: 是\n", "警告: 节点、网络或监听配置已变更，需要重启\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q，实际输出:\n%s", want, got)
		}
	}
	if strings.Contains(got, "map[") {
		t.Errorf("输出不应含 Go map 格式:\n%s", got)
	}
}

func Test_CLIStatusToleratesMissingFields(t *testing.T) {
	got := formatStatus(map[string]any{"hostname": "bare"})
	if !strings.Contains(got, "后端: 无") || !strings.Contains(got, "角色: 无") || strings.Contains(got, "map[") || strings.Contains(got, "<nil>") || !strings.Contains(got, "平台: ?/?") {
		t.Fatalf("缺字段时输出异常:\n%s", got)
	}
}
