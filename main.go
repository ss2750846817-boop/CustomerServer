package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Ticket 单条工单数据，字段含义见 task5_ticket_fields.md
type Ticket struct {
	TicketID            string  `json:"ticket_id"`
	CreatedAt           string  `json:"created_at"`
	Category            string  `json:"category"`
	Description         string  `json:"description"`
	Priority            string  `json:"priority"`
	ResolutionTimeHours float64 `json:"resolution_time_hours"`
	Satisfaction        int     `json:"satisfaction"`
	Channel             string  `json:"channel"`
	IsResolved          bool    `json:"is_resolved"`
}

// CategoryStat 某分类聚合统计
type CategoryStat struct {
	Category         string
	Count            int
	HighCount        int
	Unresolved       int
	AvgResolveHours  float64
	AvgSatisfaction  float64
	LowSatCount      int // 满意度 <=2 的工单数
}

// Summary 全量概览
type Summary struct {
	Total             int
	Resolved          int
	Unresolved        int
	HighCount         int
	AvgResolveHours   float64
	AvgSatisfaction   float64
	DateRange         string
}

// CategoryKey 按日+分类计数，用于发现某类工单突然增多
type DayCategory struct {
	Day      string
	Category string
	Count    int
}

func main() {
	input := "task5_tickets.json"
	if len(os.Args) > 1 {
		input = os.Args[1]
	}
	tickets := loadTickets(input)
	report := analyze(tickets)

	md := renderMarkdown(report)
	if err := os.WriteFile("report.md", []byte(md), 0644); err != nil {
		fatal(err)
	}
	html := renderHTML(report)
	if err := os.WriteFile("dashboard.html", []byte(html), 0644); err != nil {
		fatal(err)
	}
	fmt.Print(md)
	fmt.Fprintln(os.Stderr, "\n[written] report.md, dashboard.html")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func loadTickets(path string) []Ticket {
	f, err := os.Open(path)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		fatal(err)
	}
	var tickets []Ticket
	if err := json.Unmarshal(data, &tickets); err != nil {
		fatal(err)
	}
	return tickets
}

func parseDay(ts string) string {
	t, err := time.Parse("2006-01-02 15:04", ts)
	if err != nil {
		return ts
	}
	return t.Format("2006-01-02")
}

