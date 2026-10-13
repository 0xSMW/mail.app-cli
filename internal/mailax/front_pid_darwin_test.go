//go:build darwin && cgo

package mailax

import "testing"

func TestFrontPIDReadRequiresSuccessfulPositiveProcess(t *testing.T) {
	for _, tt := range []struct {
		frontStatus, pidStatus, pid int
		want                        bool
	}{
		{0, 0, 74484, true},
		{0, 0, 0, false}, {0, 0, -1, false}, {-1, 0, 74484, false}, {0, -1, 74484, false}, {-1, -1, 0, false},
	} {
		if got := validFrontPIDRead(tt.frontStatus, tt.pidStatus, tt.pid); got != tt.want {
			t.Errorf("foreground statuses %d/%d PID %d accepted=%v want=%v", tt.frontStatus, tt.pidStatus, tt.pid, got, tt.want)
		}
	}
}
