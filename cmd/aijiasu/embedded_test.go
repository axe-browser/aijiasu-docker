package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeDocker struct {
	worker                   embeddedWorker
	exists, running, foreign bool
	calls                    [][]string
	inputs                   []string
	login                    string
	fail                     string
	node                     string
	actualNode               string
}

func embeddedFixture(t *testing.T, index int) (*embeddedWorker, *fakeDocker) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := embeddedRequest{Protocol: embeddedProtocol, Action: "start", DataDir: filepath.Join(root, "instance"),
		InstanceID: "axe-aijiasu-123456789abc-" + string(rune('0'+index)), ProxyPort: 21080 + index,
		DockerPath: "/fixture/docker", Username: "fixture-user", Password: "fixture-secret", InstanceIndex: index}
	fake := &fakeDocker{login: "Login Result: Success\nLogin Message: OK"}
	worker := &embeddedWorker{request: request, run: fake.run, probe: func(string, int, time.Duration) error { return nil }}
	fake.worker = *worker
	return worker, fake
}

func (fake *fakeDocker) run(ctx context.Context, path, directory string, args []string, input string) (string, error) {
	fake.calls = append(fake.calls, append([]string(nil), args...))
	fake.inputs = append(fake.inputs, input)
	if _, ok := ctx.Deadline(); !ok {
		return "", errors.New("missing deadline")
	}
	joined := strings.Join(args, " ")
	if fake.fail != "" && strings.Contains(joined, fake.fail) {
		return "", errors.New("fixture-secret")
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "ls" {
		if fake.exists {
			return fake.worker.request.InstanceID + "\n", nil
		}
		return "", nil
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "inspect" {
		id := fake.worker.request.InstanceID
		labels := map[string]string{embeddedOwnerLabel: id, embeddedDirectoryLabel: fake.worker.directoryLabel(), "com.docker.compose.project": id}
		if fake.foreign {
			labels[embeddedOwnerLabel] = "someone-else"
		}
		result := []map[string]any{{"Name": "/" + id, "State": map[string]any{"Running": fake.running},
			"Config":     map[string]any{"Labels": labels, "Env": []string{"PASSWORD=fixture-secret"}},
			"HostConfig": map[string]any{"PortBindings": map[string]any{"1080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": fmtPort(fake.worker.request.ProxyPort)}}}}}}
		content, _ := json.Marshal(result)
		return string(content), nil
	}
	if strings.Contains(joined, "up --detach --no-build proxy") {
		fake.exists, fake.running = true, true
		return "", nil
	}
	if strings.HasPrefix(joined, "container stop ") {
		fake.running = false
		return "", nil
	}
	if strings.HasSuffix(joined, "ajiasu login") {
		return fake.login, nil
	}
	if strings.HasSuffix(joined, "ajiasu list") {
		return "vvn-001 ok 广东广州 #01\nvvn-002 ok 广东深圳 #02\n", nil
	}
	if strings.Contains(joined, "nohup ajiasu connect") {
		fake.node = args[len(args)-1]
		return "", nil
	}
	if strings.HasSuffix(joined, "ss -H -lntp") {
		return "LISTEN 0 128 127.0.0.1:18888 0.0.0.0:* users:((\"ajiasu\",pid=50,fd=9))", nil
	}
	if strings.HasSuffix(joined, "ps -ww -p 50 -o args=") {
		if fake.actualNode != "" {
			return "ajiasu connect " + fake.actualNode, nil
		}
		return "ajiasu connect " + fake.node, nil
	}
	if strings.Contains(joined, "https://api.ipify.org") {
		return "203.0.113.10", nil
	}
	return "", nil
}

func fmtPort(port int) string {
	content, _ := json.Marshal(port)
	return string(content)
}