// analyze 汇总所有维度统计
func analyze(tickets []Ticket) *Report {
	r := &Report{Summary: Summary{}, ByCategory: []CategoryStat{}, ByDay: []DayStat{}, Anomalies: []Anomaly{}, Keyword: []KeywordStat{}}

	// 基础概览
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].CreatedAt < tickets[j].CreatedAt })
	r.Summary.Total = len(tickets)
	first, last := tickets[0].CreatedAt[:10], tickets[len(tickets)-1].CreatedAt[:10]
	r.Summary.DateRange = first + " ~ " + last

	catMap := map[string]*CategoryStat{}
	var high, resolved, unresolved int
	var totalResolve, totalSat float64
	highAll := 0

	for _, t := range tickets {
		if t.IsResolved {
			resolved++
		} else {
			unresolved++
		}
		if t.Priority == "高" {
			high++
			highAll++
		}
		totalResolve += t.ResolutionTimeHours
		if t.Satisfaction >= 1 {
			totalSat += float64(t.Satisfaction)
		}
		cs := catMap[t.Category]
		if cs == nil {
			cs = &CategoryStat{Category: t.Category}
			catMap[t.Category] = cs
		}
		cs.Count++
		if t.Priority == "高" {
			cs.HighCount++
		}
		if !t.IsResolved {
			cs.Unresolved++
		}
		cs.AvgResolveHours += t.ResolutionTimeHours
		cs.AvgSatisfaction += float64(t.Satisfaction)
		if t.Satisfaction <= 2 {
			cs.LowSatCount++
		}
	}
	for _, cs := range catMap {
		cs.AvgResolveHours /= float64(cs.Count)
		cs.AvgSatisfaction /= float64(cs.Count)
		r.ByCategory = append(r.ByCategory, *cs)
	}
	sort.Slice(r.ByCategory, func(i, j int) bool { return r.ByCategory[i].Count > r.ByCategory[j].Count })

	r.Summary.Resolved = resolved
	r.Summary.Unresolved = unresolved
	r.Summary.HighCount = high
	if len(tickets) > 0 {
		r.Summary.AvgResolveHours = totalResolve / float64(len(tickets))
		r.Summary.AvgSatisfaction = totalSat / float64(len(tickets))
	}

	// 按日统计
	dayMap := map[string]int{}
	dayCatMap := map[string]map[string]int{}
	dayIndex := []string{}
	for _, t := range tickets {
		d := parseDay(t.CreatedAt)
		if _, ok := dayMap[d]; !ok {
			dayIndex = append(dayIndex, d)
		}
		dayMap[d]++
		if dayCatMap[d] == nil {
			dayCatMap[d] = map[string]int{}
		}
		dayCatMap[d][t.Category]++
	}
	sort.Strings(dayIndex)
	// 全局均值
	avgPerDay := float64(len(tickets)) / float64(len(dayIndex))
	r.AvgPerDay = avgPerDay
	for _, d := range dayIndex {
		ds := DayStat{Day: d, Count: dayMap[d], Categories: map[string]int{}}
		for c, n := range dayCatMap[d] {
			ds.Categories[c] = n
		}
		r.ByDay = append(r.ByDay, ds)
	}

	// 异常检测1：单日总量异常高峰（> 均值 * 1.5 且绝对值超过均值一定差距）
	for _, d := range dayIndex {
		if float64(dayMap[d]) >= avgPerDay*1.5 && dayMap[d]-int(avgPerDay) >= 2 {
			r.Anomalies = append(r.Anomalies, Anomaly{Type: "高峰日", Severity: "中", Detail: fmt.Sprintf("%s 工单总量 %d，高于日均 %.1f", d, dayMap[d], avgPerDay)})
		}
	}

	// 异常检测2：分类随时间增长趋势（后半段 vs 前半段数量激增）
	half := len(dayIndex) / 2
	if half >= 1 {
		catHalf := map[string][2]int{} // category -> [前半, 后半]
		for i, d := range dayIndex {
			for c, n := range dayCatMap[d] {
				hv := catHalf[c]
				if i < half {
					hv[0] += n
				} else {
					hv[1] += n
				}
				catHalf[c] = hv
			}
		}
		for c, hv := range catHalf {
			if hv[1] >= 2 && hv[0] == 0 {
				r.Anomalies = append(r.Anomalies, Anomaly{Type: "分类突增", Severity: "高",
					Detail: fmt.Sprintf("「%s」在后半段新增 %d 条，前半段为 0，明显集中爆发", c, hv[1])})
			} else if hv[1] >= hv[0]*2 && hv[1]-hv[0] >= 2 {
				r.Anomalies = append(r.Anomalies, Anomaly{Type: "增长趋势", Severity: "高",
					Detail: fmt.Sprintf("「%s」后半段 %d 条 vs 前半段 %d 条，增长 %.0f 倍", c, hv[1], hv[0], float64(hv[1])/float64(hv[0]))})
			}
		}
	}

	// 异常检测3：高优先级未解决（需重点关注）
	for _, t := range tickets {
		if !t.IsResolved && t.Priority == "高" {
			r.Anomalies = append(r.Anomalies, Anomaly{Type: "高危未解决", Severity: "高", Detail: fmt.Sprintf("%s 高优先级未解决：%s（%s）", t.TicketID, t.Category, t.Description)})
		}
	}

	// 异常检测4：处理时长异常（> 72h）
	for _, t := range tickets {
		if t.ResolutionTimeHours > 72 {
			r.Anomalies = append(r.Anomalies, Anomaly{Type: "处理超时", Severity: "中", Detail: fmt.Sprintf("%s %s 处理时长 %.0f 小时", t.TicketID, t.Category, t.ResolutionTimeHours)})
		}
	}

	// 异常检测5：关键词反复出现（精确子串匹配，每个关键词统计"描述中包含该确切子串"的工单条数）
	kwList := []string{"扣款", "支付", "退款", "退货运费", "物流", "重复", "机器人", "发货"}
	kwMap := map[string]int{}
	for _, kw := range kwList {
		for _, t := range tickets {
			if strings.Contains(t.Description, kw) {
				kwMap[kw]++
			}
		}
	}
	for k, v := range kwMap {
		if v >= 2 {
			r.Keyword = append(r.Keyword, KeywordStat{Keyword: k, Count: v})
		}
	}
	sort.Slice(r.Keyword, func(i, j int) bool { return r.Keyword[i].Count > r.Keyword[j].Count })

	// 异常检测6：同一用户/同一问题反复出现（重复扣款关键词若在每日出现）
	// 关联：分类 x 渠道
	channelMap := map[string]int{}
	for _, t := range tickets {
		channelMap[t.Channel]++
	}
	r.ChannelDist = channelMap

	// 关联：分类 x 优先级分布
	prioCat := map[string]map[string]int{}
	for _, t := range tickets {
		if prioCat[t.Category] == nil {
			prioCat[t.Category] = map[string]int{}
		}
		prioCat[t.Category][t.Priority]++
	}
	r.CategoryPriority = prioCat

	// 排序异常按类型分组，输出时统一整理
	r.OverallTopCategory = r.ByCategory[0].Category
	r.OverallLowestSat = lowestSatCategory(r.ByCategory)

	return r
}

