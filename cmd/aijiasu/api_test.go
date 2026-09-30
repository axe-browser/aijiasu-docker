package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebAPIResponses(t *testing.T) {
	mux := http.NewServeMux()

	// 1. 节点列表模拟
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		mockNodes := []NodeItem{
			{ID: "vvn-1001", Province: "上海", City: "上海", Number: "116", Name: "上海 #116", Status: "ok"},
			{ID: "vvn-1002", Province: "广东", City: "广州", Number: "01", Name: "广东广州 #01", Status: "ok"},
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"total":   len(mockNodes),
			"nodes":   mockNodes,
		})
	})

	// 2. 切换接口模拟 (成功 msg="ok" 并携带 ip；失败返回真实错误且 ip="")
	mux.HandleFunc("/switch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		q := r.URL.Query()
		if q.Get("fail") == "true" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"msg":     "连接超时：未在 6 秒内检测到监听端口",
				"ip":      "",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"msg":     "ok",
			"ip":      "113.108.88.25",
		})
	})

	// 3. 中断接口模拟 (成功 msg="ok")
	mux.HandleFunc("/disconnect", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"msg":     "ok",
		})
	})

	// 4. 登录接口模拟 (成功 msg="ok"，失败返回真实错误)
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var bodyMap map[string]string
		_ = json.NewDecoder(r.Body).Decode(&bodyMap)
		if bodyMap["username"] == "correct" && bodyMap["password"] == "123456" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"msg":     "ok",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"msg":     "错误的用户名或密码。",
		})
	})

	// 验证 1: 节点列表 JSON
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("GET", "/nodes", nil)
	mux.ServeHTTP(rec1, req1)

	var nodeResp struct {
		Success bool       `json:"success"`
		Total   int        `json:"total"`
		Nodes   []NodeItem `json:"nodes"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &nodeResp); err != nil {
		t.Fatalf("unmarshal /nodes response error: %v", err)
	}
	if !nodeResp.Success || nodeResp.Total != 2 {
		t.Fatalf("unexpected /nodes response: %+v", nodeResp)
	}
	if nodeResp.Nodes[0].Number != "116" || nodeResp.Nodes[0].Province != "上海" {
		t.Fatalf("unexpected node item fields: %+v", nodeResp.Nodes[0])
	}

	// 验证 2: 切换成功格式 (msg="ok", ip="113.108.88.25")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/switch", strings.NewReader(`{"id":"vvn-1002"}`))
	mux.ServeHTTP(rec2, req2)

	var switchSuccess struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		IP      string `json:"ip"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &switchSuccess); err != nil {
		t.Fatalf("unmarshal /switch response error: %v", err)
	}
	if !switchSuccess.Success || switchSuccess.Msg != "ok" || switchSuccess.IP != "113.108.88.25" {
		t.Fatalf("unexpected switch success format: %+v", switchSuccess)
	}

	// 验证 3: 切换失败格式 (msg 真实错误，无前缀)
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/switch?fail=true", nil)
	mux.ServeHTTP(rec3, req3)

	var switchFail struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		IP      string `json:"ip"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &switchFail); err != nil {
		t.Fatalf("unmarshal /switch fail response error: %v", err)
	}
	if switchFail.Success || switchFail.Msg != "连接超时：未在 6 秒内检测到监听端口" || switchFail.IP != "" {
		t.Fatalf("unexpected switch fail format: %+v", switchFail)
	}

	// 验证 4: 中断接口格式 (msg="ok")
	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest("POST", "/disconnect", nil)
	mux.ServeHTTP(rec4, req4)

	var discResp struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(rec4.Body.Bytes(), &discResp); err != nil {
		t.Fatalf("unmarshal /disconnect response error: %v", err)
	}
	if !discResp.Success || discResp.Msg != "ok" {
		t.Fatalf("unexpected disconnect response: %+v", discResp)
	}

	// 验证 5: 登录成功响应格式 (msg="ok")
	rec5 := httptest.NewRecorder()
	req5 := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"correct","password":"123456"}`))
	mux.ServeHTTP(rec5, req5)

	var loginSuccess struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(rec5.Body.Bytes(), &loginSuccess); err != nil {
		t.Fatalf("unmarshal /login response error: %v", err)
	}
	if !loginSuccess.Success || loginSuccess.Msg != "ok" {
		t.Fatalf("unexpected login success format: %+v", loginSuccess)
	}

	// 验证 6: 登录失败响应格式 (msg 真实错误无前缀)
	rec6 := httptest.NewRecorder()
	req6 := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"wrong","password":"bad"}`))
	mux.ServeHTTP(rec6, req6)

	var loginFail struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(rec6.Body.Bytes(), &loginFail); err != nil {
		t.Fatalf("unmarshal /login fail response error: %v", err)
	}
	if loginFail.Success || loginFail.Msg != "错误的用户名或密码。" {
		t.Fatalf("unexpected login fail format: %+v", loginFail)
	}
}
