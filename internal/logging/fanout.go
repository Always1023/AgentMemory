package logging

// 这个文件解决一个具体的老毛病（I-020）：双击启动时日志文件是空的。
//
// io.MultiWriter 遇到第一个写失败的出口就短路，后面的出口一行都拿不到。
// GUI 程序由资源管理器双击启动时没有控制台，os.Stderr 不是有效句柄，
// 写它失败——于是文件那一份也一句都写不进去，而 slog 又不报写失败，
// 程序看起来一切正常，偏偏在最需要日志的时候没有日志。
//
// 所以要的是「各写各的」：一个出口写砸了，别的出口照拿。

import (
	"io"
)

// fanOut 把一行同时写到几个出口，逐个写、互不牵连。
//
// 返回的错误只在**全部**出口都写失败时才给出（那时这一行确实没落下来）；
// 有任何一个成功就当成功——slog 只看这个错误，没必要为一个失败的出口
// 把整条日志判成没写成。
type fanOut struct {
	sinks []io.Writer
}

func newFanOut(sinks ...io.Writer) *fanOut {
	kept := make([]io.Writer, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			kept = append(kept, sink)
		}
	}
	return &fanOut{sinks: kept}
}

// Write 逐个出口写一遍，返回最后那个失败（全失败才算失败）。
func (f *fanOut) Write(p []byte) (int, error) {
	if len(f.sinks) == 0 {
		return len(p), nil // 没有出口就当写下去了：日志不该反过来把调用方拖住
	}

	written := 0
	var lastErr error
	for _, sink := range f.sinks {
		n, err := sink.Write(p)
		if err != nil {
			lastErr = err
			continue
		}
		if n > written {
			written = n
		}
	}
	if written == 0 && lastErr != nil {
		return 0, lastErr
	}
	return written, nil
}
