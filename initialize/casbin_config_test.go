package initialize

import (
	"testing"

	"github.com/casbin/casbin/v2"
)

func TestCasbinRequiresExplicitPolicy(t *testing.T) {
	enforcer, err := casbin.NewEnforcer("../configs/casbin.conf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enforcer.AddGroupingPolicy("user-id", "administrator"); err != nil {
		t.Fatal(err)
	}
	if _, err = enforcer.AddNamedGroupingPolicy("g2", "/devices", "device-resource"); err != nil {
		t.Fatal(err)
	}
	if _, err = enforcer.AddPolicy("administrator", "device-resource", "GET"); err != nil {
		t.Fatal(err)
	}

	allowed, err := enforcer.Enforce("user-id", "/devices", "GET")
	if err != nil || !allowed {
		t.Fatalf("explicit policy must allow access: allowed=%v err=%v", allowed, err)
	}
	for _, subject := range []string{"admin@thingspanel.cn", "super@super.cn"} {
		allowed, err = enforcer.Enforce(subject, "/devices", "GET")
		if err != nil {
			t.Fatal(err)
		}
		if allowed {
			t.Fatalf("subject %q bypassed Casbin without a policy", subject)
		}
	}
}