func lowestSatCategory(cs []CategoryStat) string {
	min := cs[0]
	for _, c := range cs {
		if c.AvgSatisfaction < min.AvgSatisfaction {
			min = c
		}
	}
	return min.Category
}

// ===== 输出结构 =====

type DayStat struct {
	Day        string
	Count      int
	Categories map[string]int
}

type Anomaly struct {
	Type     string
	Severity string
	Detail   string
}

type KeywordStat struct {
	Keyword string
	Count   int
}

type Report struct {
	Summary               Summary
	ByCategory            []CategoryStat
	ByDay                 []DayStat
	AvgPerDay             float64
	Anomalies             []Anomaly
	Keyword               []KeywordStat
	ChannelDist           map[string]int
	CategoryPriority      map[string]map[string]int
	OverallTopCategory    string
	OverallLowestSat      string
}

func printSectionHeader(w *bufio.Writer, s string) {
	fmt.Fprintf(w, "\n## %s\n\n", s)
}

// renderMarkdown 输出结构化 Markdown 报告
func renderMarkdown(r *Report) string {
	var sb strings.Builder
	w := bufio.NewWriter(&sb)

	fmt.Fprintf(w, "# 客服工单趋势分析报告\n\n")
	fmt.Fprintf(w, "> 数据范围：%s　|　共 %d 条工单\n\n", r.Summary.DateRange, r.Summary.Total)

	printSectionHeader(w, "一、总体概览")
	fmt.Fprintf(w, "| 指标 | 数值 |\n|---|---|\n")
	fmt.Fprintf(w, "| 工单总数 | %d |\n", r.Summary.Total)
	fmt.Fprintf(w, "| 已解决 | %d (%.1f%%) |\n", r.Summary.Resolved, pct(r.Summary.Resolved, r.Summary.Total))
	fmt.Fprintf(w, "| 未解决 | %d (%.1f%%) |\n", r.Summary.Unresolved, pct(r.Summary.Unresolved, r.Summary.Total))
	fmt.Fprintf(w, "| 高优先级占比 | %d (%.1f%%) |\n", r.Summary.HighCount, pct(r.Summary.HighCount, r.Summary.Total))
	fmt.Fprintf(w, "| 平均处理时长 | %.1f 小时 |\n", r.Summary.AvgResolveHours)
	fmt.Fprintf(w, "| 平均满意度 | %.2f / 5 |\n", r.Summary.AvgSatisfaction)

	printSectionHeader(w, "二、分类分布（类型分布维度）")
	fmt.Fprintf(w, "| 分类 | 数量 | 占比 | 高优先级 | 未解决 | 平均处理(h) | 平均满意度 | 低满意(≤2) |\n|---|---|---|---|---|---|---|---|\n")
	for _, c := range r.ByCategory {
		fmt.Fprintf(w, "| %s | %d | %.1f%% | %d | %d | %.1f | %.2f | %d |\n",
			c.Category, c.Count, pct(c.Count, r.Summary.Total), c.HighCount, c.Unresolved,
			c.AvgResolveHours, c.AvgSatisfaction, c.LowSatCount)
	}

	printSectionHeader(w, "三、每日趋势（时间趋势维度）")
	var dayBar strings.Builder
	for _, d := range r.ByDay {
		bar := strings.Repeat("█", d.Count)
		dayBar.WriteString(fmt.Sprintf("| %s | %d | %s |\n", d.Day, d.Count, bar))
	}
	fmt.Fprintf(w, "| 日期 | 数量 | 分布 |\n|---|---|---|\n%s", dayBar.String())
	fmt.Fprintf(w, "\n日均工单数：%.1f 条/天\n", r.AvgPerDay)

	printSectionHeader(w, "四、严重程度 / 处理质量")
	fmt.Fprintf(w, "- 平均处理时长 %.1f 小时，其中「退款退货」平均 %.1f 小时，为各分类最长。\n", r.Summary.AvgResolveHours, catResolve(r.ByCategory, "退款退货"))
	fmt.Fprintf(w, "- 平均满意度 %.2f，满意度最低的分类为「%s」。\n", r.Summary.AvgSatisfaction, r.OverallLowestSat)

	printSectionHeader(w, "五、渠道与关联关系")
	fmt.Fprintf(w, "**渠道分布**\n\n")
	fmt.Fprintf(w, "| 渠道 | 数量 |\n|---|---|\n")
	for ch, n := range r.ChannelDist {
		fmt.Fprintf(w, "| %s | %d |\n", ch, n)
	}
	fmt.Fprintf(w, "\n**分类 × 优先级分布**\n\n")
	fmt.Fprintf(w, "| 分类 | 高 | 中 | 低 |\n|---|---|---|---|\n")
	for _, c := range r.ByCategory {
		pm := r.CategoryPriority[c.Category]
		fmt.Fprintf(w, "| %s | %d | %d | %d |\n", c.Category, pm["高"], pm["中"], pm["低"])
	}

	printSectionHeader(w, "六、关键异常信号")
	if len(r.Anomalies) == 0 {
		fmt.Fprintf(w, "未发现明显异常。\n")
	} else {
		fmt.Fprintf(w, "| 类型 | 严重度 | 说明 |\n|---|---|---|\n")
		for _, a := range r.Anomalies {
			fmt.Fprintf(w, "| %s | %s | %s |\n", a.Type, a.Severity, a.Detail)
		}
	}

	printSectionHeader(w, "七、反复出现的关键问题（关键词分析）")
	if len(r.Keyword) == 0 {
		fmt.Fprintf(w, "无明显高频关键词。\n")
	} else {
		fmt.Fprintf(w, "| 关键词 | 出现次数 |\n|---|---|\n")
		for _, k := range r.Keyword {
			fmt.Fprintf(w, "| %s | %d |\n", k.Keyword, k.Count)
		}
	}

	w.Flush()
	return sb.String()
}

