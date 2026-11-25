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
	// SupportsStartDebuggingRequest は、逆向きの request の startDebugging（子のセッション。FR-285）を受け付けるかどうかである。
	SupportsStartDebuggingRequest bool `json:"supportsStartDebuggingRequest,omitempty"`
}

// capabilities は、initialize の response の body（仕様の Capabilities）のうち、使う項目である（FR-286）。
type capabilities struct {
	SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest,omitempty"`
	SupportsTerminateRequest         bool `json:"supportsTerminateRequest,omitempty"`
	SupportsConditionalBreakpoints   bool `json:"supportsConditionalBreakpoints,omitempty"`
	SupportsRestartRequest           bool `json:"supportsRestartRequest,omitempty"`
	// SupportsSetVariable と SupportsSetExpression は、setVariable と setExpression に対応するかである（スコープの区画の値の書き換え。FR-289）。
	SupportsSetVariable   bool `json:"supportsSetVariable,omitempty"`
	SupportsSetExpression bool `json:"supportsSetExpression,omitempty"`
}

// scopesArguments は、scopes の引数（仕様の ScopesArguments）である。frameId は今の止まった状態で得たものに限る（同仕様）。
type scopesArguments struct {
	FrameID int `json:"frameId"`
}

// scope は、仕様の Scope のうち、使う項目である。
type scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	// Expensive は、変数が多いか読むのが重いことを表す（同仕様）。スコープの区画は、これの無いスコープだけを開いて出す（基本設計 6.4）。
	Expensive bool `json:"expensive,omitempty"`
}

// scopesResponse は、scopes の response の body である。
type scopesResponse struct {
	Scopes []scope `json:"scopes"`
}

// variablesArguments は、variables の引数（仕様の VariablesArguments）である。start と count を省き、すべての子を読む。
type variablesArguments struct {
	VariablesReference int `json:"variablesReference"`
}

// variable は、仕様の Variable のうち、使う項目である。VariablesReference が 0 より大きければ子を持つ（同仕様）。
type variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	EvaluateName       string `json:"evaluateName,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

// variablesResponse は、variables の response の body である。
type variablesResponse struct {
	Variables []variable `json:"variables"`
}

// setVariableArguments は、setVariable の引数（仕様の SetVariableArguments）である。
type setVariableArguments struct {
	// VariablesReference は、変数を持つ入れ物（スコープか親の変数）の参照である。
	VariablesReference int    `json:"variablesReference"`
	Name               string `json:"name"`
	Value              string `json:"value"`
}

// setExpressionArguments は、setExpression の引数（仕様の SetExpressionArguments）である。
type setExpressionArguments struct {
	Expression string `json:"expression"`
	Value      string `json:"value"`
	// FrameID は、式を評価するフレームである。
	FrameID int `json:"frameId,omitempty"`
}

// restartArguments は、restart の引数（仕様の RestartArguments）である。
// Arguments は、launch か attach の構成の最新の値である。
type restartArguments struct {
	Arguments any `json:"arguments,omitempty"`
}

// startDebuggingArguments は、逆向きの request の startDebugging の引数（仕様の StartDebuggingRequestArguments）である。
type startDebuggingArguments struct {
	// Configuration は、子のセッションの launch か attach の引数である。変数を含まない（同仕様）。
	Configuration map[string]any `json:"configuration"`
	// Request は、launch か attach である。
	Request string `json:"request"`
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
	// ID は、アダプターが振ったブレークポイントの ID である。無ければ 0。breakpoint の event で、検証の結果が後から変わったことを知らせるのに使う。
	ID int `json:"id,omitempty"`
	// Verified は、ブレークポイントを置けたかどうかである（望んだ位置とは限らない）。
	Verified bool `json:"verified"`
	// Line は、実際に置いた位置の開始の行である。無ければ 0。
	Line int `json:"line,omitempty"`
}

// breakpointEvent は、breakpoint の event の body（仕様の BreakpointEvent）である。
// reason が changed なら、setBreakpoints の応答で返した ID のブレークポイントの検証の結果か位置が変わったことを表す。
type breakpointEvent struct {
	Reason     string     `json:"reason"`
	Breakpoint breakpoint `json:"breakpoint"`
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
	// PresentationHint は、normal、label、subtle のどれかである（仕様の StackFrame）。スレッドの区画は subtle を既定で隠す（FR-290）。
	PresentationHint string `json:"presentationHint,omitempty"`
}

// stackTraceResponse は、stackTrace の response の body である。
type stackTraceResponse struct {
	StackFrames []stackFrame `json:"stackFrames"`
	// TotalFrames は、スタックのフレームの総数である（省かれうる）。
	TotalFrames int `json:"totalFrames,omitempty"`
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
	// SuspendDebuggee は、切断のときにデバッグ対象を止めたままにするかどうかである。常に false を送る（session.disconnect）。
	SuspendDebuggee bool `json:"suspendDebuggee"`
}

// stoppedEvent は、stopped の event の body である。ThreadID は省かれることがある。
type stoppedEvent struct {
	Reason      string `json:"reason"`
	Description string `json:"description,omitempty"`
	ThreadID    *int   `json:"threadId,omitempty"`
	// HitBreakpointIDs は、止まったブレークポイントの ID（setBreakpoints の応答の id）である。
	HitBreakpointIDs []int `json:"hitBreakpointIds,omitempty"`
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
