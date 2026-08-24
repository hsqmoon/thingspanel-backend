package test

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("NSNR_INTEGRATION_TEST") != "1" {
		fmt.Println("skipping Redis integration tests; set NSNR_INTEGRATION_TEST=1 to enable")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
