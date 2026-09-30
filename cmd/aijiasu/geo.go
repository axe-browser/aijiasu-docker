package main

import (
	"sort"
	"strings"
)

// 中国行政区划省份定义
type ProvinceDef struct {
	Name   string   // 省份/直辖市名称，如 "广东省", "北京市"
	Short  string   // 简称，如 "广东", "北京"
	Region string   // 大区，如 "华南", "华北"
	Cities []string // 该省份下爱加速覆盖的地级市/区域
}

// 全国省份及下辖城市归属字典（完整覆盖爱加速所有 211 个城市）
var provinces = []ProvinceDef{
	// 直辖市
	{
		Name:   "北京市",
		Short:  "北京",
		Region: "直辖市",
		Cities: []string{"北京"},
	},
	{
		Name:   "上海市",
		Short:  "上海",
		Region: "直辖市",
		Cities: []string{"上海"},
	},
	{
		Name:   "天津市",
		Short:  "天津",
		Region: "直辖市",
		Cities: []string{"天津"},
	},
	{
		Name:   "重庆市",
		Short:  "重庆",
		Region: "直辖市",
		Cities: []string{"重庆"},
	},

	// 华东地区
	{
		Name:   "江苏省",
		Short:  "江苏",
		Region: "华东",
		Cities: []string{"南京", "苏州", "无锡", "常州", "镇江", "南通", "扬州", "泰州", "盐城", "淮安", "连云港", "徐州", "宿迁"},
	},
	{
		Name:   "浙江省",
		Short:  "浙江",
		Region: "华东",
		Cities: []string{"杭州", "宁波", "温州", "嘉兴", "湖州", "绍兴", "金华", "台州", "丽水"},
	},
	{
		Name:   "安徽省",
		Short:  "安徽",
		Region: "华东",
		Cities: []string{"合肥", "芜湖", "蚌埠", "淮南", "马鞍山", "铜陵", "安庆", "黄山", "滁州", "阜阳", "宿州", "六安", "亳州", "宣城"},
	},
	{
		Name:   "福建省",
		Short:  "福建",
		Region: "华东",
		Cities: []string{"福州", "厦门", "莆田", "三明", "泉州", "漳州", "南平", "龙岩", "宁德"},
	},
	{
		Name:   "江西省",
		Short:  "江西",
		Region: "华东",
		Cities: []string{"南昌", "九江", "鹰潭", "赣州", "吉安", "宜春", "抚州", "上饶"},
	},
	{
		Name:   "山东省",
		Short:  "山东",
		Region: "华东",
		Cities: []string{"济南", "青岛", "淄博", "枣庄", "东营", "烟台", "潍坊", "济宁", "泰安", "威海", "日照", "临沂", "德州", "聊城", "滨州", "菏泽"},
	},

	// 华南地区
	{
		Name:   "广东省",
		Short:  "广东",
		Region: "华南",
		Cities: []string{"广州", "深圳", "东莞", "佛山", "中山", "珠海", "惠州", "汕头", "江门", "湛江", "茂名", "肇庆", "汕尾", "河源", "阳江", "潮州", "揭阳", "云浮", "韶关"},
	},
	{
		Name:   "广西壮族自治区",
		Short:  "广西",
		Region: "华南",
		Cities: []string{"南宁", "玉林"},
	},
	{
		Name:   "海南省",
		Short:  "海南",
		Region: "华南",
		Cities: []string{"海口"},
	},

	// 华中地区
	{
		Name:   "河南省",
		Short:  "河南",
		Region: "华中",
		Cities: []string{"郑州", "开封", "洛阳", "平顶山", "安阳", "鹤壁", "新乡", "焦作", "濮阳", "许昌", "漯河", "三门峡", "南阳", "商丘", "信阳", "周口", "驻马店", "济源"},
	},
	{
		Name:   "湖北省",
		Short:  "湖北",
		Region: "华中",
		Cities: []string{"武汉", "襄阳", "鄂州", "荆州", "十堰", "潜江"},
	},
	{
		Name:   "湖南省",
		Short:  "湖南",
		Region: "华中",
		Cities: []string{"长沙", "株洲", "湘潭", "衡阳", "邵阳", "岳阳", "常德", "张家界", "益阳", "郴州", "永州", "怀化", "娄底", "湘西"},
	},

	// 华北地区
	{
		Name:   "河北省",
		Short:  "河北",
		Region: "华北",
		Cities: []string{"石家庄", "廊坊", "衡水"},
	},
	{
		Name:   "山西省",
		Short:  "山西",
		Region: "华北",
		Cities: []string{"太原", "大同", "阳泉", "长治", "晋城", "朔州", "晋中", "运城", "忻州", "临汾", "吕梁"},
	},
	{
		Name:   "内蒙古自治区",
		Short:  "内蒙古",
		Region: "华北",
		Cities: []string{"呼和浩特", "包头", "乌海", "呼伦贝尔", "乌兰察布", "兴安盟", "阿拉善盟"},
	},

	// 西北地区
	{
		Name:   "陕西省",
		Short:  "陕西",
		Region: "西北",
		Cities: []string{"西安", "延安", "渭南", "汉中"},
	},
	{
		Name:   "甘肃省",
		Short:  "甘肃",
		Region: "西北",
		Cities: []string{"兰州", "庆阳"},
	},
	{
		Name:   "青海省",
		Short:  "青海",
		Region: "西北",
		Cities: []string{"西宁", "海东", "海南藏族自治州"},
	},
	{
		Name:   "宁夏回族自治区",
		Short:  "宁夏",
		Region: "西北",
		Cities: []string{"银川", "中卫"},
	},
	{
		Name:   "新疆维吾尔自治区",
		Short:  "新疆",
		Region: "西北",
		Cities: []string{"乌鲁木齐", "哈密", "昌吉", "石河子", "和田"},
	},

	// 西南地区
	{
		Name:   "四川省",
		Short:  "四川",
		Region: "西南",
		Cities: []string{"成都", "绵阳", "德阳", "乐山", "眉山", "资阳"},
	},
	{
		Name:   "贵州省",
		Short:  "贵州",
		Region: "西南",
		Cities: []string{"贵阳"},
	},
	{
		Name:   "云南省",
		Short:  "云南",
		Region: "西南",
		Cities: []string{"昆明", "曲靖", "普洱", "西双版纳", "楚雄"},
	},
	{
		Name:   "西藏自治区",
		Short:  "西藏",
		Region: "西南",
		Cities: []string{"拉萨"},
	},

	// 东北地区
	{
		Name:   "辽宁省",
		Short:  "辽宁",
		Region: "东北",
		Cities: []string{"沈阳", "大连", "鞍山", "抚顺", "本溪", "锦州", "营口", "阜新", "辽阳", "盘锦", "朝阳", "葫芦岛"},
	},
	{
		Name:   "吉林省",
		Short:  "吉林",
		Region: "东北",
		Cities: []string{"长春", "吉林", "四平", "辽源", "松原", "白城", "延吉"},
	},
	{
		Name:   "黑龙江省",
		Short:  "黑龙江",
		Region: "东北",
		Cities: []string{"哈尔滨", "齐齐哈尔", "鹤岗", "双鸭山", "大庆", "伊春", "佳木斯", "七台河", "牡丹江", "黑河", "鸡西"},
	},
}

