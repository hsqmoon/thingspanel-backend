// /initialize/croninit/cron.go
package croninit

import (
	"context"
	"time"

	"project/internal/service"

	"github.com/robfig/cron"
	"github.com/sirupsen/logrus"
)

var c = cron.New()

func runAutomationCronTask(name string, task func() error) {
	logrus.Debugf("【定时任务】%s开始", name)
	if err := task(); err != nil {
		logrus.WithError(err).Errorf("【定时任务】%s失败", name)
	}
}

// 定义任务初始化
func CronInit() {
	// 初始化设备统计定时任务
	InitDeviceStatsCron(c)
	if err := c.AddFunc("*/5 * * * * *", func() {
		if err := service.GroupApp.Device.DeliverPendingDeviceBatchNotifications(50); err != nil {
			logrus.WithError(err).Info("device batch outbox delivery remains pending for retry")
		}
	}); err != nil {
		logrus.Error("device batch outbox worker registration failed: ", err)
	}
	if err := c.AddFunc("*/5 * * * * *", func() {
		if err := service.NewMarketBundleInstallService().DeliverPendingNotifications(context.Background(), 20); err != nil {
			logrus.WithError(err).Info("market installation notification remains pending for retry")
		}
	}); err != nil {
		logrus.Error("market installation notification worker registration failed: ", err)
	}
	if err := c.AddFunc("0 15 3 * * *", func() {
		if err := service.GroupApp.Device.CleanupDeliveredDeviceBatchOutbox(); err != nil {
			logrus.WithError(err).Error("device batch outbox cleanup failed")
		}
	}); err != nil {
		logrus.Error("device batch outbox cleanup registration failed: ", err)
	}
	if err := c.AddFunc("*/5 * * * * *", func() {
		if err := service.GroupApp.DashboardDelete.DeliverPending(20); err != nil {
			logrus.Error("dashboard delete delivery failed: ", err)
		}
	}); err != nil {
		logrus.Error("dashboard delete worker registration failed: ", err)
	}
	if err := c.AddFunc("0 25 3 * * *", func() {
		if err := service.GroupApp.DashboardDelete.CleanupDelivered(30 * 24 * time.Hour); err != nil {
			logrus.WithError(err).Error("dashboard delete cleanup failed")
		}
	}); err != nil {
		logrus.Error("dashboard delete cleanup registration failed: ", err)
	}
	c.AddFunc("0 */5 * * * *", func() {
		if err := service.RunNSNRHourlyAggregate(); err != nil {
			logrus.Error("NSNR hourly aggregation failed: ", err)
		}
	})
	c.AddFunc("0 10 0 * * *", func() {
		if err := service.RunNSNRDailyAggregate(); err != nil {
			logrus.Error("NSNR daily aggregation failed: ", err)
		}
	})
	c.AddFunc("0 30 2 * * *", func() {
		if err := service.RunNSNRRetention(); err != nil {
			logrus.Error("NSNR retention cleanup failed: ", err)
		}
	})

	// 单次定义成任务 - 每5秒执行一次
	if err := c.AddFunc("*/5 * * * * *", func() {
		runAutomationCronTask("自动化单次任务", service.GroupApp.OnceTaskExecute)
	}); err != nil {
		logrus.WithError(err).Error("【定时任务】自动化单次任务注册失败")
	}

	// 重复定义成任务 - 每5秒执行一次
	if err := c.AddFunc("*/5 * * * * *", func() {
		runAutomationCronTask("自动化重复时间任务", service.GroupApp.PeriodicTaskExecute)
	}); err != nil {
		logrus.WithError(err).Error("【定时任务】自动化重复时间任务注册失败")
	}

	// 每天凌晨2点执行数据清理
	c.AddFunc("0 2 * * *", func() {
		logrus.Debug("【定时任务】系统数据清理任务开始：")
		service.GroupApp.CleanSystemDataByCron()
	})

	// 每天凌晨1点执行脚本
	c.AddFunc("0 1 * * *", func() {
		logrus.Debug("【定时任务】每天凌晨1点执行脚本任务开始：")
		service.GroupApp.RunScript()
	})
	// 每天凌晨
	err := c.AddFunc("2 0 * * * *", func() {
		logrus.Debug("【定时任务】消息推送清理任务开始：", time.Now())
		service.GroupApp.MessagePush.MessagePushMangeClear()
	})
	if err != nil {
		logrus.Error("【定时任务】消息推送清理任务启动失败")
	}
	c.Start()
}
