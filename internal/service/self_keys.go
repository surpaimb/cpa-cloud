// Independently authored for docs/employee-self-key-inventory-contract.md.
package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const selfKeyPageSize = 20
const selfKeyMaxPageSize = 50
const selfKeyMaxCursorLength = 180

type selfKeyItem struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
	RevokedAt *string `json:"revoked_at"`
	Status    string  `json:"status"`
}

type selfKeyPage struct {
	Items      []selfKeyItem `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

func parseSelfKeyQuery(raw string) (int, string, error) {
	if len(raw) > 512 {
		return 0, "", errors.New("query too long")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", err
	}
	limit := selfKeyPageSize
	var after string
	for name, list := range values {
		if len(list) != 1 {
			return 0, "", errors.New("duplicate query parameter")
		}
		switch name {
		case "limit":
			value := list[0]
			if value == "" || len(value) > 2 || value[0] == '0' {
				return 0, "", errors.New("invalid limit")
			}
			for _, digit := range value {
				if digit < '0' || digit > '9' {
					return 0, "", errors.New("invalid limit")
				}
			}
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > selfKeyMaxPageSize {
				return 0, "", errors.New("invalid limit")
			}
		case "cursor":
			value := list[0]
			if len(value) > selfKeyMaxCursorLength || !strings.HasPrefix(value, "v1.") {
				return 0, "", errors.New("invalid cursor")
			}
			decoded, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(value, "v1."))
			if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != strings.TrimPrefix(value, "v1.") || !validSelfKeyID(string(decoded)) {
				return 0, "", errors.New("invalid cursor")
			}
			after = string(decoded)
		default:
			return 0, "", errors.New("unknown query parameter")
		}
	}
	return limit, after, nil
}

func validSelfKeyID(id string) bool {
	return utf8.ValidString(id) && validIdentifier(id, 128)
}

func selfKeyTime(value string) (string, time.Time, error) {
	t, err := parseTime(value)
	if err != nil {
		return "", time.Time{}, err
	}
	return t.UTC().Format(time.RFC3339Nano), t, nil
}

func readSelfKeyPage(ctx context.Context, db *sql.DB, employeeID, after string, limit int, now time.Time) (selfKeyPage, error) {
	page := selfKeyPage{Items: make([]selfKeyItem, 0, limit)}
	rows, err := db.QueryContext(ctx, `SELECT id,name,created_at,expires_at,revoked_at,typeof(id),typeof(name),typeof(created_at),typeof(expires_at),typeof(revoked_at)
		FROM access_keys WHERE employee_id=? AND id COLLATE BINARY > ? ORDER BY id COLLATE BINARY LIMIT ?`, employeeID, after, limit+1)
	if err != nil {
		return selfKeyPage{}, err
	}
	for rows.Next() {
		var item selfKeyItem
		var created string
		var expires, revoked sql.NullString
		var idType, nameType, createdType, expiresType, revokedType string
		if err := rows.Scan(&item.ID, &item.Name, &created, &expires, &revoked, &idType, &nameType, &createdType, &expiresType, &revokedType); err != nil {
			_ = rows.Close()
			return selfKeyPage{}, err
		}
		if idType != "text" || nameType != "text" || createdType != "text" ||
			(expiresType != "text" && expiresType != "null") || (revokedType != "text" && revokedType != "null") ||
			!validSelfKeyID(item.ID) || !utf8.ValidString(item.Name) || !validText(item.Name, 1, 120) {
			_ = rows.Close()
			return selfKeyPage{}, errors.New("invalid key metadata")
		}
		item.CreatedAt, _, err = selfKeyTime(created)
		if err != nil {
			_ = rows.Close()
			return selfKeyPage{}, err
		}
		if expires.Valid {
			var expiry time.Time
			var normalized string
			normalized, expiry, err = selfKeyTime(expires.String)
			if err != nil {
				_ = rows.Close()
				return selfKeyPage{}, err
			}
			item.ExpiresAt = &normalized
			if !now.Before(expiry) {
				item.Status = "expired"
			}
		}
		if revoked.Valid {
			var normalized string
			normalized, _, err = selfKeyTime(revoked.String)
			if err != nil {
				_ = rows.Close()
				return selfKeyPage{}, err
			}
			item.RevokedAt = &normalized
			item.Status = "revoked"
		}
		if item.Status == "" {
			item.Status = "active"
		}
		page.Items = append(page.Items, item)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return selfKeyPage{}, errors.New("key inventory read failed")
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		cursor := "v1." + base64.RawURLEncoding.EncodeToString([]byte(page.Items[limit-1].ID))
		page.NextCursor = &cursor
	}
	return page, nil
}

func (a *App) selfListKeys(w http.ResponseWriter, r *http.Request, session selfSession) {
	limit, after, err := parseSelfKeyQuery(r.URL.RawQuery)
	// Unknown-length bodies are rejected too: HTTP/2 can carry DATA without
	// Transfer-Encoding, and reading a body here would hold admission.RLock.
	if err != nil || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	page, err := readSelfKeyPage(r.Context(), a.store.db, session.EmployeeID, after, limit, time.Now().UTC())
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
