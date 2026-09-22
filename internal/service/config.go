package service

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	DataDir               string
	Listen                string
	WebDir                string
	TLSCert               string
	TLSKey                string
	AllowLoopbackUpstream bool
	Version               string
}

const (
	adminPasswordMinBytes = 12
	adminPasswordMaxBytes = 72
)

func Initialize(ctx context.Context, dataDir string, passwordReader io.Reader) error {
	if strings.TrimSpace(dataDir) == "" {
		return errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	rootPath := dataDir + string(os.PathSeparator) + rootKeyFilename
	if _, err := os.Stat(rootPath); errors.Is(err, os.ErrNotExist) {
		if err := createRootKey(dataDir); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("inspect root key: %w", err)
	}
	s, err := openStore(dataDir)
	if err != nil {
		return err
	}
	defer s.close()
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return fmt.Errorf("inspect administrators: %w", err)
	}
	if count != 0 {
		return errors.New("administrator is already initialized")
	}
	reader := bufio.NewReader(io.LimitReader(passwordReader, 4097))
	passwordBytes, err := io.ReadAll(reader)
	if err != nil {
		return errors.New("read administrator password")
	}
	password := strings.TrimRight(string(passwordBytes), "\r\n")
	if len(passwordBytes) > 4096 {
		return errors.New("administrator password is too long")
	}
	if err := validateAdminPassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("hash administrator password: %w", err)
	}
	id, err := newID("adm")
	if err != nil {
		return fmt.Errorf("generate administrator id: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO admins(id,username,password_hash,created_at) VALUES(?,?,?,?)`, id, "admin", hash, utcNow()); err != nil {
		return fmt.Errorf("create administrator: %w", err)
	}
	return nil
}

func validateAdminPassword(password string) error {
	if len(password) < adminPasswordMinBytes || len(password) > adminPasswordMaxBytes {
		return errors.New("administrator password must be 12 to 72 bytes")
	}
	return nil
}

func ensureInitialized(ctx context.Context, db *sql.DB) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("exactly one initialized administrator is required; run with --init")
	}
	return nil
}

func trimExpiredSessions(ctx context.Context, db *sql.DB) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, utcNow())
}
