package initialize

import (
	"errors"
	"os"
	"testing"

	"project/pkg/global"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCheckVersionRollsBackAndReturnsMigrationFailure(t *testing.T) {
	previousVersion, previousVersionNumber := global.VERSION, global.VERSION_NUMBER
	global.VERSION, global.VERSION_NUMBER = "0.0.25", 25
	t.Cleanup(func() {
		global.VERSION, global.VERSION_NUMBER = previousVersion, previousVersionNumber
	})
	workingDirectory, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(".."))
	t.Cleanup(func() { require.NoError(t, os.Chdir(workingDirectory)) })

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(int64(0x4e534e525450)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT EXISTS").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT (.+) FROM \"sys_version\"").
		WillReturnRows(sqlmock.NewRows([]string{"version_number"}).AddRow(24))
	migrationFailure := errors.New("forced migration failure")
	mock.ExpectExec("CREATE TABLE public.device_batch_outbox").WillReturnError(migrationFailure)
	mock.ExpectRollback()
	mock.ExpectClose()

	err = CheckVersion(db)
	require.ErrorIs(t, err, migrationFailure)
	require.NoError(t, sqlDB.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
