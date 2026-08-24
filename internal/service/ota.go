package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dal "project/internal/dal"
	model "project/internal/model"
	query "project/internal/query"
	"project/mqtt/publish"
	"project/pkg/common"
	global "project/pkg/global"
	utils "project/pkg/utils"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type OTA struct{}

func (*OTA) CreateOTAUpgradePackage(req *model.CreateOTAUpgradePackageReq, tenantID string) (string, error) {
	var ota = model.OtaUpgradePackage{}
	ota.ID = uuid.NewString()
	ota.Name = req.Name
	ota.Version = req.Version
	ota.TargetVersion = req.TargetVersion
	// 临时注释
	ota.DeviceConfigID = req.DeviceConfigID
	ota.Module = req.Module
	ota.PackageType = *req.PackageType
	ota.SignatureType = req.SignatureType

	// 生成文件签名
	fileURL := *req.PackageUrl
	const downloadPrefix = "./api/v1/ota/download/"
	if !strings.HasPrefix(fileURL, downloadPrefix) {
		return "", fmt.Errorf("invalid OTA package URL")
	}
	packagePath := filepath.Clean(strings.TrimPrefix(fileURL, downloadPrefix))
	if packagePath == "." || filepath.IsAbs(packagePath) || strings.HasPrefix(packagePath, "..") {
		return "", fmt.Errorf("invalid OTA package path")
	}
	signature, err := utils.FileSign(packagePath, *req.SignatureType)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(signature, req.ExpectedSHA256) {
		return "", fmt.Errorf("OTA package SHA-256 mismatch")
	}
	ota.Signature = &signature

	ota.AdditionalInfo = req.AdditionalInfo
	defaultAdditionalInfo := "{}"
	if req.AdditionalInfo == nil || *req.AdditionalInfo == "" {
		ota.AdditionalInfo = &defaultAdditionalInfo
	}
	ota.Description = req.Description
	ota.PackageURL = req.PackageUrl
	ota.TenantID = &tenantID

	t := time.Now().UTC()
	ota.CreatedAt = t
	ota.UpdatedAt = &t
	ota.Remark = req.Remark
	err = dal.CreateOtaUpgradePackage(&ota)
	return ota.ID, err
}

func (*OTA) UpdateOTAUpgradePackage(req *model.UpdateOTAUpgradePackageReq) error {

	oldota, err := dal.GetOtaUpgradePackageByID(req.Id)
	if err != nil {
		return err
	}

	var ota = model.OtaUpgradePackage{}
	ota.ID = req.Id

	ota.Name = req.Name
	// ota.Version = req.Version
	// ota.TargetVersion = req.TargetVersion
	// 临时注释
	// ota.DeviceConfigsID = req.DeviceConfigsID
	// ota.Module = req.Module
	// ota.PackageType = *req.PackageType
	// ota.SignatureType = req.SignatureType
	ota.AdditionalInfo = req.AdditionalInfo
	ota.Description = req.Description
	ota.PackageURL = req.PackageUrl
	if req.PackageUrl != nil && (oldota.PackageURL == nil || *req.PackageUrl != *oldota.PackageURL) {
		if req.SignatureType == nil {
			return fmt.Errorf("signature type is required when replacing an OTA package")
		}
		// 生成文件签名
		fileURL := *req.PackageUrl
		const downloadPrefix = "./api/v1/ota/download/"
		if !strings.HasPrefix(fileURL, downloadPrefix) {
			return fmt.Errorf("invalid OTA package URL")
		}
		packagePath := filepath.Clean(strings.TrimPrefix(fileURL, downloadPrefix))
		if packagePath == "." || filepath.IsAbs(packagePath) || strings.HasPrefix(packagePath, "..") {
			return fmt.Errorf("invalid OTA package path")
		}
		signature, err := utils.FileSign(packagePath, *req.SignatureType)
		if err != nil {
			return err
		}
		ota.Signature = &signature
	}

	t := time.Now().UTC()
	ota.UpdatedAt = &t
	ota.Remark = req.Remark
	info, err := dal.UpdateOtaUpgradePackage(&ota)
	if err != nil {
		return err
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("no data updated")
	}
	return nil
}