// 城市到省份的快速反向索引映射表
var cityToProvinceMap = make(map[string]ProvinceDef)

func init() {
	for _, p := range provinces {
		for _, c := range p.Cities {
			cityToProvinceMap[c] = p
		}
	}
}

// 根据城市名称查找所属省份
func findProvinceByCity(city string) (ProvinceDef, bool) {
	city = strings.TrimSpace(city)
	if p, ok := cityToProvinceMap[city]; ok {
		return p, true
	}
	// 模糊匹配
	for c, p := range cityToProvinceMap {
		if strings.HasPrefix(city, c) || strings.HasPrefix(c, city) {
			return p, true
		}
	}
	return ProvinceDef{Name: "其他地区", Short: "其他", Region: "其他"}, false
}


// 排序辅助：按省份和城市排布
type ProvinceGroupData struct {
	Province   ProvinceDef
	TotalNodes int
	CityNodes  map[string][]NodeItem
}

func aggregateNodesByProvince(nodes []NodeItem) []ProvinceGroupData {
	groupMap := make(map[string]*ProvinceGroupData)

	// 初始化所有已知省份
	for _, p := range provinces {
		groupMap[p.Name] = &ProvinceGroupData{
			Province:  p,
			CityNodes: make(map[string][]NodeItem),
		}
	}

	// 填充分组数据
	var otherNodes []NodeItem
	for _, n := range nodes {
		p, found := findProvinceByCity(n.City)
		if !found {
			otherNodes = append(otherNodes, n)
			continue
		}
		g := groupMap[p.Name]
		g.TotalNodes++
		g.CityNodes[n.City] = append(g.CityNodes[n.City], n)
	}

	// 收集并保持省份的原始顺序
	var result []ProvinceGroupData
	for _, p := range provinces {
		g := groupMap[p.Name]
		if g.TotalNodes > 0 {
			result = append(result, *g)
		}
	}

	// 处理未归类节点
	if len(otherNodes) > 0 {
		otherData := ProvinceGroupData{
			Province:   ProvinceDef{Name: "其他地区", Short: "其他", Region: "其他"},
			TotalNodes: len(otherNodes),
			CityNodes:  make(map[string][]NodeItem),
		}
		for _, n := range otherNodes {
			otherData.CityNodes[n.City] = append(otherData.CityNodes[n.City], n)
		}
		result = append(result, otherData)
	}

	return result
}

// 按节点数量从大到小对城市排序
func sortCitiesByNodeCount(cityMap map[string][]NodeItem) []string {
	type Pair struct {
		City  string
		Count int
	}
	var pairs []Pair
	for c, list := range cityMap {
		pairs = append(pairs, Pair{City: c, Count: len(list)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Count > pairs[j].Count
	})
	var cities []string
	for _, p := range pairs {
		cities = append(cities, p.City)
	}
	return cities
}
