package torm

// ErrInvalid 参数无效错误（无效参数异常）。
type ErrInvalid struct{ Msg string }

func (e ErrInvalid) Error() string { return e.Msg }

// ErrNotFound 记录未找到错误（firstOrFail/sole 未命中）。
type ErrNotFound struct{ Msg string }

func (e ErrNotFound) Error() string { return e.Msg }

// ErrMultiple 记录命中多行错误（sole 期望单行时命中多行）。
type ErrMultiple struct{ Msg string }

func (e ErrMultiple) Error() string { return e.Msg }