func catResolve(cs []CategoryStat, name string) float64 {
	for _, c := range cs {
		if c.Category == name {
			return c.AvgResolveHours
		}
	}
	return 0
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d) * 100
}

// renderHTML 生成自带图表的 HTML Dashboard（纯前端，无需外部依赖）
func renderHTML(r *Report) string {
	var sb strings.Builder

	// 准备图表数据
	var catNames, catCounts strings.Builder
	for i, c := range r.ByCategory {
		if i > 0 {
			catNames.WriteString(",")
			catCounts.WriteString(",")
		}
		catNames.WriteString(fmt.Sprintf("'%s'", c.Category))
		catCounts.WriteString(fmt.Sprint(c.Count))
	}
	var dayNames, dayCounts strings.Builder
	for i, d := range r.ByDay {
		if i > 0 {
			dayNames.WriteString(",")
			dayCounts.WriteString(",")
		}
		dayNames.WriteString(fmt.Sprintf("'%s'", d.Day[5:]))
		dayCounts.WriteString(fmt.Sprint(d.Count))
	}
	// 异常
	var anomRows strings.Builder
	for _, a := range r.Anomalies {
		sev := "orange"
		if a.Severity == "高" {
			sev = "red"
		} else if a.Severity == "低" {
			sev = "green"
		}
		fmt.Fprintf(&anomRows, "<tr><td>%s</td><td style='color:%s;font-weight:bold'>%s</td><td>%s</td></tr>", a.Type, sev, a.Severity, a.Detail)
	}
	// 关键词
	var kwRows strings.Builder
	for _, k := range r.Keyword {
		fmt.Fprintf(&kwRows, "<tr><td>%s</td><td>%d</td></tr>", k.Keyword, k.Count)
	}
	// 分类表
	var catRows strings.Builder
	for _, c := range r.ByCategory {
		fmt.Fprintf(&catRows, "<tr><td>%s</td><td>%d</td><td>%.1f%%</td><td>%d</td><td>%d</td><td>%.1f</td><td>%.2f</td><td>%d</td></tr>",
			c.Category, c.Count, pct(c.Count, r.Summary.Total), c.HighCount, c.Unresolved, c.AvgResolveHours, c.AvgSatisfaction, c.LowSatCount)
	}

	sb.WriteString(`<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>客服工单趋势 Dashboard</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<style>
body{font-family:-apple-system,Segoe UI,Microsoft YaHei,sans-serif;margin:0;background:#f5f6fa;color:#222}
header{background:linear-gradient(90deg,#2c3e50,#3498db);color:#fff;padding:24px 32px}
header h1{margin:0;font-size:22px}header p{margin:6px 0 0;opacity:.85;font-size:13px}
.wrap{max-width:1100px;margin:24px auto;padding:0 16px}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:16px;margin-bottom:24px}
.card{background:#fff;border-radius:10px;padding:18px;box-shadow:0 1px 4px rgba(0,0,0,.08);text-align:center}
.card .num{font-size:28px;font-weight:700;color:#2c3e50}
.card .lbl{font-size:13px;color:#888;margin-top:6px}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:24px}
.panel{background:#fff;border-radius:10px;padding:20px;box-shadow:0 1px 4px rgba(0,0,0,.08);margin-bottom:24px}
.panel h2{margin:0 0 16px;font-size:16px;border-left:4px solid #3498db;padding-left:10px}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{padding:8px 10px;text-align:left;border-bottom:1px solid #eee}
th{background:#f8f9fb;color:#555}
.badge{display:inline-block;padding:2px 8px;border-radius:12px;font-size:12px}
.anomaly table{border:0}
.anomaly tr:nth-child(even){background:#fafbfd}
canvas{max-height:300px}
@media(max-width:800px){.grid{grid-template-columns:1fr}}
</style>
</head>
<body>
<header>
<h1>客服工单趋势 Dashboard</h1>
<p>数据范围：`+r.Summary.DateRange+` ｜ 共 `+fmt.Sprint(r.Summary.Total)+` 条工单 ｜ 生成时间 `+time.Now().Format("2006-01-02 15:04")+`</p>
</header>
<div class="wrap">
<div class="cards">
<div class="card"><div class="num">`+fmt.Sprint(r.Summary.Total)+`</div><div class="lbl">工单总数</div></div>
<div class="card"><div class="num">`+fmt.Sprint(r.Summary.HighCount)+`</div><div class="lbl">高优先级</div></div>
<div class="card"><div class="num">`+fmt.Sprint(r.Summary.Unresolved)+`</div><div class="lbl">未解决</div></div>
<div class="card"><div class="num">`+fmt.Sprintf("%.1f", r.Summary.AvgResolveHours)+`</div><div class="lbl">平均处理(h)</div></div>
<div class="card"><div class="num">`+fmt.Sprintf("%.2f", r.Summary.AvgSatisfaction)+`</div><div class="lbl">平均满意度</div></div>
</div>

<div class="grid">
<div class="panel"><h2>分类分布</h2><canvas id="catChart"></canvas></div>
<div class="panel"><h2>每日工单趋势</h2><canvas id="dayChart"></canvas></div>
</div>

<div class="grid">
<div class="panel anomaly"><h2>关键异常信号（`+fmt.Sprint(len(r.Anomalies))+`）</h2>
<table><thead><tr><th>类型</th><th>严重度</th><th>说明</th></tr></thead><tbody>`+anomRows.String()+`</tbody></table></div>
<div class="panel"><h2>反复出现的关键问题</h2>
`+kwTable(kwRows.String())+`
</div>
</div>

<div class="panel"><h2>分类明细</h2>
<table><thead><tr><th>分类</th><th>数量</th><th>占比</th><th>高优先级</th><th>未解决</th><th>平均处理(h)</th><th>平均满意度</th><th>低满意(≤2)</th></tr></thead><tbody>`+catRows.String()+`</tbody></table></div>
</div>

<script>
const catNames=[`+catNames.String()+`];
const catCounts=[`+catCounts.String()+`];
new Chart(document.getElementById('catChart'),{type:'pie',data:{labels:catNames,datasets:[{data:catCounts,backgroundColor:['#3498db','#e74c3c','#2ecc71','#f1c40f','#9b59b6','#1abc9c','#e67e22']}]},options:{plugins:{legend:{position:'bottom'}}}});
const dayNames=[`+dayNames.String()+`];
const dayCounts=[`+dayCounts.String()+`];
new Chart(document.getElementById('dayChart'),{type:'bar',data:{labels:dayNames,datasets:[{label:'工单数',data:dayCounts,backgroundColor:'#3498db'}]},options:{plugins:{legend:{display:false}},scales:{y:{beginAtZero:true}}}});
</script>
</body>
</html>`)

	return sb.String()
}

func kwTable(rows string) string {
	if rows == "" {
		return "<p>无明显高频关键词。</p>"
	}
	return "<table><thead><tr><th>关键词</th><th>出现次数</th></tr></thead><tbody>" + rows + "</tbody></table>"
}