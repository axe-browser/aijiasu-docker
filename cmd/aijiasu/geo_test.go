package main

import (
	"testing"
)

func TestFindProvinceByCity(t *testing.T) {
	tests := []struct {
		city        string
		wantProv    string
		wantFound   bool
	}{
		{"北京", "北京市", true},
		{"北京市", "北京市", true},
		{"上海", "上海市", true},
		{"深圳", "广东省", true},
		{"广州", "广东省", true},
		{"杭州", "浙江省", true},
		{"杭州市", "浙江省", true},
		{"成都", "四川省", true},
		{"苏州", "江苏省", true},
		{"不存在的未知地名", "其他地区", false},
	}

	for _, tt := range tests {
		t.Run(tt.city, func(t *testing.T) {
			p, found := findProvinceByCity(tt.city)
			if found != tt.wantFound {
				t.Errorf("findProvinceByCity(%q) found = %v, want %v", tt.city, found, tt.wantFound)
			}
			if p.Name != tt.wantProv {
				t.Errorf("findProvinceByCity(%q) province = %q, want %q", tt.city, p.Name, tt.wantProv)
			}
		})
	}
}

func TestExtractCity(t *testing.T) {
	tests := []struct {
		name     string
		wantCity string
	}{
		{"北京 01", "北京"},
		{"上海 05", "上海"},
		{"广州天河 02", "广州天河"},
		{"深圳 10", "深圳"},
		{"", "其他"},
		{"   ", "其他"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCity(tt.name)
			if got != tt.wantCity {
				t.Errorf("extractCity(%q) = %q, want %q", tt.name, got, tt.wantCity)
			}
		})
	}
}

func TestAggregateNodesByProvince(t *testing.T) {
	mockNodes := []NodeItem{
		{ID: "1", Name: "北京 01", City: "北京"},
		{ID: "2", Name: "北京 02", City: "北京"},
		{ID: "3", Name: "上海 01", City: "上海"},
		{ID: "4", Name: "杭州 01", City: "杭州"},
		{ID: "5", Name: "宁波 01", City: "宁波"},
		{ID: "6", Name: "未知虚构城市 01", City: "虚构地名XYZ"},
	}

	groups := aggregateNodesByProvince(mockNodes)
	if len(groups) == 0 {
		t.Fatalf("expected non-empty province groups")
	}

	totalAggregated := 0
	foundBeijing := false
	foundZhejiang := false
	foundOther := false

	for _, g := range groups {
		totalAggregated += g.TotalNodes
		if g.Province.Name == "北京市" {
			foundBeijing = true
			if g.TotalNodes != 2 {
				t.Errorf("Beijing total nodes = %d, want 2", g.TotalNodes)
			}
		}
		if g.Province.Name == "浙江省" {
			foundZhejiang = true
			if g.TotalNodes != 2 {
				t.Errorf("Zhejiang total nodes = %d, want 2", g.TotalNodes)
			}
			if len(g.CityNodes["杭州"]) != 1 || len(g.CityNodes["宁波"]) != 1 {
				t.Errorf("Zhejiang city node split incorrect: %+v", g.CityNodes)
			}
		}
		if g.Province.Name == "其他地区" {
			foundOther = true
			if g.TotalNodes != 1 {
				t.Errorf("Other total nodes = %d, want 1", g.TotalNodes)
			}
		}
	}

	if totalAggregated != len(mockNodes) {
		t.Errorf("total aggregated nodes = %d, want %d", totalAggregated, len(mockNodes))
	}
	if !foundBeijing || !foundZhejiang || !foundOther {
		t.Errorf("missing expected groups: beijing=%v, zhejiang=%v, other=%v", foundBeijing, foundZhejiang, foundOther)
	}
}

func TestSortCitiesByNodeCount(t *testing.T) {
	cityMap := map[string][]NodeItem{
		"城市A": {{ID: "1"}},
		"城市B": {{ID: "2"}, {ID: "3"}, {ID: "4"}},
		"城市C": {{ID: "5"}, {ID: "6"}},
	}

	sorted := sortCitiesByNodeCount(cityMap)
	if len(sorted) != 3 {
		t.Fatalf("expected 3 sorted cities, got %d", len(sorted))
	}
	if sorted[0] != "城市B" || sorted[1] != "城市C" || sorted[2] != "城市A" {
		t.Errorf("unexpected sort order: %v (want [城市B, 城市C, 城市A])", sorted)
	}
}
