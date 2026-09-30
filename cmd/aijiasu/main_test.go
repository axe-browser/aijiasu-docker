package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseNodeLocation(t *testing.T) {
	tests := []struct {
		name         string
		expectedProv string
		expectedCity string
	}{
		{"上海 #116", "上海", "上海"},
		{"北京 #01", "北京", "北京"},
		{"广东广州 #12", "广东", "广州"},
		{"广东 深圳电信 #03", "广东", "深圳"},
		{"枣庄#1", "山东", "枣庄"},
		{"江苏南京移动 #05", "江苏", "南京"},
		{"杭州联通 #2", "浙江", "杭州"},
		{"成都 #08", "四川", "成都"},
	}

	for _, tc := range tests {
		prov, city := parseNodeLocation(tc.name)
		if prov != tc.expectedProv || city != tc.expectedCity {
			t.Errorf("parseNodeLocation(%q) = (%q, %q); expected (%q, %q)",
				tc.name, prov, city, tc.expectedProv, tc.expectedCity)
		}
	}
}

func TestExtractNodeNumber(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"上海 #116", "116"},
		{"北京 #01", "01"},
		{"广东广州 #12", "12"},
		{"山东枣庄电信 #3", "3"},
		{"深圳 08", "08"},
		{"上海普通节点", ""},
	}

	for _, tc := range tests {
		num := extractNodeNumber(tc.name)
		if num != tc.expected {
			t.Errorf("extractNodeNumber(%q) = %q; expected %q", tc.name, num, tc.expected)
		}
	}
}

func TestDedupAlgorithm(t *testing.T) {
	// 使用临时测试历史文件
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	testNodes := []NodeItem{
		{ID: "node-1", Name: "广东广州 #01", Province: "广东", City: "广州"},
		{ID: "node-2", Name: "广东广州 #02", Province: "广东", City: "广州"},
		{ID: "node-3", Name: "广东广州 #03", Province: "广东", City: "广州"},
	}

	// 第一次挑选：历史为空，从未用 3 个中选 1 个
	hist := loadHistory()
	if len(hist.UsedNodeIDs) != 0 {
		t.Fatalf("expected empty history, got %v", hist.UsedNodeIDs)
	}

	usedSet := make(map[string]bool)
	for i := 0; i < len(testNodes); i++ {
		// 每次挑一个
		var unused []NodeItem
		h := loadHistory()
		for _, n := range testNodes {
			used := false
			for _, uid := range h.UsedNodeIDs {
				if uid == n.ID {
					used = true
					break
				}
			}
			if !used {
				unused = append(unused, n)
			}
		}

		if len(unused) != len(testNodes)-i {
			t.Fatalf("step %d: expected %d unused nodes, got %d", i, len(testNodes)-i, len(unused))
		}

		chosen := unused[0]
		if usedSet[chosen.ID] {
			t.Fatalf("node %s was picked repeatedly before pool exhaustion", chosen.ID)
		}
		usedSet[chosen.ID] = true

		h.UsedNodeIDs = append(h.UsedNodeIDs, chosen.ID)
		h.LastNodeID = chosen.ID
		saveHistory(h)
	}

	// 此时 3 个节点均已使用过一轮
	hAfter := loadHistory()
	if len(hAfter.UsedNodeIDs) != 3 {
		t.Fatalf("expected 3 used nodes, got %d", len(hAfter.UsedNodeIDs))
	}

	// 验证重置历史功能
	resetHistory()
	hReset := loadHistory()
	if len(hReset.UsedNodeIDs) != 0 {
		t.Fatalf("expected 0 used nodes after reset, got %d", len(hReset.UsedNodeIDs))
	}
}

func TestDynamicEnvConfig(t *testing.T) {
	// 1. 默认值验证
	os.Unsetenv("AIJIASU_PROXY_PORT")
	os.Unsetenv("AIJIASU_BIND_IP")
	os.Unsetenv("AIJIASU_HTTP_PORT")
	os.Unsetenv("AIJIASU_CONTAINER_NAME")

	if p := getProxyPort(); p != 1080 {
		t.Fatalf("expected default proxy port 1080, got %d", p)
	}
	if h := getProxyHost(); h != "127.0.0.1" {
		t.Fatalf("expected default proxy host 127.0.0.1, got %s", h)
	}
	if hp := getHTTPPort(); hp != 1081 {
		t.Fatalf("expected default http port 1081, got %d", hp)
	}
	if c := getContainerName(); c != "aijiasu-runner" {
		t.Fatalf("expected default container name aijiasu-runner, got %s", c)
	}

	// 2. 自定义环境变量覆盖验证
	os.Setenv("AIJIASU_PROXY_PORT", "1090")
	defer os.Unsetenv("AIJIASU_PROXY_PORT")
	if p := getProxyPort(); p != 1090 {
		t.Fatalf("expected overridden proxy port 1090, got %d", p)
	}

	os.Setenv("AIJIASU_HTTP_PORT", "8888")
	defer os.Unsetenv("AIJIASU_HTTP_PORT")
	if hp := getHTTPPort(); hp != 8888 {
		t.Fatalf("expected overridden http port 8888, got %d", hp)
	}

	os.Setenv("AIJIASU_CONTAINER_NAME", "my-aijiasu-instance")
	defer os.Unsetenv("AIJIASU_CONTAINER_NAME")
	if c := getContainerName(); c != "my-aijiasu-instance" {
		t.Fatalf("expected overridden container name my-aijiasu-instance, got %s", c)
	}

	// 3. 0.0.0.0 绑定模式下本地回环自测 Host 转换验证
	os.Setenv("AIJIASU_BIND_IP", "0.0.0.0")
	defer os.Unsetenv("AIJIASU_BIND_IP")
	if h := getProxyHost(); h != "127.0.0.1" {
		t.Fatalf("expected 0.0.0.0 bind IP to connect via 127.0.0.1 locally, got %s", h)
	}

	// 4. 具体指定 IP 验证
	os.Setenv("AIJIASU_BIND_IP", "192.168.1.188")
	if h := getProxyHost(); h != "192.168.1.188" {
		t.Fatalf("expected specific bind IP 192.168.1.188, got %s", h)
	}
}

