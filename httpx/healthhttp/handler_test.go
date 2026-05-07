package healthhttp

import (
	"testing"
)

func TestHandler_PanicsOnNilChecker(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when Handler called with nil *health.Checker")
		}
	}()
	_ = Handler(nil)
}
