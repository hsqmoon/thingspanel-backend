package api

import (
	model "project/internal/model"
	service "project/internal/service"
	"project/pkg/errcode"

	"github.com/gin-gonic/gin"
)

type CasbinApi struct{}

var casbinService = service.GroupApp.Casbin

// AddFunctionToRole 角色添加多个权限
// @Router   /api/v1/casbin/function [post]
func (*CasbinApi) AddFunctionToRole(c *gin.Context) {
	var req model.FunctionsRoleValidate
	if !BindAndValidate(c, &req) {
		return
	}

	ok, err := casbinService.AddFunctionToRole(req.RoleID, req.FunctionsIDs)
	if err != nil {
		c.Error(err)
		return
	}
	if !ok {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"role_id":      req.RoleID,
			"function_ids": req.FunctionsIDs,
			"error":        "AddFunctionToRole failed",
		}))
		return
	}

	c.Set("data", nil)
}

// GetFunctionFromRole 查询角色的权限
// @Router   /api/v1/casbin/function [get]
func (*CasbinApi) HandleFunctionFromRole(c *gin.Context) {
	var req model.RoleValidate
	if !BindAndValidate(c, &req) {
		return
	}

	roles, err := casbinService.GetFunctionFromRole(req.RoleID)
	if err != nil {
		c.Error(err)
		return
	}

	c.Set("data", roles)
}

// UpdateFunctionFromRole 修改角色的权限
// @Router   /api/v1/casbin/function [put]
func (*CasbinApi) UpdateFunctionFromRole(c *gin.Context) {
	var req model.FunctionsRoleValidate
	if !BindAndValidate(c, &req) {
		return
	}

	if req.RoleID == "" && req.FunctionsIDs == nil {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"role_id":      req.RoleID,
			"function_ids": req.FunctionsIDs,
			"error":        "UpdateFunctionFromRole failed",
		}))
		return
	}

	f, err := casbinService.GetFunctionFromRole(req.RoleID)
	if err != nil {
		c.Error(err)
		return
	}
	if len(f) > 0 {
		//没有记录删除会返回false
		ok, err := casbinService.RemoveRoleAndFunction(req.RoleID)
		if err != nil {
			c.Error(err)
			return
		}
		if !ok {
			c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
				"role_id": req.RoleID,
				"error":   "RemoveRoleAndFunction failed",
			}))
			return
		}
	}
	ok, err := casbinService.AddFunctionToRole(req.RoleID, req.FunctionsIDs)
	if err != nil {
		c.Error(err)
		return
	}
	if !ok {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"role_id":      req.RoleID,
			"function_ids": req.FunctionsIDs,
			"error":        "AddFunctionToRole failed",
		}))
		return
	}
	c.Set("data", nil)
}

// DeleteFunctionFromRole 删除角色的权限
// @Router   /api/v1/casbin/function/{id} [delete]
func (*CasbinApi) DeleteFunctionFromRole(c *gin.Context) {
	id := c.Param("id")
	ok, err := casbinService.RemoveRoleAndFunction(id)
	if err != nil {
		c.Error(err)
		return
	}
	if !ok {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"role_id": id,
			"error":   "RemoveRoleAndFunction failed",
		}))
		return
	}
	c.Set("data", nil)
}

// AddRoleToUser 用户添加多个角色
// @Router   /api/v1/casbin/user [post]
func (*CasbinApi) AddRoleToUser(c *gin.Context) {
	var req model.RolesUserValidate
	if !BindAndValidate(c, &req) {
		return
	}

	ok, err := casbinService.AddRolesToUser(req.UserID, req.RolesIDs)
	if err != nil {
		c.Error(err)
		return
	}
	if !ok {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"user_id": req.UserID,
			"role_id": req.RolesIDs,
			"error":   "AddRolesToUser failed",
		}))
		return
	}

	c.Set("data", nil)

}

// GetRolesFromUser 查询用户的角色
// @Router   /api/v1/casbin/user [get]
func (*CasbinApi) HandleRolesFromUser(c *gin.Context) {
	var req model.UserValidate
	if !BindAndValidate(c, &req) {
		return
	}

	roles, err := casbinService.GetRoleFromUser(req.UserID)
	if err != nil {
		c.Error(err)
		return
	}

	c.Set("data", roles)

}

// UpdateRolesFromUser 修改用户的角色
// @Router   /api/v1/casbin/user [put]
func (*CasbinApi) UpdateRolesFromUser(c *gin.Context) {
	var req model.RolesUserValidate
	if !BindAndValidate(c, &req) {
		return
	}

	if req.UserID == "" && req.RolesIDs == nil {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"user_id": req.UserID,
			"role_id": req.RolesIDs,
			"error":   "UpdateRolesFromUser failed",
		}))
		return
	}

	if _, err := casbinService.RemoveUserAndRole(req.UserID); err != nil {
		c.Error(err)
		return
	}
	ok, err := casbinService.AddRolesToUser(req.UserID, req.RolesIDs)
	if err != nil {
		c.Error(err)
		return
	}
	if !ok {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"user_id": req.UserID,
			"role_id": req.RolesIDs,
			"error":   "AddRolesToUser failed",
		}))
		return
	}
	c.Set("data", nil)
}

// DeleteRolesFromUser 删除用户的角色
// @Router   /api/v1/casbin/user/{id} [delete]
func (*CasbinApi) DeleteRolesFromUser(c *gin.Context) {
	id := c.Param("id")
	ok, err := casbinService.RemoveUserAndRole(id)
	if err != nil {
		c.Error(err)
		return
	}
	if !ok {
		c.Error(errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"user_id": id,
			"error":   "RemoveUserAndRole failed",
		}))
		return
	}
	c.Set("data", nil)
}
