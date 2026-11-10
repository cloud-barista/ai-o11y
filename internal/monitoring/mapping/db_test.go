package mapping

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// testDB connects to the MariaDB named by AIO11Y_TEST_DB_* and starts every
// test from empty tables. Without AIO11Y_TEST_DB_HOST the test is skipped, so
// the suite runs anywhere; with it, it runs against a real server.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	host := os.Getenv("AIO11Y_TEST_DB_HOST")
	if host == "" {
		t.Skip("AIO11Y_TEST_DB_HOST is not set")
	}
	port, _ := strconv.Atoi(os.Getenv("AIO11Y_TEST_DB_PORT"))
	if port == 0 {
		port = 3306
	}
	db, err := OpenDB(DBConfig{
		Host: host, Port: port,
		Name:     os.Getenv("AIO11Y_TEST_DB_NAME"),
		User:     os.Getenv("AIO11Y_TEST_DB_USER"),
		Password: os.Getenv("AIO11Y_TEST_DB_PASSWORD"),
	})
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	for _, table := range []string{"target_attributes", "targets"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestOpenDBCreatesTables(t *testing.T) {
	db := testDB(t)
	for _, table := range []string{"targets", "target_attributes"} {
		if !db.Migrator().HasTable(table) {
			t.Errorf("table %s is missing", table)
		}
	}
	if !db.Migrator().HasIndex(&attributeRow{}, "idx_attr_value") {
		t.Error("index idx_attr_value is missing")
	}
}

// TestOpenDBAgain checks that opening an existing database leaves its tables
// as they are, since the service opens it at every start.
func TestOpenDBAgain(t *testing.T) {
	testDB(t)
	testDB(t)
}

func TestOpenDBNeedsHost(t *testing.T) {
	if _, err := OpenDB(DBConfig{}); err == nil {
		t.Fatal("OpenDB without a host succeeded")
	}
}

func TestDSNEscapesPassword(t *testing.T) {
	dsn := DBConfig{Host: "db", Port: 3306, Name: "aio11y", User: "u", Password: "p@ss/w:rd"}.dsn()
	if !strings.HasPrefix(dsn, "u:p@ss/w:rd@tcp(db:3306)/aio11y?") {
		t.Fatalf("dsn = %q", dsn)
	}
	if !strings.Contains(dsn, "parseTime=true") || !strings.Contains(dsn, "charset=utf8mb4") {
		t.Fatalf("dsn lacks parseTime or charset: %q", dsn)
	}
}

func TestKeysAndAttributesAreCaseSensitive(t *testing.T) {
	db := testDB(t)
	for _, table := range []string{"targets", "target_attributes"} {
		var collation string
		if err := db.Raw(`SELECT COLLATION_NAME FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'entity_id'`, table).Scan(&collation).Error; err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if collation != "utf8mb4_bin" {
			t.Errorf("%s.entity_id collation = %q, want utf8mb4_bin", table, collation)
		}
	}
}
