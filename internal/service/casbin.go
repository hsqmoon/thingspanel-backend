package service

import (
	"errors"
	"fmt"

	global "project/pkg/global"
)

var errCasbinNotInitialized = errors.New("casbin enforcer is not initialized")

type Casbin struct {
}

// 角色添加多个功能
func (*Casbin) AddFunctionToRole(role string, functions []string) (bool, error) {
	if global.CasbinEnforcer == nil {
		return false, errCasbinNotInitialized
	}
	var rules [][]string
	for _, function := range functions {
		rule := []string{role, function, "allow"}
		rules = append(rules, rule)
	}
	return global.CasbinEnforcer.AddNamedPolicies("p", rules)
}

// 查询角色的功能
func (*Casbin) GetFunctionFromRole(role string) ([]string, error) {
	if global.CasbinEnforcer == nil {
		return nil, errCasbinNotInitialized
	}
	policys := global.CasbinEnforcer.GetFilteredPolicy(0, role)
	var functions []string
	for _, policy := range policys {
		if len(policy) < 2 {
			return nil, fmt.Errorf("invalid casbin permission policy for role %s", role)
		}
		functions = append(functions, policy[1])
	}
	return functions, nil
}

// 删除角色和功能
func (*Casbin) RemoveRoleAndFunction(role string) (bool, error) {
	if global.CasbinEnforcer == nil {
		return false, errCasbinNotInitialized
	}
	return global.CasbinEnforcer.RemoveFilteredPolicy(0, role)
}

// 用户添加多个角色
func (*Casbin) AddRolesToUser(user string, roles []string) (bool, error) {
	if global.CasbinEnforcer == nil {
		return false, errCasbinNotInitialized
	}
	var rules [][]string
	for _, role := range roles {
		rule := []string{user, role}
		rules = append(rules, rule)
	}
	return global.CasbinEnforcer.AddNamedGroupingPolicies("g", rules)
}

// 查询用户的角色
func (*Casbin) GetRoleFromUser(user string) ([]string, error) {
	if global.CasbinEnforcer == nil {
		return nil, errCasbinNotInitialized
	}
	policys := global.CasbinEnforcer.GetFilteredNamedGroupingPolicy("g", 0, user)
	var roles []string
	for _, policy := range policys {
		if len(policy) < 2 {
			return nil, fmt.Errorf("invalid casbin role policy for user %s", user)
		}
		roles = append(roles, policy[1])
	}
	return roles, nil
}

// 删除用户和角色
func (*Casbin) RemoveUserAndRole(user string) (bool, error) {
	if global.CasbinEnforcer == nil {
		return false, errCasbinNotInitialized
	}
	return global.CasbinEnforcer.RemoveFilteredNamedGroupingPolicy("g", 0, user)
}

// 查询是否存在某个资源
func (*Casbin) GetUrl(url string) (bool, error) {
	if global.CasbinEnforcer == nil {
		return false, errCasbinNotInitialized
	}
	stringList := global.CasbinEnforcer.GetFilteredNamedGroupingPolicy("g2", 0, url)
	return len(stringList) != 0, nil
}

// 查询用户角色中是否存在某个角色
func (*Casbin) HasRole(role string) (bool, error) {
	if global.CasbinEnforcer == nil {
		return false, errCasbinNotInitialized
	}
	stringList := global.CasbinEnforcer.GetFilteredNamedGroupingPolicy("g", 1, role)
	return len(stringList) != 0, nil
}

// 校验
func (*Casbin) Verify(user string, url string) (bool, error) {
	if global.CasbinEnforcer == nil {
		return false, errCasbinNotInitialized
	}
	return global.CasbinEnforcer.Enforce(user, url, "allow")
}
