package main

// この節は、使う DAP の request の引数と、response と event の body の型である。
// 最初の範囲（基本設計 6.4）で使う項目だけを持ち、名前は DAP の仕様のとおりにする。
// 仕様: https://microsoft.github.io/debug-adapter-protocol/specification

// initializeArguments は、initialize の引数（仕様の InitializeRequestArguments）である。
type initializeArguments struct {
	ClientID   string `json:"clientID,omitempty"`
	ClientName string `json:"clientName,omitempty"`
	AdapterID  string `json:"adapterID"`
	// LinesStartAt1 と ColumnsStartAt1 は、行と列を 1 から数えるかどうかである。仕様の既定は true。
	LinesStartAt1   bool   `json:"linesStartAt1"`
	ColumnsStartAt1 bool   `json:"columnsStartAt1"`
	PathFormat      string `json:"pathFormat,omitempty"`
	// SupportsVariableType は、変数の型（Variable の type）を受け取るかどうかである（基本設計 6.4 の開始の流れの 8）。
	SupportsVariableType bool `json:"supportsVariableType,omitempty"`
}

// capabilities は、initialize の response の body（仕様の Capabilities）のうち、使う項目である。
type capabilities struct {
	SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest,omitempty"`
	SupportsTerminateRequest         bool `json:"supportsTerminateRequest,omitempty"`
}

// source は、仕様の Source のうち、使う項目である。
type source struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// sourceBreakpoint は、setBreakpoints で送るブレークポイント（仕様の SourceBreakpoint）である。
type sourceBreakpoint struct {
	Line int `json:"line"`
	// Condition は、条件付きのブレークポイントの式である。無ければ省く。
	// デバッグアダプターが supportsConditionalBreakpoints を持たなければ、無視されうる（仕様の SourceBreakpoint）。
	Condition string `json:"condition,omitempty"`
}

// setBreakpointsArguments は、setBreakpoints の引数である。
// breakpoints はそのファイルのブレークポイントのすべてで、空の列はすべての解除を表す。
type setBreakpointsArguments struct {
	Source      source             `json:"source"`
	Breakpoints []sourceBreakpoint `json:"breakpoints"`
}

// breakpoint は、仕様の Breakpoint のうち、使う項目である。
type breakpoint struct {
	// Verified は、ブレークポイントを置けたかどうかである（望んだ位置とは限らない）。
	Verified bool `json:"verified"`
	// Line は、実際に置いた位置の開始の行である。無ければ 0。
	Line int `json:"line,omitempty"`
}

// setBreakpointsResponse は、setBreakpoints の response の body である。
// Breakpoints は、引数の breakpoints と同じ順に並ぶ（仕様の SetBreakpoints Request）。
type setBreakpointsResponse struct {
	Breakpoints []breakpoint `json:"breakpoints"`
}

// threadArguments は、continue、next、stepIn、stepOut の引数である。
type threadArguments struct {
	ThreadID int `json:"threadId"`
}

// stackTraceArguments は、stackTrace の引数である。
type stackTraceArguments struct {
	ThreadID   int `json:"threadId"`
	StartFrame int `json:"startFrame,omitempty"`
	Levels     int `json:"levels,omitempty"`
}

// stackFrame は、仕様の StackFrame のうち、使う項目である。
// Source が無いとき、Line と Column は 0 で、使ってはならない（仕様の StackFrame）。
type stackFrame struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Source *source `json:"source,omitempty"`
	Line   int     `json:"line"`
	// Column は、UTF-16 のコード単位で数えた列である。0 から数えるか 1 から数えるかは columnsStartAt1 で決まる。
	Column int `json:"column"`
}

// stackTraceResponse は、stackTrace の response の body である。
type stackTraceResponse struct {
	StackFrames []stackFrame `json:"stackFrames"`
}

// thread は、仕様の Thread である。
type thread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// threadsResponse は、threads の response の body である。
type threadsResponse struct {
	Threads []thread `json:"threads"`
}

// disconnectArguments は、disconnect の引数である。
type disconnectArguments struct {
	// TerminateDebuggee は、切断のときにデバッグ対象を終わらせるかどうかである。
	TerminateDebuggee bool `json:"terminateDebuggee"`
}

// stoppedEvent は、stopped の event の body である。ThreadID は省かれることがある。
type stoppedEvent struct {
	Reason      string `json:"reason"`
	Description string `json:"description,omitempty"`
	ThreadID    *int   `json:"threadId,omitempty"`
}

// outputEvent は、output の event の body のうち、使う項目である。
type outputEvent struct {
	// Category は、出力の種類である。省くと console（仕様の OutputEvent）。
	Category string `json:"category,omitempty"`
	Output   string `json:"output"`
}

// exitedEvent は、exited の event の body である。
type exitedEvent struct {
	ExitCode int `json:"exitCode"`
}