func TestEmbeddedStartKeepsCredentialsInPipeAndOwnsIndependentRuntime(t *testing.T) {
	worker, fake := embeddedFixture(t, 2)
	result, err := worker.execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "running" || result.NodeID != "vvn-002" || result.ExitIP != "203.0.113.10" || result.Proxy != "socks5://127.0.0.1:21082" {
		t.Fatalf("unexpected result: %+v", result)
	}
	credentials := 0
	for index, args := range fake.calls {
		if strings.Contains(strings.Join(args, " "), "fixture-") {
			t.Fatal("credential in command arguments")
		}
		if strings.Contains(fake.inputs[index], "fixture-secret") {
			credentials++
			if !strings.Contains(strings.Join(args, " "), "chmod 600 /etc/ajiasu.conf") {
				t.Fatal("credential command mode")
			}
		}
	}
	if credentials != 1 {
		t.Fatalf("credential sends: %d", credentials)
	}
	err = filepath.Walk(worker.request.DataDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(content), "fixture-secret") || strings.Contains(string(content), "fixture-user") {
				t.Fatal("credential written to host")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worker.request.DataDir, ".env")); !os.IsNotExist(err) {
		t.Fatal("host .env created")
	}
	var compose map[string]any
	content, _ := os.ReadFile(filepath.Join(worker.request.DataDir, "compose.json"))
	if json.Unmarshal(content, &compose) != nil || strings.Contains(string(content), "env_file") {
		t.Fatal("unexpected compose environment")
	}
	if !strings.Contains(string(content), "127.0.0.1:21082:1080") {
		t.Fatal("non-loopback port")
	}
	if !strings.Contains(string(content), `"entrypoint":["sh","-c"`) {
		t.Fatal("managed container must override legacy automatic login entrypoint")
	}
	worker.request.Action = "status"
	fake.calls = nil
	result, err = worker.execute(context.Background())
	if err != nil || result.ExitIP != "203.0.113.10" || result.NodeID != "vvn-002" {
		t.Fatalf("status lost known connection: %+v %v", result, err)
	}
	for _, args := range fake.calls {
		if strings.Contains(strings.Join(args, " "), "ajiasu login") || strings.Contains(strings.Join(args, " "), "compose") {
			t.Fatal("status initiated a connection")
		}
	}
}

func TestEmbeddedRequestsAreStrictAndProduceOneJSONResponse(t *testing.T) {
	worker, _ := embeddedFixture(t, 1)
	content, _ := json.Marshal(worker.request)
	request, err := decodeEmbedded(bytes.NewReader(content))
	if err != nil || request.InstanceID != worker.request.InstanceID {
		t.Fatal(err)
	}
	invalid := []string{`{}`, `[]`, `{"protocol":"aijiasu-stdio-v1","protocol":"bad"}`, string(content) + `{}`, strings.Repeat("x", embeddedMaxInput+1)}
	for _, value := range invalid {
		var output bytes.Buffer
		embeddedMain(strings.NewReader(value), &output)
		var response embeddedResponse
		if json.Unmarshal(output.Bytes(), &response) != nil || response.OK || response.Protocol != embeddedProtocol || response.Error == "" || strings.Count(output.String(), "\n") != 1 {
			t.Fatalf("bad response: %q", output.String())
		}
	}
	for _, change := range []func(*embeddedRequest){
		func(r *embeddedRequest) { r.InstanceIndex = 20 }, func(r *embeddedRequest) { r.ProxyPort = 80 },
		func(r *embeddedRequest) { r.DataDir = "relative" }, func(r *embeddedRequest) { r.Password = "bad\nvalue" },
		func(r *embeddedRequest) { r.InstanceID = "../unsafe" }, func(r *embeddedRequest) { r.DockerPath = "docker" },
		func(r *embeddedRequest) { r.Action = "exec" }, func(r *embeddedRequest) { r.DataDir = "/" },
	} {
		value := worker.request
		change(&value)
		if validateEmbedded(value) == nil {
			t.Fatalf("accepted invalid input: action=%s", value.Action)
		}
	}
	worker.request.Action = "status"
	content, _ = json.Marshal(worker.request)
	if _, err := decodeEmbedded(bytes.NewReader(content)); err == nil {
		t.Fatal("credentials accepted on status")
	}
}