func (*OTA) DeleteOTAUpgradePackage(packageId string) error {
	ota, err := dal.GetOtaUpgradePackageByID(packageId)
	if err != nil {
		return err
	}
	if err = dal.DeleteOtaUpgradePackage(packageId); err != nil {
		return err
	}
	if ota.PackageURL != nil && strings.HasPrefix(*ota.PackageURL, "./api/v1/ota/download/") {
		packagePath := filepath.Clean(strings.TrimPrefix(*ota.PackageURL, "./api/v1/ota/download/"))
		if packagePath != "." && !filepath.IsAbs(packagePath) && !strings.HasPrefix(packagePath, "..") {
			if removeErr := os.Remove(packagePath); removeErr != nil && !os.IsNotExist(removeErr) {
				logrus.WithError(removeErr).Warn("failed to remove OTA package file")
			}
		}
	}
	return nil
}

func (*OTA) GetOTAUpgradePackageListByPage(req *model.GetOTAUpgradePackageLisyByPageReq, userClaims *utils.UserClaims) (map[string]interface{}, error) {
	total, list, err := dal.GetOtaUpgradePackageListByPage(req, userClaims.TenantID)
	if err != nil {
		return nil, err
	}
	packageListRspMap := make(map[string]interface{})
	packageListRspMap["total"] = total
	packageListRspMap["list"] = list
	return packageListRspMap, nil

}

func (o *OTA) CreateOTAUpgradeTask(req *model.CreateOTAUpgradeTaskReq) (string, error) {
	taskID, tasks, err := dal.CreateOTAUpgradeTaskWithDetail(req)
	if err == nil {
		go func() {
			for _, t := range tasks {
				if pushErr := o.PushOTAUpgradePackage(t); pushErr != nil {
					logrus.WithError(pushErr).WithField("ota_task_detail_id", t.ID).Error("OTA package push failed")
				}
			}
		}()
	}
	return taskID, err
}

func (*OTA) DeleteOTAUpgradeTask(id string) error {
	err := dal.DeleteOTAUpgradeTask(id)
	return err
}

func (*OTA) GetOTAUpgradeTaskListByPage(req *model.GetOTAUpgradeTaskListByPageReq) (map[string]interface{}, error) {
	total, list, err := dal.GetOtaUpgradeTaskListByPage(req)
	if err != nil {
		return nil, err
	}
	dataMap := make(map[string]interface{})
	dataMap["total"] = total
	dataMap["list"] = list
	return dataMap, nil
}

func (*OTA) GetOTAUpgradeTaskDetailListByPage(req *model.GetOTAUpgradeTaskDetailReq) (map[string]interface{}, error) {
	total, list, statistics, err := dal.GetOtaUpgradeTaskDetailListByPage(req)
	if err != nil {
		return nil, err
	}
	dataMap := make(map[string]interface{})
	dataMap["total"] = total
	dataMap["statistics"] = statistics
	dataMap["list"] = list
	return dataMap, nil
}

