package service

import (
	"testing"

	"project/internal/model"
)

func TestGetServiceSelectRejectsInvalidDeviceTypeBeforeQuery(t *testing.T) {
	invalid := 9
	_, err := (&ServicePlugin{}).GetServiceSelect(&model.GetServiceSelectReq{DeviceType: &invalid})
	if err == nil {
		t.Fatal("expected invalid device type error")
	}
}

func TestProtocolPluginRejectsUnsupportedDeviceType(t *testing.T) {
	_, err := (&ProtocolPlugin{}).GetDevicesByProtocolPlugin(model.GetDevicesByProtocolPluginReq{DeviceType: "2"})
	if err == nil {
		t.Fatal("expected unsupported device type error")
	}
}
