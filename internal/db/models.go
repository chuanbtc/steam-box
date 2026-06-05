package db

import (
	"time"

	"gorm.io/gorm"
)

// User represents an admin/agent account.
type User struct {
	ID           string `gorm:"primaryKey;size:36"`
	Username     string `gorm:"uniqueIndex;size:64;not null"`
	PasswordHash string `gorm:"size:128;not null"`
	Salt         string `gorm:"size:32;not null"`
	Role         string `gorm:"size:16;not null;default:agent"` // superadmin, agent
	Balance      float64
	Enabled      bool `gorm:"default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CDKey represents an activation key.
type CDKey struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	Code        string `gorm:"uniqueIndex;size:19;not null"` // XXXX-XXXX-XXXX-XXXX
	AppID       string `gorm:"size:16;not null"`
	GameName    string `gorm:"size:256"`
	AgentID     string `gorm:"size:36;index"`
	CreatedBy   string `gorm:"size:64"`
	Note        string `gorm:"size:256"`
	Used        bool   `gorm:"default:false"`
	UsedAt      *time.Time
	UsedMachine string `gorm:"size:128"`
	Revoked     bool   `gorm:"default:false"`
	CreatedAt   time.Time
}

// ActivationLog records each CDK redemption attempt.
type ActivationLog struct {
	ID      uint   `gorm:"primaryKey;autoIncrement"`
	CDK     string `gorm:"size:19"`
	AppID   string `gorm:"size:16"`
	Machine string `gorm:"size:128"`
	OK      bool
	At      time.Time
}

// InitDB opens SQLite and auto-migrates all tables.
func InitDB(dbPath string) (*gorm.DB, error) {
	db, err := openSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&User{}, &CDKey{}, &ActivationLog{}); err != nil {
		return nil, err
	}
	return db, nil
}
