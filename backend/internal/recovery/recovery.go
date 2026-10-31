// Package recovery is the smallest honest answer to "the run stopped — now
// what?".
//
// A run can end without a result in three ways: the user pressed 停止, the
// control plane restarted (reconciliation), or the agent child died (the
// watchdog). In every case the transcript used to say only that something was
// interrupted. This package owns the two sentences that turn that into a
// recovery path:
//
//   - Survival.Summary says what the checkout actually holds now (dirty count,
//     last commit), and says 无法确认 when it cannot know — a deleted sandbox
//     must never be reported as a clean tree;
//   - ComposeContinue builds the follow-up the 继续 action sends, naming the
//     last request, the cause, and the instruction to verify the working tree
//     before continuing.
//
// Nothing here guesses. The cause comes from the interruption record (an event),
// the survival facts come from git, and a fact that cannot be measured is
// reported as unknown rather than filled in.
package recovery

import (
	"errors"
	"fmt"
	"strings"
)

// The three causes a run can be interrupted by. They are stored in the
// interruption event so the transcript does not have to infer the cause from a
// sentence.
const (
	CauseStop        = "stop"
	CauseRestart     = "restart"
	CauseProcessExit = "process_exit"
)

// The user-visible sentences. They land in the transcript and in
// runs.completion_note, so they are sentences rather than error codes. The
// restart and watchdog sentences predate this feature and are kept verbatim.
const (
	ReasonStop        = "用户停止，本次运行被中断"
	ReasonRestart     = "服务重启，本次运行被中断"
	ReasonProcessGone = "运行进程已退出，本次运行被中断"
)

// Unavailable names why no checkout could be measured.
const (
	UnavailableSandbox   = "sandbox"
	UnavailableWorkspace = "workspace"
)

// ContinueInstruction is the part of the composed message that keeps a
// half-finished run from being restarted blindly.
const ContinueInstruction = "继续之前，请先检查当前状态：运行 git status 和 git log，" +
	"确认工作区改动与最后一次提交；不要重复已经完成的工作，也不要丢弃未提交的改动。"

// maxRequestRunes bounds the repeated request. A user prompt can be a pasted
// log; repeating all of it would spend the next turn's context on the previous
// one. The truncation is stated in the message, never silent.
const maxRequestRunes = 4000

// Interruption is what the event log says about one cut-off run.
type Interruption struct {
	// Cause is one of the Cause* constants, or "" when the record predates the
	// field and its sentence is unrecognised.
	Cause string
	// Reason is the sentence a person reads. Required.
	Reason string
}

// Survival is the measured state of the checkout. Every field is a fact that
// was read, or an explicit "could not be read".
type Survival struct {
	// Unavailable is "" when the checkout could be measured, otherwise
	// UnavailableSandbox or UnavailableWorkspace.
	Unavailable string
	// DirtyKnown reports whether the change list was read at all.
	DirtyKnown bool
	// DirtyCount is the number of paths with staged, unstaged or untracked work.
	DirtyCount int
	// CommitKnown reports whether HEAD could be read.
	CommitKnown bool
	// HasCommit is false for a repository with no commit yet.
	HasCommit  bool
	LastCommit string
}

// Summary is the "what survived" clause of the marker.
func (s Survival) Summary() string {
	switch s.Unavailable {
	case UnavailableSandbox:
		return "沙箱已不存在，工作区改动与最后一次提交无法确认"
	case UnavailableWorkspace:
		return "工作区已不存在，改动与最后一次提交无法确认"
	}
	parts := make([]string, 0, 2)
	switch {
	case !s.DirtyKnown:
		parts = append(parts, "工作区改动无法确认")
	case s.DirtyCount == 0:
		parts = append(parts, "工作区干净")
	case s.DirtyCount == 1:
		parts = append(parts, "工作区有 1 个未提交改动")
	default:
		parts = append(parts, fmt.Sprintf("工作区有 %d 个未提交改动", s.DirtyCount))
	}
	switch {
	case !s.CommitKnown:
		parts = append(parts, "最后一次提交无法确认")
	case !s.HasCommit:
		parts = append(parts, "还没有提交")
	default:
		parts = append(parts, "最后一次提交 "+s.LastCommit)
	}
	return strings.Join(parts, " · ")
}

// CauseFromReason maps a cause sentence to a cause. It exists for records
// written before the cause field existed: matching the exact server sentences is
// evidence, not a guess. An unrecognised sentence returns "" (unknown).
func CauseFromReason(reason string) string {
	switch {
	case strings.Contains(reason, "服务重启"):
		return CauseRestart
	case strings.Contains(reason, "进程已退出"), strings.Contains(reason, "进程退出"):
		return CauseProcessExit
	case strings.Contains(reason, "停止"), strings.Contains(reason, "用户"):
		return CauseStop
	}
	return ""
}

// CauseLabel is the source a person reads ("停止" / "服务重启" / "进程退出").
func CauseLabel(cause string) string {
	switch cause {
	case CauseStop:
		return "停止"
	case CauseRestart:
		return "服务重启"
	case CauseProcessExit:
		return "进程退出"
	}
	return ""
}

// ComposeContinue builds the follow-up sent by 继续.
//
// It is composed server-side for the same reason a review comment is: the
// transcript, the stored message and the agent's prompt must be one string. An
// empty request or reason is refused — the one thing this message may not do is
// pretend it knows what the user was doing.
func ComposeContinue(lastRequest string, in Interruption, survival Survival) (string, error) {
	request := strings.TrimSpace(lastRequest)
	if request == "" {
		return "", errors.New("recovery: the last request in the interrupted run is required")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return "", errors.New("recovery: the interruption reason is required")
	}

	var b strings.Builder
	b.WriteString("继续上次被中断的工作。\n\n")
	b.WriteString("上次的请求：\n")
	runes := []rune(request)
	if len(runes) > maxRequestRunes {
		b.WriteString(string(runes[:maxRequestRunes]))
		b.WriteString("\n…（上次请求过长，这里只保留开头；完整内容在会话记录里）")
	} else {
		b.WriteString(request)
	}
	b.WriteString("\n\n中断原因：")
	b.WriteString(reason)
	if label := CauseLabel(in.Cause); label != "" {
		fmt.Fprintf(&b, "（来源：%s）", label)
	}
	b.WriteString("\n")
	if summary := survival.Summary(); summary != "" {
		fmt.Fprintf(&b, "当前工作区：%s\n", summary)
	}
	b.WriteString("\n")
	b.WriteString(ContinueInstruction)
	b.WriteString("\n")
	return b.String(), nil
}
