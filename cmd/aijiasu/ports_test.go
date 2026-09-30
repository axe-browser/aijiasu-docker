package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	tests := []struct {
		s1   string
		s2   string
		want bool
	}{
		{"北京 #2", "北京 #10", true},
		{"北京 #10", "北京 #2", false},
		{"上海 01", "上海 02", true},
		{"深圳 #100", "深圳 #99", false},
		{"深圳 #99", "深圳 #100", true},
		{"node-1", "node-2", true},
		{"abc", "abc", false},
		{"abc", "abd", true},
	}

	for _, tt := range tests {
		got := naturalLess(tt.s1, tt.s2)
		if got != tt.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tt.s1, tt.s2, got, tt.want)
		}
	}
}

func TestGetProvinceSortOrder(t *testing.T) {
	beijingOrder := getProvinceSortOrder("北京市")
	shanghaiOrder := getProvinceSortOrder("上海市")
	unknownOrder := getProvinceSortOrder("未知省份XYZ")

	if beijingOrder != 0 {
		t.Errorf("beijingOrder = %d, want 0", beijingOrder)
	}
	if shanghaiOrder != 1 {
		t.Errorf("shanghaiOrder = %d, want 1", shanghaiOrder)
	}
	if unknownOrder != 999 {
		t.Errorf("unknownOrder = %d, want 999", unknownOrder)
	}
	if beijingOrder >= unknownOrder {
		t.Errorf("expected beijingOrder < unknownOrder")
	}
}

func TestGenerateAllNodeMappings(t *testing.T) {
	mockNodes := []NodeItem{
		{ID: "node-bj-2", Name: "北京 #2", City: "北京"},
		{ID: "node-bj-10", Name: "北京 #10", City: "北京"},
		{ID: "node-bj-1", Name: "北京 #1", City: "北京"},
		{ID: "node-sh-1", Name: "上海 #1", City: "上海"},
		{ID: "node-gz-1", Name: "广州 #1", City: "广州"},
	}

	basePort := 2000
	mappings := generateAllNodeMappings(mockNodes, basePort)

	if len(mappings) != len(mockNodes) {
		t.Fatalf("mappings len = %d, want %d", len(mappings), len(mockNodes))
	}

	// 端口必须连续且自 basePort 递增
	seenPorts := make(map[int]bool)
	for i, item := range mappings {
		expectedPort := basePort + i
		if item.Port != expectedPort {
			t.Errorf("mapping[%d] port = %d, want %d", i, item.Port, expectedPort)
		}
		if seenPorts[item.Port] {
			t.Errorf("duplicate port detected: %d", item.Port)
		}
		seenPorts[item.Port] = true
	}

	// 验证北京节点的自然排序：北京 #1 应在 北京 #2 前面，北京 #2 在 北京 #10 前面
	var beijingNames []string
	for _, m := range mappings {
		if m.City == "北京" {
			beijingNames = append(beijingNames, m.NodeName)
		}
	}
	if len(beijingNames) == 3 {
		if beijingNames[0] != "北京 #1" || beijingNames[1] != "北京 #2" || beijingNames[2] != "北京 #10" {
			t.Errorf("unexpected Beijing sort order: %v", beijingNames)
		}
	} else {
		t.Errorf("expected 3 Beijing nodes, got %d", len(beijingNames))
	}
}

func TestPortConfigJSONSerialization(t *testing.T) {
	origCfg := &PortConfig{
		Version:       "2.0",
		BasePort:      1080,
		BackendPort:   1079,
		TotalNodes:    2,
		AllowFailover: true,
		Mappings: []PortItem{
			{Port: 1080, NodeID: "n1", NodeName: "北京 #1", Province: "北京市", City: "北京", Enabled: true},
			{Port: 1081, NodeID: "n2", NodeName: "上海 #1", Province: "上海市", City: "上海", Enabled: true},
		},
	}

	data, err := json.Marshal(origCfg)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsedCfg PortConfig
	if err := json.Unmarshal(data, &parsedCfg); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsedCfg.Version != origCfg.Version ||
		parsedCfg.BasePort != origCfg.BasePort ||
		parsedCfg.BackendPort != origCfg.BackendPort ||
		parsedCfg.TotalNodes != origCfg.TotalNodes ||
		parsedCfg.AllowFailover != origCfg.AllowFailover ||
		len(parsedCfg.Mappings) != len(origCfg.Mappings) {
		t.Errorf("deserialized config mismatch: got %+v, want %+v", parsedCfg, origCfg)
	}
}

func TestPortFilterLogic(t *testing.T) {
	cfg := &PortConfig{
		TotalNodes: 3,
		Mappings: []PortItem{
			{Port: 1080, NodeID: "vvn-bj-1", NodeName: "北京 01", Province: "北京市", City: "北京", Enabled: true},
			{Port: 1081, NodeID: "vvn-sh-1", NodeName: "上海 01", Province: "上海市", City: "上海", Enabled: true},
			{Port: 1082, NodeID: "vvn-gz-1", NodeName: "广州 01", Province: "广东省", City: "广州", Enabled: true},
		},
	}

	filter := func(keyword string) []PortItem {
		kw := strings.ToLower(strings.TrimSpace(keyword))
		var filtered []PortItem
		for _, item := range cfg.Mappings {
			if kw == "" {
				filtered = append(filtered, item)
			} else {
				portStr := strconv.Itoa(item.Port)
				if strings.Contains(portStr, kw) ||
					strings.Contains(strings.ToLower(item.NodeName), kw) ||
					strings.Contains(strings.ToLower(item.NodeID), kw) ||
					strings.Contains(strings.ToLower(item.City), kw) ||
					strings.Contains(strings.ToLower(item.Province), kw) {
					filtered = append(filtered, item)
				}
			}
		}
		return filtered
	}

	// 1. 无关键字
	all := filter("")
	if len(all) != 3 {
		t.Errorf("filter('') = %d items, want 3", len(all))
	}

	// 2. 按城市筛选
	bj := filter("北京")
	if len(bj) != 1 || bj[0].NodeID != "vvn-bj-1" {
		t.Errorf("filter('北京') failed: %+v", bj)
	}

	// 3. 按端口号筛选
	p1081 := filter("1081")
	if len(p1081) != 1 || p1081[0].NodeID != "vvn-sh-1" {
		t.Errorf("filter('1081') failed: %+v", p1081)
	}

	// 4. 按省份筛选
	gd := filter("广东")
	if len(gd) != 1 || gd[0].NodeID != "vvn-gz-1" {
		t.Errorf("filter('广东') failed: %+v", gd)
	}

	// 5. 无匹配项
	none := filter("武汉")
	if len(none) != 0 {
		t.Errorf("filter('武汉') expected 0, got %d", len(none))
	}
}