func TestBootstrapAssets(t *testing.T) {
	// 在独立空临时目录中测试自举初始化
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// 1. 执行自举
	if err := ensureBootstrapAssets(); err != nil {
		t.Fatalf("ensureBootstrapAssets failed: %v", err)
	}

	// 2. 检查必要文件是否存在
	files := []string{
		"docker-compose.yml",
		"Dockerfile",
		"entrypoint.sh",
		"conf/localtime",
		".env.example",
	}

	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatalf("expected bootstrapped file %s to exist, err: %v", f, err)
		}
		if info.Size() == 0 {
			t.Fatalf("bootstrapped file %s is empty", f)
		}
		if f == "entrypoint.sh" {
			// 验证可执行权限
			if info.Mode()&0111 == 0 {
				t.Fatalf("entrypoint.sh must be executable, got mode: %v", info.Mode())
			}
		}
	}

	// 3. 验证幂等性：再次调用不应报错且不覆盖自定义修改
	customCompose := "# custom compose file"
	_ = os.WriteFile("docker-compose.yml", []byte(customCompose), 0644)

	if err := ensureBootstrapAssets(); err != nil {
		t.Fatalf("idempotent ensureBootstrapAssets failed: %v", err)
	}

	content, _ := os.ReadFile("docker-compose.yml")
	if string(content) != customCompose {
		t.Fatalf("ensureBootstrapAssets should not overwrite existing files")
	}
}

func TestBootstrapAssetsReportsWriteFailure(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// conf 应是目录；同名普通文件会阻止写入 conf/localtime。
	if err := os.WriteFile("conf", []byte("occupied"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker-compose.yml", "Dockerfile", "entrypoint.sh"} {
		if err := os.WriteFile(name, []byte("existing"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureBootstrapAssets(); err == nil || !strings.Contains(err.Error(), filepath.Join("conf", "localtime")) {
		t.Fatalf("expected an error naming conf/localtime, got %v", err)
	}
}

func TestForwardComposeStopsOnBootstrapFailure(t *testing.T) {
	if os.Getenv("AIJIASU_BOOTSTRAP_TEST_CHILD") == "1" {
		forwardCompose(os.Getenv("AIJIASU_BOOTSTRAP_TEST_ACTION"))
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake Docker executable is a shell script")
	}

	for _, action := range []string{"up", "restart", "build"} {
		t.Run(action, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmpDir, "conf"), []byte("occupied"), 0644); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"docker-compose.yml", "Dockerfile", "entrypoint.sh"} {
				if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("existing"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			fakeBin := filepath.Join(tmpDir, "bin")
			if err := os.Mkdir(fakeBin, 0755); err != nil {
				t.Fatal(err)
			}
			fakeDocker := filepath.Join(fakeBin, "docker")
			if err := os.WriteFile(fakeDocker, []byte("#!/bin/sh\ntouch \"$AIJIASU_BOOTSTRAP_TEST_MARKER\"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(tmpDir, "docker-called")
			cmd := exec.Command(os.Args[0], "-test.run=^TestForwardComposeStopsOnBootstrapFailure$")
			cmd.Dir = tmpDir
			cmd.Env = append(os.Environ(),
				"AIJIASU_BOOTSTRAP_TEST_CHILD=1",
				"AIJIASU_BOOTSTRAP_TEST_ACTION="+action,
				"AIJIASU_BOOTSTRAP_TEST_MARKER="+marker,
				"AIJIASU_ASSUME_YES=1",
				"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatalf("expected exit status 1, got %v\n%s", err, output)
			}
			if !strings.Contains(string(output), filepath.Join("conf", "localtime")) {
				t.Fatalf("expected failing asset path in output, got:\n%s", output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("Docker must not run after bootstrap failure, marker stat: %v", err)
			}
		})
	}
}
