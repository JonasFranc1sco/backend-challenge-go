package main

import (
	"testing"

	"go.uber.org/fx"
)

func TestAppDependencyGraph(t *testing.T) {
	if err := fx.ValidateApp(AppModules); err != nil {
		t.Fatalf("failed to validate Uber Fx application dependency graph: %v", err)
	}
}
