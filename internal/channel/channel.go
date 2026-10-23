package channel

import "time"

// Status 是渠道的可用状态。
type Status string

const (
	// StatusAvailable 渠道可用：数据目录存在，且已发现会话源文件。
	StatusAvailable Status = "available"
	// StatusNotInstalled 渠道未安装：数据目录不存在。这不是错误，是正常结果。
	StatusNotInstalled Status = "not_installed"
	// StatusFailed 渠道存在但检测失败，原因在 DetectResult.Err。
	StatusFailed Status = "failed"
)

// SourceFile 是一个待解析的会话源文件。
type SourceFile struct {
	// Path 是绝对路径。
	Path string
	// Size 是字节数。
	Size int64
	// ModTime 是最后修改时间。
	ModTime time.Time
}

// DetectResult 是「发现」这一步的结果。
type DetectResult struct {
	// Status 是可用状态。
	Status Status
	// Root 是渠道的数据根目录；未安装时为空。
	Root string
	// Files 是发现的会话源文件；非可用状态时为空。
	Files []SourceFile
	// Err 在失败时非空。
	Err error
}

// Adapter 是渠道适配器的契约。
//
// 只有「发现」与「解析」两个方法，不暴露任何存储细节——
// 适配器不知道游标、不知道归档目录、不知道 Markdown。
type Adapter interface {
	// Detect 发现本渠道的会话源文件，返回可用 / 未安装 / 失败三态。
	//
	// 目录不存在时返回 StatusNotInstalled 而**不是**错误。
	Detect() DetectResult
	// Parse 把一个源文件解析成统一会话模型。
	//
	// 单个文件损坏时返回错误，由调用方按「这一条跳过、其余照常」处理（AC-05）。
	Parse(SourceFile) (Session, error)
}

// Prober 是适配器可选实现的轻量探测能力。
//
// 首页统计只关心「这个源文件里有没有会话消息」，不需要完整会话；
// 实现了 Probe 的适配器可以只读到第一条消息就返回，避免把整份会话解析一遍。
// 没实现时调用方退回 Parse，行为不变（I-056）。
type Prober interface {
	Probe(SourceFile) (bool, error)
}

// Report 是注册表聚合后的一条渠道报告：身份 + 检测结果。
type Report struct {
	// ID 是渠道标识。
	ID string
	// Name 是给人看的渠道名。
	Name string
	// DetectResult 是这次检测的结果。
	DetectResult
}
