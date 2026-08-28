package service

import (
	"errors"
	"testing"

	"project/pkg/global"
)

func TestCasbinFailsClosedWhenEnforcerIsUnavailable(t *testing.T) {
	original := global.CasbinEnforcer
	global.CasbinEnforcer = nil
	t.Cleanup(func() { global.CasbinEnforcer = original })

	casbinService := &Casbin{}
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "add function", run: func() error { _, err := casbinService.AddFunctionToRole("role", []string{"function"}); return err }},
		{name: "get function", run: func() error { _, err := casbinService.GetFunctionFromRole("role"); return err }},
		{name: "remove function", run: func() error { _, err := casbinService.RemoveRoleAndFunction("role"); return err }},
		{name: "add role", run: func() error { _, err := casbinService.AddRolesToUser("user", []string{"role"}); return err }},
		{name: "get role", run: func() error { _, err := casbinService.GetRoleFromUser("user"); return err }},
		{name: "remove role", run: func() error { _, err := casbinService.RemoveUserAndRole("user"); return err }},
		{name: "get URL", run: func() error { _, err := casbinService.GetUrl("api/v1/device"); return err }},
		{name: "has role", run: func() error { _, err := casbinService.HasRole("role"); return err }},
		{name: "verify", run: func() error { _, err := casbinService.Verify("user", "api/v1/device"); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, errCasbinNotInitialized) {
				t.Fatalf("expected errCasbinNotInitialized, got %v", err)
			}
		})
	}
}