// 设备状态修改(请求参数1-取消升级 2-重新升级)
// 1-待推送 2-已推送 3-升级中 修改为已取消
// 5-升级失败 修改为待推送
// 4-升级成功 6-已取消 不修改
func (o *OTA) UpdateOTAUpgradeTaskStatus(req *model.UpdateOTAUpgradeTaskStatusReq) error {
	taskDetail, err := query.OtaUpgradeTaskDetail.Where(query.OtaUpgradeTaskDetail.ID.Eq(req.Id)).First()
	if err != nil {
		return err
	}
	// 4-升级成功 6-已取消 不修改
	if taskDetail.Status == 4 || taskDetail.Status == 6 {
		return fmt.Errorf("the task status cannot be modified")
	}
	// 升级成功的任务不能取消升级
	if req.Action == 6 && taskDetail.Status == 5 {
		return fmt.Errorf("the task status cannot be modified")
	}
	// 1-待推送 2-已推送 3-升级中 不能重新升级
	if req.Action == 1 && taskDetail.Status <= 3 {
		return fmt.Errorf("the task is upgrading")
	}
	t := time.Now().UTC()
	if req.Action == 6 {
		//取消升级
		taskDetail.Status = 6
		taskDetail.UpdatedAt = &t
		desc := "手动取消升级"
		taskDetail.StatusDescription = &desc
		_, err := query.OtaUpgradeTaskDetail.Updates(taskDetail)
		return err
	}
	if req.Action == 1 {
		desc := "手动开始重新升级"
		startStep := int16(0)
		//重新升级
		taskDetail.Status = 1
		taskDetail.UpdatedAt = &t
		taskDetail.StatusDescription = &desc
		taskDetail.Step = &startStep

		_, err := query.OtaUpgradeTaskDetail.Updates(taskDetail)
		if err != nil {
			return err
		}
		// 重新升级后推送升级包
		err = o.PushOTAUpgradePackage(taskDetail)
		return err
	}

	return err
}
func (*OTA) PushOTAUpgradePackage(taskDetail *model.OtaUpgradeTaskDetail) error {
	// 查看设备是否在线
	device := &model.Device{}
	device, err := query.Device.Where(query.Device.ID.Eq(taskDetail.DeviceID)).First()
	if err != nil {
		return err
	}
	if device.IsOnline != 1 {
		//修改设备升级任务信息
		taskDetail.Status = 5
		desc := "设备离线"
		taskDetail.StatusDescription = &desc
		t := time.Now().UTC()
		taskDetail.UpdatedAt = &t
		_, err := query.OtaUpgradeTaskDetail.Updates(taskDetail)
		if err != nil {
			return err
		}
		return fmt.Errorf("the device is offline")
	}
	// 查看设备是否有其他升级中的任务
	count, err := query.OtaUpgradeTaskDetail.Where(query.OtaUpgradeTaskDetail.DeviceID.Eq(taskDetail.DeviceID), query.OtaUpgradeTaskDetail.ID.Neq(taskDetail.ID), query.OtaUpgradeTaskDetail.Status.In(2, 3)).Count()
	if err != nil {
		return err
	}
	if count > 0 {
		//修改设备升级任务信息
		taskDetail.Status = 5
		desc := "上次升级未完成"
		taskDetail.StatusDescription = &desc
		t := time.Now().UTC()
		taskDetail.UpdatedAt = &t
		_, err := query.OtaUpgradeTaskDetail.Updates(taskDetail)
		if err != nil {
			return err
		}
		return fmt.Errorf("the device is upgrading")
	}
	// 推送升级包
	taskQuery, err := query.OtaUpgradeTask.Where(query.OtaUpgradeTask.ID.Eq(taskDetail.OtaUpgradeTaskID)).First()
	if err != nil {
		return err
	}
	otapackage, err := query.OtaUpgradePackage.Where(query.OtaUpgradePackage.ID.Eq(taskQuery.OtaUpgradePackageID)).First()
	if err != nil {
		return err
	}
	var otamsg = make(map[string]interface{})
	// 获取随机九位数字并转换为字符串
	randNum, err := common.GetRandomNineDigits()
	if err != nil {
		return err
	}
	otamsg["id"] = randNum
	otamsg["code"] = "200"
	var otamsgparams = make(map[string]interface{})
	otamsgparams["version"] = otapackage.Version
	packagePath := filepath.Clean(strings.TrimPrefix(*otapackage.PackageURL, "./api/v1/ota/download/"))
	packageInfo, err := os.Stat(packagePath)
	if err != nil {
		return err
	}
	otamsgparams["size"] = packageInfo.Size()
	otamsgparams["url"] = global.OtaAddress + strings.TrimPrefix(*otapackage.PackageURL, ".")
	otamsgparams["signMethod"] = otapackage.SignatureType
	otamsgparams["sign"] = otapackage.Signature
	otamsgparams["module"] = otapackage.Module
	//其他配置格式成map
	var m map[string]interface{}
	err = json.Unmarshal([]byte(*otapackage.AdditionalInfo), &m)
	if err != nil {
		logrus.Error(err)
	}
	otamsgparams["extData"] = m
	otamsg["params"] = otamsgparams
	palyload, json_err := json.Marshal(otamsg)
	if json_err != nil {
		logrus.Error(err)
	} else {
		if err = publish.PublishOtaAdress(device.DeviceNumber, palyload); err != nil {
			return err
		}
		taskDetail.Status = 2
		desc := "已通知设备"
		taskDetail.StatusDescription = &desc
		t := time.Now().UTC()
		taskDetail.UpdatedAt = &t
		_, err := query.OtaUpgradeTaskDetail.Updates(taskDetail)
		if err != nil {
			return err
		}
	}

	return nil
}
