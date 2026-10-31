package recovery

import (
	"strings"
	"testing"
)

// TestComposeContinueCarriesTheRequestTheCauseAndTheVerifyInstruction is the
// promise of the 继续 button: the next agent is told what it was doing, why the
// previous turn stopped, and that it must read the current state before doing
// anything. All three guests are load-bearing; a message that names the request
// but not the interruption reads like a new instruction, and one that does not
// ask for a state check is how the work gets repeated or the dirty tree lost.
func TestComposeContinueCarriesTheRequestTheCauseAndTheVerifyInstruction(t *testing.T) {
	const request = "修一下缓存淘汰的竞态，并补一个测试"
	survival := Survival{
		DirtyKnown: true, DirtyCount: 2,
		CommitKnown: true, HasCommit: true, LastCommit: "e3f1a2b",
	}
	cases := []struct {
		name  string
		cause string
		want  string
		label string
	}{
		{name: "stop", cause: CauseStop, want: ReasonStop, label: "停止"},
		{name: "restart", cause: CauseRestart, want: ReasonRestart, label: "服务重启"},
		{name: "process exit", cause: CauseProcessExit, want: ReasonProcessGone, label: "进程退出"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := ComposeContinue(request, Interruption{Cause: tc.cause, Reason: tc.want}, survival)
			if err != nil {
				t.Fatalf("ComposeContinue: %v", err)
			}
			for _, want := range []string{
				request,
				tc.want,
				"来源：" + tc.label,
				ContinueInstruction,
				"工作区有 2 个未提交改动",
				"最后一次提交 e3f1a2b",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("composed message is missing %q:\n%s", want, text)
				}
			}
			if !strings.Contains(text, "继续上次被中断的工作") {
				t.Fatalf("the message does not say it continues interrupted work:\n%s", text)
			}
		})
	}
}

// A request is the one thing that cannot be reconstructed. Refusing is honest;
// composing "继续上次的工作" without saying what it was is the failure mode this
// whole feature exists to remove.
func TestComposeContinueRefusesWithoutTheLastRequest(t *testing.T) {
	if _, err := ComposeContinue("   ", Interruption{Cause: CauseRestart, Reason: ReasonRestart}, Survival{}); err == nil {
		t.Fatal("ComposeContinue accepted an empty request")
	}
	if _, err := ComposeContinue("do the thing", Interruption{}, Survival{}); err == nil {
		t.Fatal("ComposeContinue accepted an interruption with no reason")
	}
}

// TestSurvivalSummaryStatesWhatActuallySurvived covers the facts the marker
// shows. The sandbox case must not fall back to "工作区干净" or invent a commit:
// when the checkout is gone, the only honest answer is 无法确认.
func TestSurvivalSummaryStatesWhatActuallySurvived(t *testing.T) {
	cases := []struct {
		name     string
		in       Survival
		wants    []string
		refuses  []string
	}{
		{
			name: "dirty work and a commit",
			in: Survival{DirtyKnown: true, DirtyCount: 2, CommitKnown: true, HasCommit: true, LastCommit: "e3f1a2b"},
			wants: []string{"工作区有 2 个未提交改动", "最后一次提交 e3f1a2b"},
		},
		{
			name: "clean tree",
			in: Survival{DirtyKnown: true, CommitKnown: true, HasCommit: true, LastCommit: "abc1234"},
			wants: []string{"工作区干净", "最后一次提交 abc1234"},
			refuses: []string{"0 个未提交改动"},
		},
		{
			name: "no commit yet",
			in: Survival{DirtyKnown: true, DirtyCount: 1, CommitKnown: true},
			wants: []string{"工作区有 1 个未提交改动", "还没有提交"},
		},
		{
			name: "commit unreadable",
			in: Survival{DirtyKnown: true, CommitKnown: false},
			wants: []string{"工作区干净", "最后一次提交无法确认"},
			refuses: []string{"最后一次提交 "},
		},
		{
			name: "changes unreadable",
			in: Survival{DirtyKnown: false, CommitKnown: true, HasCommit: true, LastCommit: "def5678"},
			wants: []string{"工作区改动无法确认", "最后一次提交 def5678"},
			refuses: []string{"工作区干净"},
		},
		{
			name: "sandbox gone",
			in: Survival{Unavailable: UnavailableSandbox},
			wants: []string{"沙箱已不存在", "无法确认"},
			refuses: []string{"工作区干净", "最后一次提交 e3f1a2b"},
		},
		{
			name: "workspace gone",
			in: Survival{Unavailable: UnavailableWorkspace},
			wants: []string{"工作区已不存在", "无法确认"},
			refuses: []string{"工作区干净"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Summary()
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Fatalf("summary %q is missing %q", got, want)
				}
			}
			for _, refuse := range tc.refuses {
				if strings.Contains(got, refuse) {
					t.Fatalf("summary %q must not say %q", got, refuse)
				}
			}
		})
	}
}

// Old events predate the cause field. Their sentence is still exact evidence;
// reading the cause from it is not a guess, and an unrecognised sentence stays
// unknown rather than being forced into one of the three buckets.
func TestCauseFromReason(t *testing.T) {
	cases := []struct{ reason, want string }{
		{ReasonStop, CauseStop},
		{ReasonRestart, CauseRestart},
		{ReasonProcessGone, CauseProcessExit},
		{"", ""},
		{"something else", ""},
	}
	for _, tc := range cases {
		if got := CauseFromReason(tc.reason); got != tc.want {
			t.Fatalf("CauseFromReason(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
	if CauseLabel(CauseRestart) != "服务重启" || CauseLabel(CauseStop) != "停止" ||
		CauseLabel(CauseProcessExit) != "进程退出" {
		t.Fatalf("cause labels changed: %q %q %q",
			CauseLabel(CauseStop), CauseLabel(CauseRestart), CauseLabel(CauseProcessExit))
	}
}