func TestEmbeddedStatusStopAbsentAreReadOnly(t *testing.T) {
	for _, action := range []string{"status", "stop"} {
		worker, fake := embeddedFixture(t, 1)
		worker.request.Action = action
		result, err := worker.execute(context.Background())
		if err != nil || result.Status != "stopped" || len(fake.calls) != 0 {
			t.Fatalf("unexpected absent %s: %+v %v", action, result, err)
		}
		if _, err := os.Stat(worker.request.DataDir); !os.IsNotExist(err) {
			t.Fatal("query created directory")
		}
	}
}

func TestEmbeddedStatusDoesNotHealOrRestartAndStopOnlyTargetsOwnedContainer(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	if err := worker.prepare(); err != nil {
		t.Fatal(err)
	}
	fake.exists, fake.running = true, true
	worker.request.Action = "status"
	worker.probe = func(string, int, time.Duration) error { return errors.New("not connected") }
	result, err := worker.execute(context.Background())
	if err != nil || result.Status != "error" {
		t.Fatalf("unexpected status: %+v %v", result, err)
	}
	for _, args := range fake.calls {
		if args[0] != "container" || (args[1] != "ls" && args[1] != "inspect") {
			t.Fatal("status mutated container")
		}
	}
	fake.calls = nil
	worker.request.Action = "stop"
	result, err = worker.execute(context.Background())
	if err != nil || result.Status != "stopped" || fake.running {
		t.Fatalf("stop failed: %+v %v", result, err)
	}
	for _, args := range fake.calls {
		if strings.Contains(strings.Join(args, " "), "down") || strings.Contains(strings.Join(args, " "), " rm ") {
			t.Fatal("deleted Docker resources")
		}
	}
	if _, err := os.Stat(filepath.Join(worker.request.DataDir, ".instance.json")); err != nil {
		t.Fatal("stop deleted data")
	}
}

func TestEmbeddedForeignContainerIsNeverTakenOver(t *testing.T) {
	for _, action := range []string{"start", "status", "stop"} {
		worker, fake := embeddedFixture(t, 1)
		if err := worker.prepare(); err != nil {
			t.Fatal(err)
		}
		fake.exists, fake.running, fake.foreign = true, true, true
		worker.request.Action = action
		if _, err := worker.execute(context.Background()); err == nil || !strings.Contains(err.Error(), "拒绝") {
			t.Fatal("foreign container accepted")
		}
		if !fake.running {
			t.Fatal("foreign container stopped")
		}
		for _, args := range fake.calls {
			if args[0] != "container" || (args[1] != "ls" && args[1] != "inspect") {
				t.Fatal("foreign container mutated")
			}
		}
	}
}

func TestEmbeddedFailedLoginStopsOwnedContainerAndDoesNotLeak(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	fake.login = "Login Result: WrongPassword\nLogin Message: fixture-secret"
	_, err := worker.execute(context.Background())
	if err == nil || !strings.Contains(err.Error(), "登录失败") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("unsafe error")
	}
	if fake.running {
		t.Fatal("failed start left container running")
	}
}

func TestEmbeddedDesktopCancellationLeavesProxyContainerRunning(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.run = func(ctx context.Context, path, directory string, args []string, input string) (string, error) {
		if strings.HasSuffix(strings.Join(args, " "), "ajiasu login") {
			cancel()
			return "", ctx.Err()
		}
		return fake.run(ctx, path, directory, args, input)
	}
	if _, err := worker.execute(ctx); err == nil {
		t.Fatal("cancelled start succeeded")
	}
	if !fake.running {
		t.Fatal("closing desktop stopped proxy container")
	}
}

func TestEmbeddedUnhealthyProxyCannotBecomeRunning(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	worker.probe = func(string, int, time.Duration) error { return errors.New("fixture-secret") }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := worker.execute(ctx); err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("proxy health failure not handled")
	}
	if fake.running {
		t.Fatal("failed health check left managed start running")
	}
}

func TestEmbeddedInsufficientNodesDoesNotSilentlyShareFirstNode(t *testing.T) {
	worker, fake := embeddedFixture(t, 3)
	_, err := worker.execute(context.Background())
	if err == nil || !strings.Contains(err.Error(), "节点不足") || fake.running {
		t.Fatal("insufficient nodes not handled")
	}
}

