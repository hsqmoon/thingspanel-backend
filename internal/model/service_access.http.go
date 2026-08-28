package model

type CreateAccessReq struct {
	Name                string `json:"name" binding:"required"`
	ServicePluginID     string `json:"service_plugin_id" binding:"required"`
	Voucher             string `json:"voucher" binding:"required"`
	Description         string `json:"description"`
	ServiceAccessConfig string `json:"service_access_config"`
	Remark              string `json:"remark" `
}

type UpdateAccessReq struct {
	ID                  string  `json:"id" binding:"required"`
	IdempotencyKey      string  `json:"idempotency_key" binding:"required,uuid"`
	ServiceAccessConfig *string `json:"service_access_config"`
	Name                *string `json:"name"`
	Voucher             *string `json:"voucher"`
}

type DeleteAccessReq struct {
	ID string `json:"id" form:"id" binding:"required"`
}

type GetServiceAccessByPageReq struct {
	PageReq
	ServicePluginID string `json:"service_plugin_id" form:"service_plugin_id"`
}

type GetServiceAccessVoucherFormReq struct {
	ServicePluginID string `json:"service_plugin_id" form:"service_plugin_id"  binding:"required"`
}

// 服务接入点设备列表 service_access_id page_size page
type ServiceAccessDeviceListReq struct {
	PageReq
	ServiceAccessID string `json:"service_access_id" form:"service_access_id" binding:"required"`
}

type GetPluginServiceAccessListReq struct {
	ServiceIdentifier string `json:"service_identifier" form:"service_identifier" binding:"required"`
}

type GetPluginServiceAccessReq struct {
	ServiceAccessID string `json:"service_access_id" form:"service_access_id" binding:"required"`
}
