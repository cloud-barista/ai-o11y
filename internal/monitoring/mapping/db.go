package mapping

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DBConfig is where the MariaDB holding the target table is.
type DBConfig struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	// ConnectTimeout, ReadTimeout and WriteTimeout bound one connection
	// attempt and each read and write on a connection, so an unresponsive
	// database fails a request instead of holding it. Zero takes the
	// Default ones.
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

// Default database timeouts.
const (
	DefaultConnectTimeout = 5 * time.Second
	DefaultReadTimeout    = 30 * time.Second
	DefaultWriteTimeout   = 30 * time.Second
)

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// dsn builds the driver connection string. It goes through the driver's own
// Config so a password with special characters needs no escaping by hand.
func (c DBConfig) dsn() string {
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg.DBName = c.Name
	cfg.User = c.User
	cfg.Passwd = c.Password
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	cfg.Timeout = orDefault(c.ConnectTimeout, DefaultConnectTimeout)
	cfg.ReadTimeout = orDefault(c.ReadTimeout, DefaultReadTimeout)
	cfg.WriteTimeout = orDefault(c.WriteTimeout, DefaultWriteTimeout)
	return cfg.FormatDSN()
}

// targetRow is one target in the common model. EntityID is the management
// platform's key, so a target keeps its row for its whole life. Keys and
// attributes compare byte for byte (utf8mb4_bin): a selector matches exactly
// the value it names, as the mapping layer defines.
type targetRow struct {
	EntityID     string `gorm:"primaryKey;type:varchar(64) COLLATE utf8mb4_bin"`
	Platform     string `gorm:"size:32;not null"`
	Kind         string `gorm:"size:32;not null"`
	Name         string `gorm:"size:255"`
	Provider     string `gorm:"size:64"`
	Region       string `gorm:"size:64"`
	OS           string `gorm:"column:os;size:32"`
	GPUCount     int    `gorm:"column:gpu_count"`
	GPUModel     string `gorm:"column:gpu_model;size:255"`
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

func (targetRow) TableName() string { return "targets" }

// attributeRow is one attribute a selector can match: every common field and
// every platform attribute of a target, one row each. The (attr, value) index
// answers "which targets have namespace=team-a" without knowing in advance
// which attributes a platform has.
type attributeRow struct {
	EntityID string `gorm:"primaryKey;type:varchar(64) COLLATE utf8mb4_bin"`
	Attr     string `gorm:"primaryKey;type:varchar(64) COLLATE utf8mb4_bin;index:idx_attr_value,priority:1"`
	Value    string `gorm:"type:varchar(255) COLLATE utf8mb4_bin;not null;index:idx_attr_value,priority:2"`
}

func (attributeRow) TableName() string { return "target_attributes" }

// OpenDB connects to MariaDB and brings the target tables up to date, the way
// CB-Tumblebug brings its own tables up at start.
func OpenDB(cfg DBConfig) (*gorm.DB, error) {
	if cfg.Host == "" {
		return nil, errors.New("database host is not set")
	}
	db, err := gorm.Open(gormmysql.Open(cfg.dsn()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the database: %w", err)
	}
	if err := db.AutoMigrate(&targetRow{}, &attributeRow{}); err != nil {
		return nil, fmt.Errorf("failed to create the target tables: %w", err)
	}
	return db, nil
}
