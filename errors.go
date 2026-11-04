package torm

// ErrInvalid 对应 Laravel 的 InvalidArgumentException。
type ErrInvalid struct{ Msg string }

func (e ErrInvalid) Error() string { return e.Msg }

// ErrNotFound 对应 Laravel 的 RecordNotFoundException（firstOrFail/sole 未命中）。
type ErrNotFound struct{ Msg string }

func (e ErrNotFound) Error() string { return e.Msg }

// ErrMultiple 对应 Laravel 的 MultipleRecordsFoundException（sole 命中多行）。
type ErrMultiple struct{ Msg string }

func (e ErrMultiple) Error() string { return e.Msg }
