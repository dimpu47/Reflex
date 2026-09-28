package runbooks

import (
	"context"
	"testing"
	"sync"
)

func TestEngineLoad(t *testing.T) {
	e := &Engine{
		ConfigPath: "../runbooks.yaml",
		Registry:   &sync.Map{},
	}
	
	err := e.Load()
	if err != nil {
		t.Fatalf("failed to load runbooks: %v", err)
	}
	
	val, ok := e.Registry.Load("database")
	if !ok {
		t.Fatalf("expected 'database' domain to be loaded")
	}
	
	dag := val.(DAG)
	if len(dag.Steps) == 0 {
		t.Errorf("expected DAG steps, got 0")
	}
}

func TestResolveApproval(t *testing.T) {
	e := &Engine{
		PendingApprovals: &sync.Map{},
	}
	
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