func TestEmbeddedRejectsUnownedDirectoriesTamperedAssetsAndSymlinks(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	if err := os.MkdirAll(worker.request.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worker.request.DataDir, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.execute(context.Background()); err == nil {
		t.Fatal("accepted unowned directory")
	}
	if len(fake.calls) != 0 {
		t.Fatal("ran Docker for unowned directory")
	}
	worker, fake = embeddedFixture(t, 1)
	if err := worker.prepare(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worker.request.DataDir, "Dockerfile"), []byte("custom"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.execute(context.Background()); err == nil {
		t.Fatal("accepted modified runtime assets")
	}
	if len(fake.calls) != 0 {
		t.Fatal("ran Docker for modified runtime")
	}
	worker, _ = embeddedFixture(t, 1)
	if err := os.Symlink(filepath.Dir(worker.request.DataDir), worker.request.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := worker.prepare(); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestEmbeddedBusyLockPreventsParallelMutation(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	if err := worker.prepare(); err != nil {
		t.Fatal(err)
	}
	lock, err := worker.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := worker.execute(context.Background()); err == nil || !strings.Contains(err.Error(), "正在操作") {
		t.Fatal("parallel mutation accepted")
	}
	if len(fake.calls) != 0 {
		t.Fatal("Docker command escaped operation lock")
	}
}

func TestEmbeddedCommandEnvironmentAndTimeoutUseFakeExecutableOnly(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "fake-docker")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s|%s|%s' \"$DOCKER_HOST\" \"${DOCKER_CONTEXT-unset}\" \"${AIJIASU_PASS-unset}\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix:///fixture/docker.sock")
	t.Setenv("DOCKER_CONTEXT", "remote-context")
	t.Setenv("AIJIASU_PASS", "fixture-secret")
	output, err := runEmbeddedDocker(context.Background(), executable, directory, nil, "")
	if err != nil || output != "unix:///fixture/docker.sock|unset|unset" {
		t.Fatalf("unsafe environment: %q %v", output, err)
	}
	for _, host := range []string{"", "tcp://example.com:2375", "ssh://example.com", "unix://host/path"} {
		t.Setenv("DOCKER_HOST", host)
		if _, err := runEmbeddedDocker(context.Background(), executable, directory, nil, ""); err == nil {
			t.Fatal("remote Docker accepted")
		}
	}
	t.Setenv("DOCKER_HOST", "unix:///fixture/docker.sock")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if _, err := runEmbeddedDocker(ctx, executable, directory, nil, ""); err == nil {
		t.Fatal("timeout ignored")
	}
	if time.Since(begin) > time.Second {
		t.Fatal("timeout did not terminate fake process")
	}
}

func TestEmbeddedNodeParserDiscardsMalformedIDsAndDuplicates(t *testing.T) {
	value := embeddedNodes("bad;command ok Test\nvvn-1 ok 广东\nvvn-1 ok 重复\nvvn-2 offline 广东\nvvn-3 ok 上海")
	if !reflect.DeepEqual(value, []string{"vvn-1", "vvn-3"}) {
		t.Fatalf("bad nodes: %+v", value)
	}
	empty := embeddedNodeItems("")
	content, err := json.Marshal(embeddedResponse{Protocol: embeddedProtocol, OK: true, Nodes: &empty})
	if err != nil || !strings.Contains(string(content), `"nodes":[]`) {
		t.Fatalf("empty node list must be an array: %s %v", content, err)
	}
}

func TestEmbeddedExplicitStartNodeAndExactSwitch(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	worker.request.NodeID = "vvn-002"
	result, err := worker.execute(context.Background())
	if err != nil || result.Status != "running" || result.NodeID != "vvn-002" || fake.node != "vvn-002" {
		t.Fatalf("explicit start failed: %+v %v", result, err)
	}

	worker.request.Action = "switch"
	worker.request.Username, worker.request.Password = "", ""
	worker.request.NodeID = "vvn-001"
	fake.calls = nil
	result, err = worker.execute(context.Background())
	if err != nil || result.Status != "running" || result.NodeID != "vvn-001" || fake.node != "vvn-001" {
		t.Fatalf("exact switch failed: %+v %v", result, err)
	}
	for _, args := range fake.calls {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "compose") || strings.Contains(joined, "ajiasu login") {
			t.Fatalf("switch restarted or logged into service: %q", joined)
		}
	}

	worker.request.NodeID = "vvn-00"
	fake.calls = nil
	if _, err := worker.execute(context.Background()); err == nil || !strings.Contains(err.Error(), "不在当前可用列表") {
		t.Fatalf("partial node ID accepted: %v", err)
	}
	for _, args := range fake.calls {
		if strings.Contains(strings.Join(args, " "), "disconnect") {
			t.Fatal("invalid node disconnected current proxy")
		}
	}
	if fake.node != "vvn-001" {
		t.Fatal("invalid switch changed the current node")
	}
}

func TestEmbeddedNodesAndStatusUseLiveNode(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	if err := worker.prepare(); err != nil {
		t.Fatal(err)
	}
	fake.exists, fake.running, fake.node = true, true, "vvn-002"
	if err := worker.saveConnection("vvn-001"); err != nil {
		t.Fatal(err)
	}

	worker.request.Action = "nodes"
	worker.request.Username, worker.request.Password = "", ""
	result, nodes, err := worker.executeWithNodes(context.Background())
	if err != nil || result.Status != "running" || result.NodeID != "vvn-002" ||
		len(nodes) != 2 || nodes[0].ID != "vvn-001" || nodes[1].Name != "广东深圳 #02" {
		t.Fatalf("nodes response or live node invalid: %+v %+v %v", result, nodes, err)
	}
	response := embeddedResponse{Protocol: embeddedProtocol, OK: true, Instance: result, Nodes: &nodes}
	content, err := json.Marshal(response)
	if err != nil || !strings.Contains(string(content), `"nodes":[{"id":"vvn-001"`) {
		t.Fatalf("nodes protocol invalid: %s %v", content, err)
	}
	for _, args := range fake.calls {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "compose") || strings.Contains(joined, "ajiasu login") ||
			strings.Contains(joined, "disconnect") {
			t.Fatalf("nodes action mutated service: %q", joined)
		}
	}

	fake.node = ""
	worker.request.Action = "status"
	result, err = worker.execute(context.Background())
	if err != nil || result.Status != "error" || result.NodeID != "" {
		t.Fatalf("unverifiable live node accepted: %+v %v", result, err)
	}
}

func TestEmbeddedNodeActionsRejectStoppedAndInvalidRequests(t *testing.T) {
	for _, action := range []string{"nodes", "switch"} {
		worker, fake := embeddedFixture(t, 1)
		worker.request.Action = action
		worker.request.Username, worker.request.Password = "", ""
		if action == "switch" {
			worker.request.NodeID = "vvn-001"
		}
		if _, _, err := worker.executeWithNodes(context.Background()); err == nil || !strings.Contains(err.Error(), "尚未启动") {
			t.Fatalf("%s accepted stopped service: %v", action, err)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("%s ran Docker for absent instance", action)
		}
	}

	worker, _ := embeddedFixture(t, 1)
	for _, change := range []func(*embeddedRequest){
		func(r *embeddedRequest) { r.Action, r.NodeID = "switch", "" },
		func(r *embeddedRequest) { r.Action, r.NodeID = "switch", "vvn-1;bad" },
		func(r *embeddedRequest) { r.Action, r.NodeID = "nodes", "vvn-001" },
	} {
		request := worker.request
		change(&request)
		if validateEmbedded(request) == nil {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
}

func TestEmbeddedConnectionRejectsUnexpectedLiveNode(t *testing.T) {
	worker, fake := embeddedFixture(t, 1)
	fake.actualNode = "vvn-002"
	if _, err := worker.execute(context.Background()); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("unexpected node accepted: %v", err)
	}
	if fake.running {
		t.Fatal("failed start left mismatched proxy running")
	}
}
