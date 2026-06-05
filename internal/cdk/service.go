package cdk

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"steam-box/internal/db"

	"gorm.io/gorm"
)

var (
	ErrInvalidCount = errors.New("count must be between 1 and 100")
	ErrNotFound     = errors.New("CDK not found")
	ErrAlreadyUsed  = errors.New("CDK has already been used")
	ErrRevoked      = errors.New("CDK has been revoked")
)

// Service manages CDK generation, validation, and redemption.
type Service struct {
	DB *gorm.DB
}

// NewService creates a new CDK service backed by the given database.
func NewService(db *gorm.DB) *Service {
	return &Service{DB: db}
}

// generateCode produces a single XXXX-XXXX-XXXX-XXXX code using crypto/rand.
// Each segment is 4 uppercase alphanumeric characters (0-9, A-Z).
func generateCode() (string, error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const segmentLen = 4
	const segments = 4

	// We need 16 random charset selections. Each byte from crypto/rand is
	// reduced modulo len(charset). To avoid bias we draw extra entropy and
	// use rejection sampling per byte.
	buf := make([]byte, segmentLen*segments)
	for i := range buf {
		b, err := randCharFromSet(charset)
		if err != nil {
			return "", fmt.Errorf("crypto/rand read: %w", err)
		}
		buf[i] = b
	}

	parts := make([]string, segments)
	for i := 0; i < segments; i++ {
		parts[i] = string(buf[i*segmentLen : (i+1)*segmentLen])
	}
	return strings.Join(parts, "-"), nil
}

// randCharFromSet picks a uniformly random byte from charset using rejection
// sampling on crypto/rand output to eliminate modulo bias.
func randCharFromSet(charset string) (byte, error) {
	n := len(charset)
	// largest multiple of n that fits in a byte
	limit := 256 - (256 % n)
	raw := make([]byte, 1)
	for {
		if _, err := rand.Read(raw); err != nil {
			return 0, err
		}
		if int(raw[0]) < limit {
			return charset[int(raw[0])%n], nil
		}
	}
}

// Generate creates 'count' new CDKs for the given app/agent and inserts them
// into the database. Returns the list of generated codes.
func (s *Service) Generate(appID, gameName, agentID, createdBy, note string, count int) ([]string, error) {
	if count < 1 || count > 100 {
		return nil, ErrInvalidCount
	}

	codes := make([]string, 0, count)
	keys := make([]db.CDKey, 0, count)
	now := time.Now()

	for i := 0; i < count; i++ {
		code, err := generateCode()
		if err != nil {
			return nil, fmt.Errorf("generate code: %w", err)
		}
		codes = append(codes, code)
		keys = append(keys, db.CDKey{
			Code:      code,
			AppID:     appID,
			GameName:  gameName,
			AgentID:   agentID,
			CreatedBy: createdBy,
			Note:      note,
			CreatedAt: now,
		})
	}

	if err := s.DB.Create(&keys).Error; err != nil {
		return nil, fmt.Errorf("insert CDKs: %w", err)
	}
	return codes, nil
}

// NormalizeCode uppercases, trims whitespace, strips internal spaces, and
// ensures the result follows the XXXX-XXXX-XXXX-XXXX format. If the input
// contains 16 alphanumeric characters with no dashes, dashes are inserted.
func NormalizeCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")

	// If the caller passed raw hex/alphanum without dashes, re-insert them.
	stripped := strings.ReplaceAll(code, "-", "")
	if len(stripped) == 16 {
		return fmt.Sprintf("%s-%s-%s-%s",
			stripped[0:4], stripped[4:8], stripped[8:12], stripped[12:16])
	}
	return code
}

// Validate looks up a CDK by its code and checks that it has not been used
// or revoked. Returns the CDKey record on success.
func (s *Service) Validate(code string) (*db.CDKey, error) {
	code = NormalizeCode(code)

	var key db.CDKey
	if err := s.DB.Where("code = ?", code).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query CDK: %w", err)
	}

	if key.Used {
		return nil, ErrAlreadyUsed
	}
	if key.Revoked {
		return nil, ErrRevoked
	}
	return &key, nil
}

// Redeem validates a CDK and then atomically marks it as used, recording the
// machine identifier. An ActivationLog entry is written regardless of outcome.
func (s *Service) Redeem(code, machine string) (*db.CDKey, error) {
	code = NormalizeCode(code)

	var result *db.CDKey

	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// Validate within the transaction.
		var key db.CDKey
		if err := tx.Where("code = ?", code).First(&key).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logActivation(tx, code, "", machine, false)
				return ErrNotFound
			}
			return fmt.Errorf("query CDK: %w", err)
		}

		if key.Used {
			logActivation(tx, code, key.AppID, machine, false)
			return ErrAlreadyUsed
		}
		if key.Revoked {
			logActivation(tx, code, key.AppID, machine, false)
			return ErrRevoked
		}

		now := time.Now()
		key.Used = true
		key.UsedAt = &now
		key.UsedMachine = machine

		if err := tx.Save(&key).Error; err != nil {
			return fmt.Errorf("update CDK: %w", err)
		}

		logActivation(tx, code, key.AppID, machine, true)

		result = &key
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// logActivation writes an ActivationLog entry.
func logActivation(tx *gorm.DB, code, appID, machine string, ok bool) {
	entry := db.ActivationLog{
		CDK:     code,
		AppID:   appID,
		Machine: machine,
		OK:      ok,
		At:      time.Now(),
	}
	// Best-effort; do not fail the parent operation on log write error.
	_ = tx.Create(&entry).Error
}

// List retrieves CDKeys with optional filtering by agent and usage status.
// filter must be one of "all", "used", or "unused". limit caps the result set.
func (s *Service) List(agentID string, limit int, filter string) ([]db.CDKey, error) {
	q := s.DB.Model(&db.CDKey{})

	if agentID != "" {
		q = q.Where("agent_id = ?", agentID)
	}

	switch strings.ToLower(filter) {
	case "used":
		q = q.Where("used = ?", true)
	case "unused":
		q = q.Where("used = ?", false)
	case "all", "":
		// no additional filter
	default:
		return nil, fmt.Errorf("unknown filter %q: must be all, used, or unused", filter)
	}

	if limit > 0 {
		q = q.Limit(limit)
	}

	var keys []db.CDKey
	if err := q.Order("created_at DESC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("list CDKs: %w", err)
	}
	return keys, nil
}

// Revoke marks a CDK as revoked so it can no longer be redeemed.
func (s *Service) Revoke(code string) error {
	code = NormalizeCode(code)

	res := s.DB.Model(&db.CDKey{}).Where("code = ?", code).Update("revoked", true)
	if res.Error != nil {
		return fmt.Errorf("revoke CDK: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

