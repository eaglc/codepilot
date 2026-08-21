package session

import "testing"

func TestClassifyTurnStatus(t *testing.T) {
	tests := []struct {
		name      string
		cancelled bool
		failed    bool
		hasPatch  bool
		outcome   CheckOutcome
		want      TurnStatus
	}{
		{name: "cancelled dominates passed checks", cancelled: true, hasPatch: true, outcome: CheckPassed, want: TurnCancelled},
		{name: "passed with patch is verified", hasPatch: true, outcome: CheckPassed, want: TurnVerified},
		{name: "not run with patch is unverified", hasPatch: true, outcome: CheckNotRun, want: TurnUnverified},
		{name: "denied with patch is unverified", hasPatch: true, outcome: CheckDenied, want: TurnUnverified},
		{name: "timeout with patch is unverified", hasPatch: true, outcome: CheckTimedOut, want: TurnUnverified},
		{name: "inconclusive with patch is unverified", hasPatch: true, outcome: CheckInconclusive, want: TurnUnverified},
		{name: "failed check with patch is failed", hasPatch: true, outcome: CheckFailed, want: TurnFailed},
		{name: "completed without patch stays neutral", outcome: CheckPassed, want: TurnCompleted},
		{name: "agent error without patch is failed", failed: true, outcome: CheckNotRun, want: TurnFailed},
		{name: "cancelled outcome is cancelled", hasPatch: true, outcome: CheckCancelled, want: TurnCancelled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyTurnStatus(test.cancelled, test.failed, test.hasPatch, test.outcome)
			if got != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestTitleFromMessage(t *testing.T) {
	message := "  第一行标题\n第二行不会进入标题"
	if got := titleFromMessage(message); got != "第一行标题" {
		t.Fatalf("got %q, want first trimmed line", got)
	}

	long := []rune("这是一个很长的标题")
	for len(long) < 90 {
		long = append(long, '字')
	}
	if got := []rune(titleFromMessage(string(long))); len(got) != 80 {
		t.Fatalf("got %d runes, want 80", len(got))
	}
}
