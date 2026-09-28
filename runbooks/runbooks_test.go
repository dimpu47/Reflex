package runbooks

import (
	"testing"
)

func TestEngine_Initialization(t *testing.T) {
	e := NewEngine(nil, nil)
	
	if e.wasmRuntime == nil {
		t.Errorf("expected wasmRuntime to be initialized")
	}
}

func TestResolveApproval(t *testing.T) {
	e := NewEngine(nil, nil)
	
	ch := make(chan bool, 1)
	e.PendingApprovals.Store("test-alert", ch)
	
	val, ok := e.PendingApprovals.Load("test-alert")
	if !ok {
		t.Fatal("expected to load pending approval channel")
	}
	loadedCh := val.(chan bool)
	
	// simulate approval
	loadedCh <- true
	
	approved := <-ch
	if !approved {
		t.Error("expected true for approval")
	}
}
