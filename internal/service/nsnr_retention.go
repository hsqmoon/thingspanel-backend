package service

import (
	"time"

	"project/pkg/global"
)

func RunNSNRHourlyAggregate() error {
	end := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	start := end - int64(time.Hour/time.Millisecond)
	return global.DB.Exec(`INSERT INTO telemetry_hourly(device_id,"key",bucket_ts,avg_number,min_number,max_number,last_number,last_bool,last_string,sample_count)
SELECT device_id,"key",?,avg(number_v),min(number_v),max(number_v),last(number_v,ts),last(bool_v,ts),last(string_v,ts),count(*)
FROM telemetry_datas WHERE ts>=? AND ts<? GROUP BY device_id,"key"
ON CONFLICT(device_id,"key",bucket_ts) DO UPDATE SET avg_number=excluded.avg_number,min_number=excluded.min_number,max_number=excluded.max_number,last_number=excluded.last_number,last_bool=excluded.last_bool,last_string=excluded.last_string,sample_count=excluded.sample_count`, start, start, end).Error
}

func RunNSNRDailyAggregate() error {
	now := time.Now().UTC()
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
	start := end - int64(24*time.Hour/time.Millisecond)
	return global.DB.Exec(`INSERT INTO telemetry_daily(device_id,"key",bucket_ts,avg_number,min_number,max_number,last_number,last_bool,last_string,sample_count)
SELECT device_id,"key",?,sum(avg_number*sample_count)/nullif(sum(sample_count),0),min(min_number),max(max_number),last(last_number,bucket_ts),last(last_bool,bucket_ts),last(last_string,bucket_ts),sum(sample_count)
FROM telemetry_hourly WHERE bucket_ts>=? AND bucket_ts<? GROUP BY device_id,"key"
ON CONFLICT(device_id,"key",bucket_ts) DO UPDATE SET avg_number=excluded.avg_number,min_number=excluded.min_number,max_number=excluded.max_number,last_number=excluded.last_number,last_bool=excluded.last_bool,last_string=excluded.last_string,sample_count=excluded.sample_count`, start, start, end).Error
}

func RunNSNRRetention() error {
	rawCutoff := time.Now().UTC().Add(-90 * 24 * time.Hour).UnixMilli()
	hourlyCutoff := time.Now().UTC().AddDate(-2, 0, 0).UnixMilli()
	if err := global.DB.Exec(`DELETE FROM telemetry_datas WHERE ts<?`, rawCutoff).Error; err != nil {
		return err
	}
	return global.DB.Exec(`DELETE FROM telemetry_hourly WHERE bucket_ts<?`, hourlyCutoff).Error
}
